package backup

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Run kinds.
const (
	KindScheduled = "scheduled"
	KindManual    = "manual"
	KindVerify    = "verify"
	KindCLI       = "cli"
)

// Run statuses.
const (
	StatusRunning = "running"
	StatusSuccess = "success"
	StatusPartial = "partial"
	StatusFailed  = "failed"
)

// Run is one backup/verify execution record.
type Run struct {
	ID                string     `json:"id"`
	Kind              string     `json:"kind"`
	Status            string     `json:"status"`
	StartedAt         time.Time  `json:"startedAt"`
	FinishedAt        *time.Time `json:"finishedAt,omitempty"`
	DbBytes           int64      `json:"dbBytes"`
	FilesNew          int64      `json:"filesNew"`
	FilesChanged      int64      `json:"filesChanged"`
	DataAddedBytes    int64      `json:"dataAddedBytes"`
	LocalSnapshotID   string     `json:"localSnapshotId"`
	OffsiteSnapshotID string     `json:"offsiteSnapshotId"`
	OffsiteOK         bool       `json:"offsiteOk"`
	VerifyOK          bool       `json:"verifyOk"`
	ProbeRelPath      string     `json:"probeRelPath,omitempty"`
	Error             string     `json:"error,omitempty"`
}

// FinishUpdate carries the fields written when a run completes.
type FinishUpdate struct {
	Status            string
	DbBytes           int64
	FilesNew          int64
	FilesChanged      int64
	DataAddedBytes    int64
	LocalSnapshotID   string
	OffsiteSnapshotID string
	OffsiteOK         bool
	VerifyOK          bool
	ProbeRelPath      string
	ProbeSHA256       string
	Error             string
}

func InsertRun(ctx context.Context, db *pgxpool.Pool, kind string) (string, error) {
	var id string
	err := db.QueryRow(ctx, `
		insert into backup_runs (kind, status)
		values ($1, $2)
		returning id::text`, kind, StatusRunning,
	).Scan(&id)
	if err != nil {
		return "", err
	}
	return id, nil
}

// MarkOrphanedRunning flips stale "running" rows (previous crash) to failed so
// the history never shows a phantom in-progress run forever.
func MarkOrphanedRunning(ctx context.Context, db *pgxpool.Pool, olderThan time.Duration) error {
	_, err := db.Exec(ctx, `
		update backup_runs
		set status = $1, finished_at = now(), error = 'process exited before the run finished'
		where status = $2 and started_at < now() - make_interval(secs => $3)`,
		StatusFailed, StatusRunning, olderThan.Seconds(),
	)
	return err
}

func FinishRun(ctx context.Context, db *pgxpool.Pool, id string, u FinishUpdate) error {
	_, err := db.Exec(ctx, `
		update backup_runs
		set status = $2, finished_at = now(),
		    db_bytes = $3, files_new = $4, files_changed = $5, data_added_bytes = $6,
		    local_snapshot_id = $7, offsite_snapshot_id = $8,
		    offsite_ok = $9, verify_ok = $10,
		    probe_relpath = $11, probe_sha256 = $12, error = $13
		where id = $1::uuid`,
		id, u.Status, u.DbBytes, u.FilesNew, u.FilesChanged, u.DataAddedBytes,
		u.LocalSnapshotID, u.OffsiteSnapshotID, u.OffsiteOK, u.VerifyOK,
		u.ProbeRelPath, u.ProbeSHA256, u.Error,
	)
	return err
}

func scanRun(scanner interface {
	Scan(dest ...any) error
}) (Run, error) {
	var r Run
	err := scanner.Scan(
		&r.ID, &r.Kind, &r.Status, &r.StartedAt, &r.FinishedAt,
		&r.DbBytes, &r.FilesNew, &r.FilesChanged, &r.DataAddedBytes,
		&r.LocalSnapshotID, &r.OffsiteSnapshotID, &r.OffsiteOK, &r.VerifyOK,
		&r.ProbeRelPath, &r.Error,
	)
	return r, err
}

const runColumns = `id::text, kind, status, started_at, finished_at,
	db_bytes, files_new, files_changed, data_added_bytes,
	local_snapshot_id, offsite_snapshot_id, offsite_ok, verify_ok,
	probe_relpath, error`

func ListRuns(ctx context.Context, db *pgxpool.Pool, limit int) ([]Run, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := db.Query(ctx, `select `+runColumns+` from backup_runs order by started_at desc limit $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Run, 0, limit)
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func GetRun(ctx context.Context, db *pgxpool.Pool, id string) (*Run, error) {
	r, err := scanRun(db.QueryRow(ctx, `select `+runColumns+` from backup_runs where id = $1::uuid`, id))
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func lastRunWhere(ctx context.Context, db *pgxpool.Pool, where string) (*Run, error) {
	r, err := scanRun(db.QueryRow(ctx,
		`select `+runColumns+` from backup_runs where `+where+` order by started_at desc limit 1`))
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// LastRun returns the most recently started run of any status.
func LastRun(ctx context.Context, db *pgxpool.Pool) (*Run, error) {
	return lastRunWhere(ctx, db, "true")
}

// LastSuccess returns the most recent successful full backup
// (verify-only runs do not count as data protection).
func LastSuccess(ctx context.Context, db *pgxpool.Pool) (*Run, error) {
	return lastRunWhere(ctx, db, "status = 'success' and kind in ('scheduled','manual','cli')")
}

// LastProbe returns the probe file recorded by the latest verified backup,
// used by verify-only runs to re-check blob restorability.
func LastProbe(ctx context.Context, db *pgxpool.Pool) (relPath, sha256 string, err error) {
	err = db.QueryRow(ctx, `
		select probe_relpath, probe_sha256 from backup_runs
		where verify_ok = true and probe_relpath <> ''
		order by started_at desc limit 1`,
	).Scan(&relPath, &sha256)
	return relPath, sha256, err
}
