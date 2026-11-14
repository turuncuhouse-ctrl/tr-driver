package backup

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrAlreadyRunning is returned when a run is requested while another is active.
var ErrAlreadyRunning = errors.New("a backup run is already in progress")

// ErrNotConfigured is returned when backups cannot run (disabled or missing secret).
var ErrNotConfigured = errors.New("backups are not configured")

const dumpFileName = "db.dump"

// Service orchestrates scheduled and manual backup runs.
type Service struct {
	db          *pgxpool.Pool
	cfg         Config
	dataDir     string
	databaseURL string
	restic      *Client

	mu           sync.Mutex
	running      bool
	runningID    string
	runningKind  string
	runningSince time.Time

	schedulerOn bool
	schedHour   int
	schedMin    int
	nextRun     time.Time

	startTime      time.Time
	lastStaleAlert time.Time
}

// NewService builds the backup service. dataDir and cfg.Dir are absolutised so
// restic snapshot paths are stable across restarts.
func NewService(db *pgxpool.Pool, cfg Config, dataDir, databaseURL string) *Service {
	if abs, err := filepath.Abs(dataDir); err == nil {
		dataDir = abs
	}
	if abs, err := filepath.Abs(cfg.Dir); err == nil {
		cfg.Dir = abs
	}
	return &Service{
		db:          db,
		cfg:         cfg,
		dataDir:     dataDir,
		databaseURL: databaseURL,
		restic:      &Client{Bin: cfg.ResticBin, Password: cfg.Password},
		startTime:   time.Now(),
	}
}

// Configured reports whether automatic/manual runs are possible.
func (s *Service) Configured() (bool, string) {
	if !s.cfg.Enabled {
		return false, "BACKUP_ENABLED=false"
	}
	if s.cfg.Password == "" {
		return false, "BACKUP_PASSWORD is not set"
	}
	if err := s.cfg.Validate(); err != nil {
		return false, err.Error()
	}
	return true, ""
}

func (s *Service) localRepo() Repo {
	return Repo{Location: s.cfg.Dir}
}

func (s *Service) offsiteRepo() (Repo, bool) {
	if !s.cfg.S3Enabled() {
		return Repo{}, false
	}
	return Repo{
		Location: s.cfg.S3Repo(),
		Env:      s.cfg.S3Env(),
		Options:  s.cfg.S3Options(),
	}, true
}

// StagingDir holds the transient pg_dump file next to (never inside) the repo.
func (s *Service) StagingDir() string {
	return filepath.Join(filepath.Dir(s.cfg.Dir), "staging")
}

// Status describes the backup subsystem for admins and monitoring.
type Status struct {
	Enabled        bool            `json:"enabled"`
	Configured     bool            `json:"configured"`
	ConfigError    string          `json:"configError,omitempty"`
	Schedule       string          `json:"schedule"`
	NextRun        *time.Time      `json:"nextRun,omitempty"`
	MaxAgeHours    float64         `json:"maxAgeHours"`
	Stale          bool            `json:"stale"`
	Running        bool            `json:"running"`
	RunningKind    string          `json:"runningKind,omitempty"`
	RunningSince   *time.Time      `json:"runningSince,omitempty"`
	LocalRepo      string          `json:"localRepo"`
	OffsiteEnabled bool            `json:"offsiteEnabled"`
	Binaries       map[string]bool `json:"binaries"`
	LastRun        *Run            `json:"lastRun,omitempty"`
	LastSuccessAt  *time.Time      `json:"lastSuccessAt,omitempty"`
}

// Status collects the current backup state.
func (s *Service) Status(ctx context.Context) (Status, error) {
	configured, reason := s.Configured()
	st := Status{
		Enabled:        s.cfg.Enabled,
		Configured:     configured,
		Schedule:       s.cfg.Schedule,
		MaxAgeHours:    s.cfg.MaxAge.Hours(),
		LocalRepo:      s.cfg.Dir,
		OffsiteEnabled: s.cfg.S3Enabled(),
		Binaries: map[string]bool{
			"restic":     lookPathOK(s.cfg.ResticBin),
			"pg_dump":    lookPathOK(s.cfg.PgDumpBin),
			"pg_restore": lookPathOK(s.cfg.PgRestoreBin),
		},
	}
	if !configured {
		st.ConfigError = reason
	}
	s.mu.Lock()
	st.Running = s.running
	if s.running {
		st.RunningKind = s.runningKind
		since := s.runningSince
		st.RunningSince = &since
	}
	if s.schedulerOn {
		next := s.nextRun
		st.NextRun = &next
	}
	s.mu.Unlock()

	if last, err := LastRun(ctx, s.db); err == nil {
		st.LastRun = last
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return Status{}, err
	}
	if ok, err := LastSuccess(ctx, s.db); err == nil {
		at := ok.StartedAt
		st.LastSuccessAt = &at
		st.Stale = configured && time.Since(ok.StartedAt) > s.cfg.MaxAge
	} else if errors.Is(err, pgx.ErrNoRows) {
		st.Stale = configured
	} else {
		return Status{}, err
	}
	return st, nil
}

// RunNow starts a full backup asynchronously and returns its run id.
// kind is "manual", "scheduled" or "cli".
func (s *Service) RunNow(ctx context.Context, kind string) (string, error) {
	switch kind {
	case KindScheduled, KindManual, KindCLI:
	default:
		kind = KindManual
	}
	id, err := s.startRun(ctx, kind)
	if err != nil {
		return "", err
	}
	go s.execute(context.WithoutCancel(ctx), id, kind)
	return id, nil
}

// VerifyNow starts a verify-only run asynchronously.
func (s *Service) VerifyNow(ctx context.Context) (string, error) {
	id, err := s.startRun(ctx, KindVerify)
	if err != nil {
		return "", err
	}
	go s.execute(context.WithoutCancel(ctx), id, KindVerify)
	return id, nil
}

// RunSync runs a full backup synchronously (CLI and tests).
func (s *Service) RunSync(ctx context.Context, kind string) (*Run, error) {
	id, err := s.startRun(ctx, kind)
	if err != nil {
		return nil, err
	}
	s.execute(ctx, id, kind)
	return GetRun(ctx, s.db, id)
}

// VerifySync runs verification synchronously (CLI).
func (s *Service) VerifySync(ctx context.Context) (*Run, error) {
	id, err := s.startRun(ctx, KindVerify)
	if err != nil {
		return nil, err
	}
	s.execute(ctx, id, KindVerify)
	return GetRun(ctx, s.db, id)
}

func (s *Service) startRun(ctx context.Context, kind string) (string, error) {
	if ok, reason := s.Configured(); !ok {
		return "", fmt.Errorf("%w: %s", ErrNotConfigured, reason)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return "", ErrAlreadyRunning
	}
	id, err := InsertRun(ctx, s.db, kind)
	if err != nil {
		return "", err
	}
	s.running = true
	s.runningID = id
	s.runningKind = kind
	s.runningSince = time.Now()
	return id, nil
}

func (s *Service) execute(ctx context.Context, id, kind string) {
	defer func() {
		s.mu.Lock()
		s.running = false
		s.runningID = ""
		s.mu.Unlock()
	}()
	if recovered := func() (rec any) {
		defer func() { rec = recover() }()
		s.executeInner(ctx, id, kind)
		return nil
	}(); recovered != nil {
		log.Printf("backup run %s panicked: %v", id, recovered)
		_ = FinishRun(context.Background(), s.db, id, FinishUpdate{
			Status: StatusFailed, Error: fmt.Sprintf("internal panic: %v", recovered),
		})
		_ = Notify(s.cfg.WebhookURL, EventFailed, nil, fmt.Sprintf("backup run %s panicked", id))
	}
}

func (s *Service) executeInner(ctx context.Context, id, kind string) {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()
	log.Printf("backup run %s (%s) started", id, kind)

	var update FinishUpdate
	if kind == KindVerify {
		update = s.executeVerify(ctx)
	} else {
		update = s.executeBackup(ctx)
	}

	if err := FinishRun(context.WithoutCancel(ctx), s.db, id, update); err != nil {
		log.Printf("backup run %s: record result: %v", id, err)
	}
	run, _ := GetRun(context.Background(), s.db, id)
	event := EventSuccess
	switch update.Status {
	case StatusPartial:
		event = EventPartial
	case StatusFailed:
		event = EventFailed
	}
	if update.Status == StatusFailed || s.cfg.WebhookURL != "" {
		if err := Notify(s.cfg.WebhookURL, event, run, update.Error); err != nil {
			log.Printf("backup run %s: webhook: %v", id, err)
		}
	}
	log.Printf("backup run %s (%s) finished: %s (verify=%v offsite=%v) %s",
		id, kind, update.Status, update.VerifyOK, update.OffsiteOK, update.Error)
}

// executeBackup performs: pg_dump -> restic local -> restic offsite ->
// retention -> restore rehearsal. It never returns a partial FinishUpdate
// without a terminal Status.
func (s *Service) executeBackup(ctx context.Context) FinishUpdate {
	update := FinishUpdate{Status: StatusSuccess}
	var problems []string

	// Refuse layouts that would recurse or miss the database dump.
	if withinDir(s.dataDir, s.cfg.Dir) {
		update.Status = StatusFailed
		update.Error = fmt.Sprintf("refusing run: DATA_DIR %s is inside BACKUP_DIR %s", s.dataDir, s.cfg.Dir)
		return update
	}

	staging := s.StagingDir()
	if err := os.MkdirAll(staging, 0o755); err != nil {
		update.Status = StatusFailed
		update.Error = fmt.Sprintf("staging dir: %v", err)
		return update
	}
	dumpPath := filepath.Join(staging, dumpFileName)

	dbBytes, err := Dump(ctx, s.cfg.PgDumpBin, s.databaseURL, dumpPath)
	if err != nil {
		update.Status = StatusFailed
		update.Error = err.Error()
		return update
	}
	update.DbBytes = dbBytes
	if _, err := RestoreList(ctx, s.cfg.PgRestoreBin, dumpPath); err != nil {
		update.Status = StatusFailed
		update.Error = fmt.Sprintf("fresh dump failed sanity check: %v", err)
		return update
	}

	local := s.localRepo()
	if err := s.restic.EnsureInit(ctx, local); err != nil {
		update.Status = StatusFailed
		update.Error = err.Error()
		return update
	}

	probeRel, probeSHA, probeErr := PickProbe(s.dataDir)
	if probeErr == nil {
		update.ProbeRelPath = probeRel
		update.ProbeSHA256 = probeSHA
	}

	// Anchored excludes: only the top-level transient dirs are skipped. A bare
	// "tmp" pattern would also skip a user file literally named tmp at any
	// depth. dataDir is absolute (see NewService) so anchoring matches what
	// restic scans.
	excludes := []string{
		filepath.ToSlash(filepath.Join(s.dataDir, "tmp")),
		filepath.ToSlash(filepath.Join(s.dataDir, "lost+found")),
	}
	if rel, ok := relWithin(s.dataDir, staging); ok {
		excludes = append(excludes, filepath.ToSlash(filepath.Join(s.dataDir, rel)))
	}
	if rel, ok := relWithin(s.dataDir, s.cfg.Dir); ok {
		excludes = append(excludes, filepath.ToSlash(filepath.Join(s.dataDir, rel)))
	}
	kind := s.currentKind()
	summary, err := s.restic.Backup(ctx, local,
		[]string{"trdriver", kind}, excludes, []string{dumpPath, s.dataDir})
	if err != nil {
		update.Status = StatusFailed
		update.Error = err.Error()
		return update
	}
	update.LocalSnapshotID = summary.SnapshotID
	update.FilesNew = summary.FilesNew
	update.FilesChanged = summary.FilesChanged
	update.DataAddedBytes = summary.DataAdded

	offsite, hasOffsite := s.offsiteRepo()
	if hasOffsite {
		if err := s.restic.EnsureInit(ctx, offsite); err != nil {
			problems = append(problems, "offsite init: "+err.Error())
		} else if offSummary, err := s.restic.Backup(ctx, offsite,
			[]string{"trdriver", kind}, excludes, []string{dumpPath, s.dataDir}); err != nil {
			problems = append(problems, "offsite backup: "+err.Error())
		} else {
			update.OffsiteSnapshotID = offSummary.SnapshotID
			update.OffsiteOK = true
		}
	}

	if err := s.restic.Forget(ctx, local, s.cfg.KeepDaily, s.cfg.KeepWeekly, s.cfg.KeepMonthly); err != nil {
		problems = append(problems, "local retention: "+err.Error())
	}
	if hasOffsite && update.OffsiteOK {
		if err := s.restic.Forget(ctx, offsite, s.cfg.KeepDaily, s.cfg.KeepWeekly, s.cfg.KeepMonthly); err != nil {
			problems = append(problems, "offsite retention: "+err.Error())
		}
	}

	// Restore rehearsal: prove the fresh snapshot actually restores.
	if err := s.verifySnapshot(ctx, local, summary.SnapshotID, true, probeRel, probeSHA); err != nil {
		problems = append(problems, "verify local: "+err.Error())
	} else {
		update.VerifyOK = true
	}
	if hasOffsite && update.OffsiteOK {
		// Offsite gets a metadata check only: re-downloading blobs every
		// night would burn bandwidth to re-prove identical bytes.
		if err := s.restic.Check(ctx, offsite); err != nil {
			update.OffsiteOK = false
			problems = append(problems, "verify offsite: "+err.Error())
		}
	}

	if s.cfg.VerifyRestore && update.VerifyOK {
		if err := RestoreRehearsal(ctx, s.databaseURL, s.cfg.PgRestoreBin, dumpPath); err != nil {
			problems = append(problems, "restore rehearsal: "+err.Error())
			update.VerifyOK = false
		}
	}

	switch {
	case hasOffsite && !update.OffsiteOK:
		update.Status = StatusPartial
	case !update.VerifyOK:
		update.Status = StatusPartial
	case len(problems) > 0:
		update.Status = StatusPartial
	}
	update.Error = strings.Join(problems, "; ")
	if len(update.Error) > 4000 {
		update.Error = update.Error[:4000]
	}
	return update
}

// executeVerify re-checks the latest snapshots without taking a new one.
func (s *Service) executeVerify(ctx context.Context) FinishUpdate {
	update := FinishUpdate{Status: StatusSuccess}
	local := s.localRepo()

	snapshots, err := s.restic.Snapshots(ctx, local)
	if err != nil {
		update.Status = StatusFailed
		update.Error = err.Error()
		return update
	}
	if len(snapshots) == 0 {
		update.Status = StatusFailed
		update.Error = "local repository has no snapshots to verify"
		return update
	}
	probeRel, probeSHA, _ := LastProbe(ctx, s.db)
	if err := s.verifySnapshot(ctx, local, snapshots[0].ID, true, probeRel, probeSHA); err != nil {
		update.Status = StatusFailed
		update.Error = "verify local: " + err.Error()
		return update
	}
	update.VerifyOK = true
	update.LocalSnapshotID = snapshots[0].ID

	if offsite, ok := s.offsiteRepo(); ok {
		offSnapshots, err := s.restic.Snapshots(ctx, offsite)
		if err != nil {
			update.Status = StatusPartial
			update.Error = "offsite snapshots: " + err.Error()
			return update
		}
		if len(offSnapshots) == 0 {
			update.Status = StatusPartial
			update.Error = "offsite repository has no snapshots"
			return update
		}
		if err := s.restic.Check(ctx, offsite); err != nil {
			update.Status = StatusPartial
			update.Error = "verify offsite: " + err.Error()
			return update
		}
		update.OffsiteOK = true
		update.OffsiteSnapshotID = offSnapshots[0].ID
	}
	return update
}

// verifySnapshot runs restic check, then restores the database dump and the
// probe file from the snapshot and validates both.
func (s *Service) verifySnapshot(ctx context.Context, repo Repo, snapshotID string, wantDump bool, probeRel, probeSHA string) error {
	if err := s.restic.Check(ctx, repo); err != nil {
		return err
	}
	if wantDump {
		stored, err := s.restic.FindPath(ctx, repo, snapshotID, dumpFileName)
		if err != nil {
			return fmt.Errorf("dump lookup: %w", err)
		}
		restored, err := s.restoreSingle(ctx, repo, snapshotID, stored)
		if err != nil {
			return fmt.Errorf("dump restore: %w", err)
		}
		defer os.RemoveAll(filepath.Dir(restored))
		if _, err := RestoreList(ctx, s.cfg.PgRestoreBin, restored); err != nil {
			return fmt.Errorf("restored dump unreadable: %w", err)
		}
	}
	if probeRel != "" && probeSHA != "" {
		stored := filepath.ToSlash(filepath.Join(s.dataDir, filepath.FromSlash(probeRel)))
		restored, err := s.restoreSingle(ctx, repo, snapshotID, stored)
		if err != nil {
			// Fall back to a basename search (some restic versions normalise
			// stored paths); require a full relative-path suffix match so a
			// same-named file in another folder can never pass.
			candidates, findErr := s.restic.FindPaths(ctx, repo, snapshotID, filepath.Base(probeRel))
			if findErr != nil {
				return fmt.Errorf("probe restore: %w", err)
			}
			matched := ""
			for _, c := range candidates {
				if c == stored || strings.HasSuffix(c, "/"+probeRel) {
					matched = c
					break
				}
			}
			if matched == "" {
				return fmt.Errorf("probe restore: %w", err)
			}
			if restored, err = s.restoreSingle(ctx, repo, snapshotID, matched); err != nil {
				return fmt.Errorf("probe restore: %w", err)
			}
		}
		defer os.RemoveAll(filepath.Dir(restored))
		digest, err := HashFile(restored)
		if err != nil {
			return fmt.Errorf("probe hash: %w", err)
		}
		if !strings.EqualFold(digest, probeSHA) {
			return fmt.Errorf("probe %s restored with wrong content", probeRel)
		}
	}
	return nil
}

func (s *Service) restoreSingle(ctx context.Context, repo Repo, snapshotID, includePath string) (string, error) {
	target, err := os.MkdirTemp("", "trdriver-verify-")
	if err != nil {
		return "", err
	}
	if err := s.restic.RestorePath(ctx, repo, snapshotID, includePath, target); err != nil {
		os.RemoveAll(target)
		return "", err
	}
	found, err := FindSingleFile(target)
	if err != nil {
		os.RemoveAll(target)
		return "", err
	}
	return found, nil
}

func (s *Service) currentKind() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runningKind == "" {
		return KindManual
	}
	return s.runningKind
}

func lookPathOK(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}

// withinDir reports whether child == parent or is nested inside it.
func withinDir(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..")
}

// relWithin returns the child path relative to parent when nested.
func relWithin(parent, child string) (string, bool) {
	rel, err := filepath.Rel(parent, child)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}
