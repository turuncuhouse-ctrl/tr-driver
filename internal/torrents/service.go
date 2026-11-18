package torrents

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
	"golang.org/x/time/rate"

	"necipdrive/internal/sharedspace"
)

var (
	ErrForbidden    = errors.New("torrent yönetimi için yönetici yetkisi gerekir")
	ErrUnavailable  = errors.New("torrent istemcisi kullanılamıyor")
	ErrInvalid      = errors.New("geçersiz torrent")
	ErrNameTaken    = errors.New("Ortak Paylaşım'da aynı adda bir öğe zaten var")
	ErrNoSpace      = errors.New("Ortak Paylaşım'da yeterli boş alan yok")
	ErrTooMany      = errors.New("torrent listesi dolu; önce tamamlananları kaldırın")
	ErrInvalidState = errors.New("torrent bu işlem için uygun durumda değil")
	ErrInvalidLimit = errors.New("geçersiz hız sınırı")
	ErrFileNotFound = errors.New("torrent içinde bu dosya bulunamadı")
)

const (
	// MaxMetainfoBytes bounds an uploaded .torrent file.
	MaxMetainfoBytes = 8 << 20
	// MaxMagnetBytes bounds a magnet link.
	MaxMagnetBytes = 16 << 10
	// MaxLimitKBps is the highest accepted speed limit (about 10 GiB/s).
	MaxLimitKBps = 10 * 1024 * 1024
	// MaxSeedRatioLimit caps the seed ratio a torrent may be given.
	MaxSeedRatioLimit = 1000.0
	// MaxSeedTimeLimitSec caps the seed time (10 years).
	MaxSeedTimeLimitSec = 10 * 365 * 24 * 3600

	defaultMaxTorrents = 200
	defaultTick        = 2 * time.Second
	stallCheckEvery    = 5 // ticks
	persistEvery       = 8 // ticks
	// detachRetention keeps a removed-without-data torrent's row so the same
	// torrent can be re-added and continue; older rows are forgotten (their
	// files are not touched).
	detachRetention = 90 * 24 * time.Hour

	settingDownloadLimit = "torrent_download_limit_kbps"
	settingUploadLimit   = "torrent_upload_limit_kbps"
	notificationKind     = "torrent_completed"

	// uploadBurst must stay at least as large as the biggest block a peer may
	// request: the client reserves a whole block from the limiter at once.
	uploadBurst      = 1 << 20
	minDownloadBurst = 64 << 10
	maxDownloadBurst = 1 << 20

	// diskReserve mirrors the shared space: downloads stop before the disk is
	// completely full.
	diskReserve int64 = 64 << 20
)

// Space is the part of the shared space the torrent service depends on.
type Space interface {
	Dir() string
	Quota(ctx context.Context) (sharedspace.QuotaStatus, error)
	InvalidateUsage()
	Delete(ctx context.Context, actor sharedspace.Actor, path string) error
}

// Actor is the authenticated caller.
type Actor = sharedspace.Actor

// Options configure the embedded client.
type Options struct {
	Enabled           bool
	ListenPort        int
	DownloadLimitKBps int64 // KiB/s, 0 = unlimited
	UploadLimitKBps   int64 // KiB/s, 0 = unlimited
	SeedAfterComplete bool
	DisableDHT        bool
	MaxTorrents       int
	TickInterval      time.Duration
	// StateDir keeps the persistent piece-completion database outside the
	// shared folder, so verified pieces survive a container restart.
	// Empty falls back to an in-memory map (tests only: no persistence).
	StateDir string
	// Configure adjusts the client configuration last. It exists for tests.
	Configure func(*torrent.ClientConfig)
}

// Limits are the global speed limits in KiB/s; 0 means unlimited.
type Limits struct {
	DownloadLimitKBps int64 `json:"downloadLimitKBps"`
	UploadLimitKBps   int64 `json:"uploadLimitKBps"`
}

// View is a torrent as shown to the administrator. It deliberately has no
// magnet link, tracker list or metainfo (those would carry private tracker
// passkeys); the detail endpoint returns sanitised trackers instead.
type View struct {
	ID             string  `json:"id"`
	InfoHash       string  `json:"infoHash"`
	Name           string  `json:"name"`
	Status         string  `json:"status"`
	Message        string  `json:"message,omitempty"`
	Path           string  `json:"path,omitempty"`
	TotalBytes     int64   `json:"totalBytes"`
	CompletedBytes int64   `json:"completedBytes"`
	Progress       float64 `json:"progress"`
	DownloadRate   int64   `json:"downloadRate"`
	UploadRate     int64   `json:"uploadRate"`
	Peers          int     `json:"peers"`
	Seeders        int     `json:"seeders"`
	Ratio          float64 `json:"ratio"`
	ETASec         int64   `json:"etaSec"` // seconds, -1 unknown
	UploadedBytes  int64   `json:"uploadedBytes"`
	SeededSeconds  int64   `json:"seededSeconds"`
	// Per-torrent limits; 0 = unlimited / off.
	DownloadLimitKBps int64      `json:"downloadLimitKBps"`
	UploadLimitKBps   int64      `json:"uploadLimitKBps"`
	SeedRatioLimit    float64    `json:"seedRatioLimit"`
	SeedTimeLimitSec  int        `json:"seedTimeLimitSec"`
	AddedAt           time.Time  `json:"addedAt"`
	CompletedAt       *time.Time `json:"completedAt,omitempty"`
}

// FileDetail is one file inside a torrent, as the drawer shows it. Path is the
// display path used by the file-priority endpoint as well.
type FileDetail struct {
	Path      string `json:"path"`
	Length    int64  `json:"length"`
	Completed int64  `json:"completed"`
	Priority  string `json:"priority"`
}

// TrackerDetail is a tracker address with its path and query stripped: those
// can contain private passkeys, which must never leave the server.
type TrackerDetail struct {
	URL string `json:"url"`
}

// PeerDetail describes one connected peer.
type PeerDetail struct {
	Addr   string `json:"addr"`
	Client string `json:"client,omitempty"`
}

// TorrentDetail powers the drawer in the admin panel.
type TorrentDetail struct {
	View
	Files    []FileDetail    `json:"files"`
	Trackers []TrackerDetail `json:"trackers"`
	Peers    []PeerDetail    `json:"peers"`
}

// Statuses reported in View.Status.
const (
	StatusMetadata    = "metadata"
	StatusVerifying   = "verifying"
	StatusDownloading = "downloading"
	StatusStalled     = "stalled"
	StatusPaused      = "paused"
	StatusCompleted   = "completed"
	StatusSeeding     = "seeding"
	StatusError       = "error"
)

// File priorities accepted by SetFilePriority.
const (
	PrioritySkip   = "skip"
	PriorityNormal = "normal"
	PriorityHigh   = "high"
)

// Overview is everything the admin panel's torrent section shows.
type Overview struct {
	Enabled           bool                     `json:"enabled"`
	Available         bool                     `json:"available"`
	Reason            string                   `json:"reason,omitempty"`
	ListenPort        int                      `json:"listenPort"`
	SeedAfterComplete bool                     `json:"seedAfterComplete"`
	Limits            Limits                   `json:"limits"`
	Torrents          []View                   `json:"torrents"`
	Quota             *sharedspace.QuotaStatus `json:"quota,omitempty"`
}

type item struct {
	rec    Record
	handle *torrent.Torrent
	// admitted is set once the file list is known and the download was
	// accepted against the quota.
	admitted bool
	stalled  bool
	dirty    bool
	// verifying is true while existing pieces are being re-checked after a
	// restart or re-add; the UI shows "Doğrulanıyor".
	verifying bool
	// needsVerify triggers a full piece re-check as soon as the file list is
	// known (restart with content, resume, or re-adding a removed torrent).
	needsVerify bool
	// showVerify makes the re-check visible and blocks "complete" before the
	// hash runs. Resume of a partial download leaves this false so the call
	// still reports "downloading"; a re-add sets it because the completion
	// bitmap can already say every piece is done.
	showVerify bool
	// seededCarry accumulates sub-second seeding time between ticks.
	seededCarry float64
	// seededFrom is the last accrual point; zero while not seeding.
	seededFrom time.Time
	// prios caches the parsed file priorities of rec.FilePriorities.
	prios    map[string]string
	priosRaw string

	lastDown, lastUp int64
	sampledAt        time.Time
	downRate, upRate float64
	peers, seeders   int
}

// priorities returns the parsed file-priority map, re-parsing only when the
// stored JSON changed.
func (it *item) priorities() map[string]string {
	if it.priosRaw == it.rec.FilePriorities && it.prios != nil {
		return it.prios
	}
	parsed := map[string]string{}
	if it.rec.FilePriorities != "" {
		_ = json.Unmarshal([]byte(it.rec.FilePriorities), &parsed)
	}
	it.priosRaw, it.prios = it.rec.FilePriorities, parsed
	return parsed
}

// Service owns the embedded BitTorrent client.
type Service struct {
	opts  Options
	store Store
	space Space

	client      *torrent.Client
	guard       *guardStorage
	fileStorage storage.ClientImplCloser
	// completion persists piece verification across restarts (bolt when a
	// state directory is configured, memory otherwise).
	completion storage.PieceCompletion
	dl, ul     *rate.Limiter
	reason     string // why the client is unavailable, if it is

	mu     sync.Mutex
	items  map[string]*item
	limits Limits
	ticks  int

	runCtx context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New creates the service and, when enabled, starts the client's networking.
// A client that cannot start (for instance because the port is taken) does not
// stop the server: the service reports itself unavailable instead.
func New(store Store, space Space, opts Options) *Service {
	if opts.MaxTorrents <= 0 {
		opts.MaxTorrents = defaultMaxTorrents
	}
	if opts.TickInterval <= 0 {
		opts.TickInterval = defaultTick
	}
	s := &Service{
		opts:   opts,
		store:  store,
		space:  space,
		items:  map[string]*item{},
		limits: Limits{DownloadLimitKBps: max(opts.DownloadLimitKBps, 0), UploadLimitKBps: max(opts.UploadLimitKBps, 0)},
	}
	s.runCtx, s.cancel = context.WithCancel(context.Background())
	if !opts.Enabled {
		s.reason = "Torrent istemcisi TORRENT_ENABLED=false ile kapatılmış."
		return s
	}
	if err := s.startClient(); err != nil {
		// The error comes from opening sockets; it carries no secrets.
		log.Printf("torrent client could not start: %v", err)
		s.reason = "Torrent istemcisi başlatılamadı. Sunucu günlüklerini ve TORRENT_LISTEN_PORT ayarını kontrol edin."
	}
	return s
}

func (s *Service) startClient() error {
	dir := s.space.Dir()
	discard := slog.New(slog.DiscardHandler)
	// Piece verification results are persisted in StateDir (outside the
	// shared folder) so an existing download is neither re-fetched nor
	// re-checked after a container restart. Without a state directory the
	// in-memory map keeps tests hermetic.
	completion := storage.PieceCompletion(storage.NewMapPieceCompletion())
	if s.opts.StateDir != "" {
		if err := os.MkdirAll(s.opts.StateDir, 0o700); err != nil {
			log.Printf("torrent state dir %s not usable, piece state is in-memory only: %v", s.opts.StateDir, err)
		} else if bolt, err := storage.NewBoltPieceCompletion(s.opts.StateDir); err != nil {
			log.Printf("torrent piece-completion db could not be opened, piece state is in-memory only: %v", err)
		} else {
			completion = bolt
		}
	}
	files := storage.NewFileOpts(storage.NewFileClientOpts{
		ClientBaseDir:   dir,
		PieceCompletion: completion,
		Logger:          discard,
	})
	guard := newGuardStorage(dir, files)
	guard.ctx = s.runCtx
	dl := rate.NewLimiter(rate.Inf, maxDownloadBurst)
	ul := rate.NewLimiter(rate.Inf, uploadBurst)

	build := func(disableIPv6 bool) *torrent.ClientConfig {
		cfg := torrent.NewDefaultClientConfig()
		cfg.DataDir = dir
		cfg.DefaultStorage = guard
		cfg.ListenPort = s.opts.ListenPort
		cfg.NoDHT = s.opts.DisableDHT
		cfg.Seed = s.opts.SeedAfterComplete
		cfg.DisableIPv6 = disableIPv6
		// No UPnP/NAT-PMP: the server does not reconfigure the router. Forward
		// the port deliberately if incoming connections are wanted.
		cfg.NoDefaultPortForwarding = true
		// Web seeds and WebTorrent make the server issue HTTP requests to URLs
		// taken from a torrent file; neither is needed.
		cfg.DisableWebseeds = true
		cfg.DisableWebtorrent = true
		cfg.TrackerDialContext = guardedDialer()
		cfg.HTTPDialContext = guardedDialer()
		cfg.DownloadRateLimiter = dl
		cfg.UploadRateLimiter = ul
		// The library logs tracker URLs, which for private trackers contain
		// the user's passkey. Its output is discarded; this package logs the
		// events that matter itself.
		cfg.Slogger = discard
		cfg.Debug = false
		if s.opts.Configure != nil {
			s.opts.Configure(cfg)
		}
		return cfg
	}
	client, err := torrent.NewClient(build(false))
	if err != nil {
		// Containers frequently have no IPv6; retry on IPv4 only.
		var retryErr error
		client, retryErr = torrent.NewClient(build(true))
		if retryErr != nil {
			_ = files.Close()
			return err
		}
	}
	s.client, s.guard, s.fileStorage, s.completion, s.dl, s.ul = client, guard, files, completion, dl, ul
	return nil
}

// Available reports whether torrents can be added.
func (s *Service) Available() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.client != nil
}

// Busy reports whether a shared-space path belongs to a torrent that is still
// downloading or seeding. The shared space refuses to rename or delete those.
func (s *Service) Busy(rel string) bool {
	if s.guard == nil {
		return false
	}
	return s.guard.busy(rel)
}

// PathChanged tells the service that a top-level entry of the shared space was
// renamed or deleted, so a finished torrent stops pointing at it.
func (s *Service) PathChanged(rel string) {
	if rel == "" || strings.Contains(rel, "/") {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, it := range s.items {
		if it.handle == nil && (it.rec.ContentPath == rel || it.rec.ContentPath+".part" == rel) {
			it.rec.ContentPath = ""
			it.dirty = true
		}
	}
}

// Start restores saved torrents and begins the periodic bookkeeping. It
// returns once the saved torrents are loaded.
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Stop the bookkeeping when the server shuts down, without replacing the
	// context that goroutines started earlier are already watching.
	context.AfterFunc(ctx, s.cancel)

	if value, ok, err := s.store.Setting(ctx, settingDownloadLimit); err != nil {
		return err
	} else if ok {
		if parsed, err := strconv.ParseInt(value, 10, 64); err == nil && parsed >= 0 && parsed <= MaxLimitKBps {
			s.limits.DownloadLimitKBps = parsed
		}
	}
	if value, ok, err := s.store.Setting(ctx, settingUploadLimit); err != nil {
		return err
	} else if ok {
		if parsed, err := strconv.ParseInt(value, 10, 64); err == nil && parsed >= 0 && parsed <= MaxLimitKBps {
			s.limits.UploadLimitKBps = parsed
		}
	}
	s.applyLimitsLocked()

	// Forget detached rows (removed without data) that are older than the
	// retention window; their files are never touched by this.
	if err := s.store.PruneDetached(ctx, time.Now().Add(-detachRetention)); err != nil {
		log.Printf("torrent: pruning detached rows failed: %v", err)
	}

	records, err := s.store.List(ctx)
	if err != nil {
		return err
	}
	for _, rec := range records {
		it := &item{rec: rec, admitted: rec.ContentPath != ""}
		s.items[rec.ID] = it
		if s.client == nil {
			continue
		}
		if rec.State == StatePaused {
			// A paused torrent stays out of the client but keeps its folder,
			// so the partial data is still there when it is resumed.
			if rec.ContentPath != "" {
				s.guard.restore(rec.InfoHash, rec.ContentPath)
			}
			continue
		}
		resume := rec.State == StateActive ||
			(rec.State == StateCompleted && s.opts.SeedAfterComplete && !rec.SeedStopped && rec.ContentPath != "")
		if !resume {
			continue
		}
		if rec.ContentPath != "" {
			s.guard.restore(rec.InfoHash, rec.ContentPath)
		}
		// Re-check only when bytes were actually stored. A torrent that was
		// added and restarted before any data arrived has a content path but
		// nothing to hash; putting it in "verifying" would stall the resume.
		it.needsVerify = rec.State == StateActive && rec.ContentPath != "" && rec.CompletedBytes > 0
		it.showVerify = it.needsVerify
		if it.needsVerify && len(rec.Metainfo) > 0 {
			// Hash the files themselves before the client maps them. A cached
			// mapping can still show the old bytes after the file was edited.
			s.quarantineBadPieces(rec.Metainfo)
		}
		if err := s.attachLocked(it); err != nil {
			s.failLocked(it, attachMessage(err))
		}
	}
	if s.client != nil {
		s.wg.Add(1)
		go s.loop()
	}
	return nil
}

// Close stops the client and saves progress.
func (s *Service) Close() {
	s.cancel()
	s.wg.Wait()

	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, it := range s.items {
		if it.handle != nil && it.handle.Info() != nil {
			it.rec.CompletedBytes = it.handle.BytesCompleted()
			it.dirty = true
		}
		if it.dirty {
			_ = s.store.Save(ctx, it.rec)
			it.dirty = false
		}
		it.handle = nil
	}
	if s.client != nil {
		s.client.Close()
		s.client = nil
	}
	if s.fileStorage != nil {
		_ = s.fileStorage.Close()
		s.fileStorage = nil
	}
}

func limitFor(kbps int64) rate.Limit {
	if kbps <= 0 {
		// Zero must never reach the limiter as a literal 0: the client treats
		// that as "no downloading at all" and refuses its own configuration.
		return rate.Inf
	}
	return rate.Limit(kbps * 1024)
}

func (s *Service) applyLimitsLocked() {
	if s.dl == nil {
		return
	}
	burst := maxDownloadBurst
	if s.limits.DownloadLimitKBps > 0 {
		// About one second of traffic: a read never sleeps much longer than
		// that waiting for tokens, so connections do not time out.
		burst = int(min(max(s.limits.DownloadLimitKBps*1024, minDownloadBurst), maxDownloadBurst))
	}
	s.dl.SetBurst(burst)
	s.dl.SetLimit(limitFor(s.limits.DownloadLimitKBps))
	s.ul.SetLimit(limitFor(s.limits.UploadLimitKBps))
}

// errAttach carries a fixed, user-presentable reason; library error text is
// never passed on.
type errAttach struct{ message string }

func (e errAttach) Error() string { return e.message }

func attachMessage(err error) string {
	var attach errAttach
	if errors.As(err, &attach) {
		return attach.message
	}
	return msgAddFailed
}

// specFor rebuilds the client spec from what is stored.
func specFor(rec Record) (*torrent.TorrentSpec, error) {
	if len(rec.Metainfo) > 0 {
		mi, err := metainfo.Load(bytes.NewReader(rec.Metainfo))
		if err != nil {
			return nil, errAttach{msgBadMeta}
		}
		return specForMetaInfo(mi)
	}
	spec, err := torrent.TorrentSpecFromMagnetUri(rec.Magnet)
	if err != nil {
		return nil, errAttach{msgBadMeta}
	}
	hardenSpec(spec)
	return spec, nil
}

func specForMetaInfo(mi *metainfo.MetaInfo) (*torrent.TorrentSpec, error) {
	spec, err := torrent.TorrentSpecFromMetaInfoErr(mi)
	if err != nil {
		return nil, errAttach{msgBadMeta}
	}
	hardenSpec(spec)
	return spec, nil
}

// hardenSpec removes everything in a spec that would make the server contact
// an address chosen by whoever authored the torrent or magnet link, other than
// vetted trackers.
func hardenSpec(spec *torrent.TorrentSpec) {
	spec.Trackers = sanitizeTrackers(spec.Trackers)
	spec.Webseeds = nil  // HTTP(S) URLs fetched by the server
	spec.Sources = nil   // HTTP(S) URLs the metainfo would be fetched from
	spec.PeerAddrs = nil // arbitrary host:port the server would connect to
	spec.DhtNodes = nil  // arbitrary host:port the server would send UDP to
	spec.Storage = nil
}

func (s *Service) attachLocked(it *item) error {
	if s.client == nil {
		return errAttach{ErrUnavailable.Error()}
	}
	spec, err := specFor(it.rec)
	if err != nil {
		return err
	}
	if spec.InfoHash.HexString() != it.rec.InfoHash {
		return errAttach{msgBadMeta}
	}
	handle, _, err := s.client.AddTorrentSpec(spec)
	if err != nil {
		if message := s.guard.takeFailure(it.rec.InfoHash); message != "" {
			return errAttach{message}
		}
		return errAttach{msgAddFailed}
	}
	hash := it.rec.InfoHash
	// Called by the client with its lock held: only record the failure here.
	handle.SetOnWriteChunkError(func(error) { s.guard.fail(hash, msgWriteError) })
	s.guard.setItemLimits(hash, it.rec.DownloadLimitKBps, it.rec.UploadLimitKBps)
	it.handle = handle
	it.stalled = false
	it.sampledAt = time.Time{}
	it.downRate, it.upRate = 0, 0
	if it.rec.State == StateCompleted {
		it.seededFrom = time.Now()
	} else {
		it.seededFrom = time.Time{}
	}
	verify := it.needsVerify
	// The piece-completion database can already say "complete" for a torrent
	// that was removed without deleting its files. Block that until the hash
	// runs. A partial resume does not set showVerify: its bitmap is not
	// complete, and the caller expects status "downloading".
	if verify && it.showVerify {
		it.verifying = true
	}
	s.wg.Add(1)
	go s.awaitInfo(it.rec.ID, handle, verify)
	return nil
}

func (s *Service) awaitInfo(id string, handle *torrent.Torrent, verify bool) {
	defer s.wg.Done()
	select {
	case <-handle.GotInfo():
	case <-handle.Closed():
		return
	case <-s.runCtx.Done():
		return
	}
	if verify && handle.Info() != nil {
		// Re-check the pieces that are already on disk before anything is
		// requested from the network: a torrent re-added after being removed
		// continues from verified data instead of restarting. The check runs
		// without the service lock; errors mean unreadable pieces, which the
		// client then downloads again.
		s.mu.Lock()
		it := s.items[id]
		stillCurrent := it != nil && it.handle == handle
		hash := ""
		if stillCurrent {
			it.verifying = true
			hash = it.rec.InfoHash
		}
		s.mu.Unlock()
		if stillCurrent {
			if err := handle.VerifyDataContext(s.runCtx); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("torrent %s: piece re-check ended early: %v", shortHash(hash), err)
			}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	it := s.items[id]
	if it == nil || it.handle != handle {
		return
	}
	it.verifying = false
	it.needsVerify = false
	s.onInfoLocked(it)
}

// onInfoLocked runs once a torrent's file list is known: at once for a
// .torrent file, after the metadata exchange for a magnet link.
func (s *Service) onInfoLocked(it *item) {
	info := it.handle.Info()
	top, total, err := validateInfo(info)
	if err != nil {
		s.failLocked(it, msgUnsafe)
		return
	}
	if !it.admitted {
		status, err := s.space.Quota(s.runCtx)
		if err != nil || it.handle.BytesMissing() > status.FreeBytes {
			s.failLocked(it, msgNoSpace)
			// Opening storage may have created empty files. The name was
			// claimed as unused, so whatever is there now is this torrent's.
			s.removeContent(top)
			return
		}
		it.admitted = true
	}
	it.rec.Name = top
	it.rec.ContentPath = top
	it.rec.TotalBytes = total
	if len(it.rec.Metainfo) == 0 {
		// Keep the metadata so a restart resumes without fetching it again.
		var buf bytes.Buffer
		mi := it.handle.Metainfo()
		if err := mi.Write(&buf); err == nil && buf.Len() <= 4*MaxMetainfoBytes {
			it.rec.Metainfo = buf.Bytes()
		}
	}
	it.handle.DownloadAll()
	s.applyPrioritiesLocked(it)
	s.saveLocked(it)
	s.space.InvalidateUsage()
}

// applyPrioritiesLocked re-applies the stored per-file choices (after
// DownloadAll, which resets every file to "normal").
func (s *Service) applyPrioritiesLocked(it *item) {
	if it.handle == nil || it.handle.Info() == nil || it.rec.FilePriorities == "" {
		return
	}
	prios := it.priorities()
	if len(prios) == 0 {
		return
	}
	for _, f := range it.handle.Files() {
		if prio, ok := prios[f.DisplayPath()]; ok {
			f.SetPriority(priorityValue(prio))
		}
	}
}

// wantedFilesCompleteLocked reports whether every file the administrator did
// not skip is fully hashed on disk. Piece bytes that belong only to a skipped
// file must not count: otherwise a fast piece of the skipped file makes the
// torrent look finished and the files that were wanted never get written.
func (s *Service) wantedFilesCompleteLocked(it *item) bool {
	if it.handle == nil || it.handle.Info() == nil {
		return false
	}
	files := it.handle.Files()
	if len(files) == 0 {
		return it.handle.Complete().Bool()
	}
	prios := it.priorities()
	if len(prios) == 0 {
		return it.handle.Complete().Bool()
	}
	for _, f := range files {
		if prios[f.DisplayPath()] == PrioritySkip {
			continue
		}
		if hashedFileBytes(f) < f.Length() {
			return false
		}
	}
	// Every wanted file is hashed, or every file was skipped.
	return true
}

// hashedFileBytes counts only piece bytes that passed the hash check. Dirty
// chunks of an incomplete piece are not included, so a torrent is not declared
// finished before those bytes are flushed.
func hashedFileBytes(f *torrent.File) int64 {
	var n int64
	for _, st := range f.State() {
		if st.Complete {
			n += st.Bytes
		}
	}
	return n
}

// skippedBytesLocked is the total length of files the administrator chose to
// skip; the torrent counts as complete once everything else has arrived.
func (s *Service) skippedBytesLocked(it *item) int64 {
	if it.rec.FilePriorities == "" || it.handle == nil || it.handle.Info() == nil {
		return 0
	}
	prios := it.priorities()
	if len(prios) == 0 {
		return 0
	}
	var skipped int64
	for _, f := range it.handle.Files() {
		if prios[f.DisplayPath()] == PrioritySkip {
			skipped += f.Length()
		}
	}
	return skipped
}

func priorityValue(name string) torrent.PiecePriority {
	switch name {
	case PrioritySkip:
		return torrent.PiecePriorityNone
	case PriorityHigh:
		return torrent.PiecePriorityHigh
	default:
		return torrent.PiecePriorityNormal
	}
}

// removeContent deletes a torrent's files from the shared folder.
func (s *Service) removeContent(top string) {
	if top == "" {
		return
	}
	dir := s.space.Dir()
	for _, name := range []string{top, top + ".part"} {
		// top passed validateInfo: a single plain name, so this stays inside
		// the shared folder. RemoveAll does not follow symbolic links.
		_ = os.RemoveAll(filepath.Join(dir, name))
	}
	s.space.InvalidateUsage()
}

func (s *Service) saveLocked(it *item) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.store.Save(ctx, it.rec); err != nil {
		log.Printf("torrent %s: saving state failed: %v", shortHash(it.rec.InfoHash), err)
		it.dirty = true
		return
	}
	it.dirty = false
}

func shortHash(hash string) string {
	if len(hash) > 8 {
		return hash[:8]
	}
	return hash
}

// failLocked moves a torrent to the error state and takes it out of the
// client. The administrator can retry it with "resume".
func (s *Service) failLocked(it *item, message string) {
	if it.handle != nil {
		it.handle.Drop()
		it.handle = nil
	}
	if !it.admitted {
		// Nothing was downloaded under this name; let another torrent use it.
		s.guard.release(it.rec.InfoHash)
	}
	it.rec.State = StateError
	it.rec.Error = message
	it.stalled = false
	it.downRate, it.upRate, it.peers, it.seeders = 0, 0, 0, 0
	log.Printf("torrent %s: %s", shortHash(it.rec.InfoHash), message)
	s.saveLocked(it)
}

func (s *Service) completeLocked(it *item) {
	now := time.Now().UTC()
	skipped := s.skippedBytesLocked(it)
	it.rec.State = StateCompleted
	it.rec.Error = ""
	it.rec.CompletedAt = &now
	if it.handle != nil {
		it.rec.CompletedBytes = it.handle.BytesCompleted()
	} else {
		it.rec.CompletedBytes = it.rec.TotalBytes
	}
	it.stalled = false
	if !s.opts.SeedAfterComplete || it.rec.SeedStopped {
		s.dropHandleLocked(it, false)
	} else {
		it.seededFrom = time.Now()
	}
	log.Printf("torrent %s: download completed (%d bytes)", shortHash(it.rec.InfoHash), it.rec.CompletedBytes)
	s.saveLocked(it)
	s.space.InvalidateUsage()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	body := fmt.Sprintf("“%s” Ortak Paylaşım klasörüne indirildi.", it.rec.Name)
	if skipped > 0 {
		body = fmt.Sprintf("“%s” Ortak Paylaşım klasörüne indirildi (atlanan dosyalar hariç).", it.rec.Name)
	}
	if err := s.store.Notify(ctx, it.rec.AddedBy, notificationKind, "Torrent indirildi", body); err != nil {
		log.Printf("torrent %s: notification failed: %v", shortHash(it.rec.InfoHash), err)
	}
}

// accountUploaded folds the session's uploaded bytes into the persistent
// counter before a handle leaves the client.
func (s *Service) accountUploaded(it *item) {
	if it.handle == nil {
		return
	}
	stats := it.handle.Stats()
	up := stats.BytesWrittenData.Int64()
	if delta := up - it.lastUp; delta > 0 {
		it.rec.UploadedBytes += delta
	}
	it.lastUp = up
}

// dropHandleLocked removes the torrent from the client, keeping or releasing
// the claim on its folder and folding upload statistics into the record.
func (s *Service) dropHandleLocked(it *item, keepClaim bool) {
	s.accountUploaded(it)
	if it.handle != nil {
		if it.handle.Info() != nil {
			it.rec.CompletedBytes = it.handle.BytesCompleted()
		}
		it.handle.Drop()
		it.handle = nil
	}
	if !keepClaim && s.guard != nil {
		s.guard.release(it.rec.InfoHash)
	}
	it.seededFrom = time.Time{}
	it.stalled = false
	it.verifying = false
	it.downRate, it.upRate, it.peers, it.seeders = 0, 0, 0, 0
}

// stopSeedingLocked stops sharing a completed torrent (user action or a
// configured ratio/time limit) and remembers that it was stopped, so a server
// restart does not start it again.
func (s *Service) stopSeedingLocked(it *item, why string) {
	it.rec.SeededSeconds += int64(it.seededCarry)
	it.seededCarry = 0
	s.dropHandleLocked(it, false)
	it.rec.SeedStopped = true
	it.rec.State = StateCompleted
	log.Printf("torrent %s: seeding stopped (%s)", shortHash(it.rec.InfoHash), why)
	s.saveLocked(it)
	s.space.InvalidateUsage()
}

// seedLimitReached reports whether a seeding torrent hit its configured
// ratio or time limit.
func (s *Service) seedLimitReached(it *item) (bool, string) {
	if it.rec.SeedRatioLimit > 0 && it.rec.CompletedBytes > 0 &&
		float64(it.rec.UploadedBytes) >= it.rec.SeedRatioLimit*float64(it.rec.CompletedBytes) {
		return true, "seed ratio limit reached"
	}
	if it.rec.SeedTimeLimitSec > 0 && it.rec.SeededSeconds >= int64(it.rec.SeedTimeLimitSec) {
		return true, "seed time limit reached"
	}
	return false, ""
}

func (s *Service) loop() {
	defer s.wg.Done()
	ticker := time.NewTicker(s.opts.TickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.runCtx.Done():
			return
		case <-ticker.C:
			s.tick()
		}
	}
}

func (s *Service) tick() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ticks++
	now := time.Now()

	hardFull, checked := false, false
	if s.ticks%stallCheckEvery == 0 {
		if status, err := s.space.Quota(s.runCtx); err == nil {
			checked = true
			hardFull = (status.DiskTotalBytes > 0 && status.DiskFreeBytes <= diskReserve) ||
				(status.QuotaBytes > 0 && status.UsedBytes > status.QuotaBytes)
		}
	}

	for _, it := range s.items {
		if it.handle == nil {
			if it.dirty && s.ticks%persistEvery == 0 {
				s.saveLocked(it)
			}
			continue
		}
		if message := s.guard.takeFailure(it.rec.InfoHash); message != "" {
			s.failLocked(it, message)
			continue
		}
		stats := it.handle.Stats()
		down, up := stats.BytesReadUsefulData.Int64(), stats.BytesWrittenData.Int64()
		if delta := up - it.lastUp; delta > 0 {
			// Persistent per-torrent upload total (survives restarts).
			it.rec.UploadedBytes += delta
			it.dirty = true
		}
		if !it.sampledAt.IsZero() {
			if seconds := now.Sub(it.sampledAt).Seconds(); seconds > 0 {
				// Light smoothing keeps the displayed speed from flickering.
				it.downRate = 0.5*it.downRate + 0.5*float64(down-it.lastDown)/seconds
				it.upRate = 0.5*it.upRate + 0.5*float64(up-it.lastUp)/seconds
			}
		}
		it.lastDown, it.lastUp, it.sampledAt = down, up, now
		it.peers, it.seeders = stats.ActivePeers, stats.ConnectedSeeders

		if it.handle.Info() == nil {
			continue
		}
		if completed := it.handle.BytesCompleted(); completed != it.rec.CompletedBytes {
			it.rec.CompletedBytes = completed
			it.dirty = true
		}
		if it.admitted && it.rec.State != StateCompleted && !it.verifying && !it.needsVerify {
			// While a re-check is running the completion bitmap is stale: a
			// corrupt piece is still marked done until the hash fails. Declaring
			// the torrent finished here would drop it and skip the repair.
			if s.wantedFilesCompleteLocked(it) {
				s.completeLocked(it)
				continue
			}
		}
		if it.rec.State == StateCompleted && !it.seededFrom.IsZero() {
			it.seededCarry += now.Sub(it.seededFrom).Seconds()
			it.seededFrom = now
			if whole := int64(it.seededCarry); whole > 0 {
				it.rec.SeededSeconds += whole
				it.seededCarry -= float64(whole)
				it.dirty = true
			}
			if reached, why := s.seedLimitReached(it); reached {
				s.stopSeedingLocked(it, why)
				continue
			}
		}
		if checked && it.rec.State == StateActive {
			switch {
			case hardFull && !it.stalled:
				it.handle.DisallowDataDownload()
				it.stalled = true
				log.Printf("torrent %s: paused, shared space is full", shortHash(it.rec.InfoHash))
			case !hardFull && it.stalled:
				it.handle.AllowDataDownload()
				it.stalled = false
			}
		}
		if it.dirty && s.ticks%persistEvery == 0 {
			s.saveLocked(it)
		}
	}
}

func (s *Service) viewLocked(it *item) View {
	view := View{
		ID:                it.rec.ID,
		InfoHash:          it.rec.InfoHash,
		Name:              it.rec.Name,
		Path:              it.rec.ContentPath,
		TotalBytes:        it.rec.TotalBytes,
		CompletedBytes:    it.rec.CompletedBytes,
		AddedAt:           it.rec.AddedAt,
		CompletedAt:       it.rec.CompletedAt,
		Peers:             it.peers,
		Seeders:           it.seeders,
		DownloadRate:      int64(max(it.downRate, 0)),
		UploadRate:        int64(max(it.upRate, 0)),
		UploadedBytes:     it.rec.UploadedBytes,
		SeededSeconds:     it.rec.SeededSeconds,
		DownloadLimitKBps: it.rec.DownloadLimitKBps,
		UploadLimitKBps:   it.rec.UploadLimitKBps,
		SeedRatioLimit:    it.rec.SeedRatioLimit,
		SeedTimeLimitSec:  it.rec.SeedTimeLimitSec,
		ETASec:            -1,
	}
	if view.TotalBytes > 0 {
		view.Progress = min(1, float64(view.CompletedBytes)/float64(view.TotalBytes))
	}
	if view.CompletedBytes > 0 && it.rec.UploadedBytes > 0 {
		view.Ratio = float64(it.rec.UploadedBytes) / float64(view.CompletedBytes)
	}
	switch it.rec.State {
	case StateError:
		view.Status, view.Message = StatusError, it.rec.Error
	case StateCompleted:
		view.Status = StatusCompleted
		if it.rec.TotalBytes > 0 && it.rec.CompletedBytes >= it.rec.TotalBytes {
			view.Progress = 1
		}
		if it.handle != nil {
			view.Status = StatusSeeding
		}
	case StatePaused:
		view.Status = StatusPaused
	default:
		switch {
		case it.verifying:
			view.Status = StatusVerifying
		case it.handle == nil:
			view.Status, view.Message = StatusError, ErrUnavailable.Error()
		case !it.admitted:
			view.Status = StatusMetadata
		case it.stalled:
			view.Status, view.Message = StatusStalled, msgNoSpace
		default:
			view.Status = StatusDownloading
		}
	}
	if view.Status == StatusDownloading && view.DownloadRate > 0 && it.handle != nil && it.handle.Info() != nil {
		if missing := it.handle.BytesMissing(); missing > 0 {
			view.ETASec = int64(float64(missing) / float64(view.DownloadRate))
		} else {
			view.ETASec = 0
		}
	}
	if view.Status != StatusDownloading && view.Status != StatusSeeding && view.Status != StatusMetadata {
		view.DownloadRate, view.UploadRate = 0, 0
	}
	return view
}

// Overview returns the torrent list and settings. Admin only.
func (s *Service) Overview(ctx context.Context, actor Actor) (Overview, error) {
	if !actor.IsAdmin() {
		return Overview{}, ErrForbidden
	}
	out := Overview{
		Enabled:           s.opts.Enabled,
		SeedAfterComplete: s.opts.SeedAfterComplete,
		ListenPort:        s.opts.ListenPort,
	}
	if status, err := s.space.Quota(ctx); err == nil {
		out.Quota = &status
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out.Available = s.client != nil
	out.Reason = s.reason
	out.Limits = s.limits
	if s.client != nil {
		out.ListenPort = s.client.LocalPort()
	}
	out.Torrents = make([]View, 0, len(s.items))
	for _, it := range s.items {
		out.Torrents = append(out.Torrents, s.viewLocked(it))
	}
	sort.Slice(out.Torrents, func(i, j int) bool {
		a, b := out.Torrents[i], out.Torrents[j]
		if !a.AddedAt.Equal(b.AddedAt) {
			return a.AddedAt.After(b.AddedAt)
		}
		return a.ID < b.ID
	})
	return out, nil
}

// displayName cleans a magnet's "dn" for display until the real name arrives.
func displayName(raw, hash string) string {
	name := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(raw))
	if !utf8.ValidString(name) {
		name = ""
	}
	if runes := []rune(name); len(runes) > 200 {
		name = string(runes[:200])
	}
	if name == "" {
		name = "Magnet " + shortHash(hash)
	}
	return name
}

// AddMagnet queues a magnet link. Admin only.
func (s *Service) AddMagnet(ctx context.Context, actor Actor, uri string) (View, error) {
	if !actor.IsAdmin() {
		return View{}, ErrForbidden
	}
	uri = strings.TrimSpace(uri)
	if uri == "" || len(uri) > MaxMagnetBytes || !strings.HasPrefix(strings.ToLower(uri), "magnet:?") {
		return View{}, fmt.Errorf("%w: magnet bağlantısı bekleniyor", ErrInvalid)
	}
	spec, err := torrent.TorrentSpecFromMagnetUri(uri)
	if err != nil {
		// The parse error can echo parts of the link; do not pass it on.
		return View{}, fmt.Errorf("%w: magnet bağlantısı okunamadı", ErrInvalid)
	}
	var zero metainfo.Hash
	if spec.InfoHash == zero {
		return View{}, fmt.Errorf("%w: bağlantıda BitTorrent v1 özeti (btih) yok", ErrInvalid)
	}
	hash := spec.InfoHash.HexString()
	return s.add(ctx, actor, Record{
		InfoHash: hash,
		Name:     displayName(spec.DisplayName, hash),
		Magnet:   uri,
		State:    StateActive,
		AddedBy:  actor.ID,
	}, nil)
}

// AddFile queues an uploaded .torrent file. Admin only.
func (s *Service) AddFile(ctx context.Context, actor Actor, data []byte) (View, error) {
	if !actor.IsAdmin() {
		return View{}, ErrForbidden
	}
	if len(data) == 0 || len(data) > MaxMetainfoBytes {
		return View{}, fmt.Errorf("%w: .torrent dosyası boş veya çok büyük", ErrInvalid)
	}
	mi, err := metainfo.Load(bytes.NewReader(data))
	if err != nil {
		return View{}, fmt.Errorf("%w: .torrent dosyası okunamadı", ErrInvalid)
	}
	info, err := mi.UnmarshalInfo()
	if err != nil {
		return View{}, fmt.Errorf("%w: .torrent dosyası okunamadı", ErrInvalid)
	}
	if !info.HasV1() {
		return View{}, fmt.Errorf("%w: yalnızca BitTorrent v1 ve hibrit torrentler desteklenir", ErrInvalid)
	}
	top, total, err := validateInfo(&info)
	if err != nil {
		return View{}, err
	}
	return s.add(ctx, actor, Record{
		InfoHash:   mi.HashInfoBytes().HexString(),
		Name:       top,
		Metainfo:   data,
		State:      StateActive,
		TotalBytes: total,
		AddedBy:    actor.ID,
	}, &pending{top: top, total: total})
}

// pending is what is known up front about a torrent added from a file.
type pending struct {
	top   string
	total int64
}

func (s *Service) add(ctx context.Context, actor Actor, rec Record, known *pending) (View, error) {
	status, err := s.space.Quota(ctx)
	if err != nil {
		return View{}, err
	}
	if status.Level == sharedspace.QuotaFull {
		return View{}, ErrNoSpace
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client == nil {
		return View{}, ErrUnavailable
	}
	for _, it := range s.items {
		if it.rec.InfoHash == rec.InfoHash {
			return View{}, ErrDuplicate
		}
	}
	if len(s.items) >= s.opts.MaxTorrents {
		return View{}, ErrTooMany
	}
	// The same torrent removed without its data keeps a hidden row: adding it
	// again recognises the files, re-checks them and continues.
	if detached, ok, err := s.store.DetachedByInfoHash(ctx, rec.InfoHash); err != nil {
		return View{}, err
	} else if ok {
		return s.reviveLocked(ctx, actor, rec, known, detached)
	}
	if known != nil {
		if known.total > status.FreeBytes {
			return View{}, ErrNoSpace
		}
		for _, name := range []string{known.top, known.top + ".part"} {
			if _, err := os.Lstat(filepath.Join(s.space.Dir(), name)); err == nil || !errors.Is(err, os.ErrNotExist) {
				return View{}, ErrNameTaken
			}
		}
	}
	saved, err := s.store.Insert(ctx, rec)
	if err != nil {
		return View{}, err
	}
	it := &item{rec: saved}
	s.items[saved.ID] = it
	if err := s.attachLocked(it); err != nil {
		delete(s.items, saved.ID)
		s.guard.release(saved.InfoHash)
		_ = s.store.Delete(ctx, saved.ID)
		switch attachMessage(err) {
		case msgNameTaken:
			return View{}, ErrNameTaken
		case msgUnsafe:
			return View{}, ErrUnsafeTorrent
		default:
			return View{}, ErrUnavailable
		}
	}
	// Deliberately logs neither the magnet link nor the trackers.
	log.Printf("torrent %s: added by admin %s", shortHash(saved.InfoHash), actor.ID)
	return s.viewLocked(it), nil
}

// reviveLocked brings back a detached row when the same torrent is added
// again. Called with s.mu held; the record still points at the files that the
// earlier run downloaded, which are then verified piece by piece.
func (s *Service) reviveLocked(ctx context.Context, actor Actor, rec Record, known *pending, base Record) (View, error) {
	if base.ContentPath == "" {
		// Nothing ever arrived: treat it like a fresh add, name checks and all.
		if known != nil {
			if known.total > 0 {
				status, err := s.space.Quota(ctx)
				if err != nil {
					return View{}, err
				}
				if known.total > status.FreeBytes {
					return View{}, ErrNoSpace
				}
			}
			for _, name := range []string{known.top, known.top + ".part"} {
				if _, err := os.Lstat(filepath.Join(s.space.Dir(), name)); err == nil || !errors.Is(err, os.ErrNotExist) {
					return View{}, ErrNameTaken
				}
			}
		}
	}
	if rec.Magnet != "" {
		base.Magnet = rec.Magnet
	}
	if len(rec.Metainfo) > 0 {
		base.Metainfo = rec.Metainfo
	}
	if base.ContentPath == "" {
		base.Name = rec.Name
		base.TotalBytes = rec.TotalBytes
	}
	base.State = StateActive
	base.Error = ""
	base.DetachedAt = nil
	base.CompletedAt = nil
	base.SeedStopped = false
	base.AddedBy = actor.ID
	base.AddedAt = time.Now().UTC()
	if base.ContentPath != "" {
		if !s.guard.restore(base.InfoHash, base.ContentPath) {
			// Another torrent claimed the folder meanwhile.
			return View{}, ErrNameTaken
		}
	}
	it := &item{rec: base, admitted: base.ContentPath != "", needsVerify: true, showVerify: base.ContentPath != ""}
	if base.ContentPath != "" && len(base.Metainfo) > 0 {
		s.quarantineBadPieces(base.Metainfo)
	}
	s.items[base.ID] = it
	if err := s.store.Save(ctx, base); err != nil {
		delete(s.items, base.ID)
		return View{}, err
	}
	if err := s.attachLocked(it); err != nil {
		s.failLocked(it, attachMessage(err))
		if !it.admitted {
			delete(s.items, base.ID)
			_ = s.store.Delete(ctx, base.ID)
		}
		switch attachMessage(err) {
		case msgNameTaken:
			return View{}, ErrNameTaken
		case msgUnsafe:
			return View{}, ErrUnsafeTorrent
		default:
			return View{}, ErrUnavailable
		}
	}
	log.Printf("torrent %s: re-added by admin %s (existing data will be verified)", shortHash(base.InfoHash), actor.ID)
	return s.viewLocked(it), nil
}

// Pause stops a download without removing it. Admin only.
func (s *Service) Pause(ctx context.Context, actor Actor, id string) (View, error) {
	if !actor.IsAdmin() {
		return View{}, ErrForbidden
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	it := s.items[id]
	if it == nil {
		return View{}, ErrNotFound
	}
	if it.rec.State != StateActive {
		return View{}, ErrInvalidState
	}
	// Pausing takes the torrent out of the client altogether: connections
	// close and nothing more is read or written. Merely refusing new
	// requests would let data already in flight keep arriving. The claim
	// on the folder is kept, so the partial data stays reserved.
	it.rec.State = StatePaused
	s.dropHandleLocked(it, true)
	s.saveLocked(it)
	s.space.InvalidateUsage()
	return s.viewLocked(it), nil
}

// Resume continues a paused download, retries a failed one, or restarts the
// seeding of a completed torrent. Admin only.
func (s *Service) Resume(ctx context.Context, actor Actor, id string) (View, error) {
	if !actor.IsAdmin() {
		return View{}, ErrForbidden
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	it := s.items[id]
	if it == nil {
		return View{}, ErrNotFound
	}
	if s.client == nil {
		return View{}, ErrUnavailable
	}
	switch it.rec.State {
	case StateCompleted:
		// Explicitly (re)starting the seed: the counters start over so an
		// old ratio/time limit does not stop the torrent again at once. The
		// state stays "completed" — the view reports "seeding" as soon as
		// the handle is back, and no completion notice fires again.
		if it.handle != nil {
			return View{}, ErrInvalidState
		}
		it.rec.SeedStopped = false
		it.rec.SeededSeconds = 0
		it.seededCarry = 0
		it.rec.UploadedBytes = 0
		it.rec.Error = ""
		if it.rec.ContentPath != "" {
			s.guard.restore(it.rec.InfoHash, it.rec.ContentPath)
		}
		// Completed data does not need a re-check here: every file that
		// exists is complete, and skipped files stay skipped.
		it.needsVerify = false
		if err := s.attachLocked(it); err != nil {
			s.failLocked(it, attachMessage(err))
			return s.viewLocked(it), nil
		}
		s.saveLocked(it)
		return s.viewLocked(it), nil
	case StatePaused, StateError:
	default:
		return View{}, ErrInvalidState
	}
	it.rec.State = StateActive
	it.rec.Error = ""
	if it.handle == nil {
		if it.rec.ContentPath != "" {
			s.guard.restore(it.rec.InfoHash, it.rec.ContentPath)
		}
		// Re-check partial data before continuing. A torrent that never
		// received a byte has nothing to hash and goes straight back to
		// downloading.
		it.needsVerify = it.rec.ContentPath != "" && it.rec.CompletedBytes > 0
		it.showVerify = false
		if err := s.attachLocked(it); err != nil {
			s.failLocked(it, attachMessage(err))
			return s.viewLocked(it), nil
		}
	}
	s.saveLocked(it)
	return s.viewLocked(it), nil
}

// StopSeeding stops sharing a completed torrent without touching the files.
// The torrent stays "Tamamlandı" and can be started again with Resume.
// Admin only.
func (s *Service) StopSeeding(ctx context.Context, actor Actor, id string) (View, error) {
	if !actor.IsAdmin() {
		return View{}, ErrForbidden
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	it := s.items[id]
	if it == nil {
		return View{}, ErrNotFound
	}
	if it.rec.State != StateCompleted || it.handle == nil {
		return View{}, ErrInvalidState
	}
	s.stopSeedingLocked(it, "stopped by administrator")
	return s.viewLocked(it), nil
}

// ItemLimits are the per-torrent speed and seed limits; 0 disables a limit.
type ItemLimits struct {
	DownloadLimitKBps int64   `json:"downloadLimitKBps"`
	UploadLimitKBps   int64   `json:"uploadLimitKBps"`
	SeedRatioLimit    float64 `json:"seedRatioLimit"`
	SeedTimeLimitSec  int     `json:"seedTimeLimitSec"`
}

// SetItemLimits changes one torrent's speed and seed limits. Admin only.
func (s *Service) SetItemLimits(ctx context.Context, actor Actor, id string, limits ItemLimits) (View, error) {
	if !actor.IsAdmin() {
		return View{}, ErrForbidden
	}
	if limits.DownloadLimitKBps < 0 || limits.DownloadLimitKBps > MaxLimitKBps ||
		limits.UploadLimitKBps < 0 || limits.UploadLimitKBps > MaxLimitKBps ||
		limits.SeedRatioLimit < 0 || limits.SeedRatioLimit > MaxSeedRatioLimit ||
		limits.SeedTimeLimitSec < 0 || limits.SeedTimeLimitSec > MaxSeedTimeLimitSec {
		return View{}, ErrInvalidLimit
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	it := s.items[id]
	if it == nil {
		return View{}, ErrNotFound
	}
	it.rec.DownloadLimitKBps = limits.DownloadLimitKBps
	it.rec.UploadLimitKBps = limits.UploadLimitKBps
	it.rec.SeedRatioLimit = limits.SeedRatioLimit
	it.rec.SeedTimeLimitSec = limits.SeedTimeLimitSec
	s.guard.setItemLimits(it.rec.InfoHash, limits.DownloadLimitKBps, limits.UploadLimitKBps)
	s.saveLocked(it)
	return s.viewLocked(it), nil
}

// SetFilePriority chooses how one file inside a torrent is downloaded:
// "skip" leaves it out, "normal" is the default, "high" downloads it first.
// The choice is stored and re-applied every time the torrent is attached.
// Admin only.
func (s *Service) SetFilePriority(ctx context.Context, actor Actor, id, path, priority string) (View, error) {
	if !actor.IsAdmin() {
		return View{}, ErrForbidden
	}
	switch priority {
	case PrioritySkip, PriorityNormal, PriorityHigh:
	default:
		return View{}, fmt.Errorf("%w: geçersiz dosya önceliği", ErrInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	it := s.items[id]
	if it == nil {
		return View{}, ErrNotFound
	}
	prios := it.priorities()
	if priority == PriorityNormal {
		delete(prios, path)
	} else {
		prios[path] = priority
	}
	if len(prios) == 0 {
		it.rec.FilePriorities = ""
	} else {
		encoded, err := json.Marshal(prios)
		if err != nil || len(encoded) > 64<<10 {
			return View{}, fmt.Errorf("%w: çok fazla dosya tercihi", ErrInvalid)
		}
		it.rec.FilePriorities = string(encoded)
	}
	it.prios, it.priosRaw = prios, it.rec.FilePriorities
	if it.handle != nil && it.handle.Info() != nil {
		found := false
		for _, f := range it.handle.Files() {
			if f.DisplayPath() == path {
				found = true
				f.SetPriority(priorityValue(priority))
			}
		}
		if !found {
			return View{}, ErrFileNotFound
		}
	}
	s.saveLocked(it)
	return s.viewLocked(it), nil
}

// Detail returns the drawer's data: files, sanitised trackers and peers.
// Admin only.
func (s *Service) Detail(ctx context.Context, actor Actor, id string) (TorrentDetail, error) {
	if !actor.IsAdmin() {
		return TorrentDetail{}, ErrForbidden
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	it := s.items[id]
	if it == nil {
		return TorrentDetail{}, ErrNotFound
	}
	detail := TorrentDetail{View: s.viewLocked(it), Files: []FileDetail{}, Trackers: []TrackerDetail{}, Peers: []PeerDetail{}}
	if it.handle == nil {
		// A paused/stopped/completed torrent still knows its files and
		// trackers from the stored metainfo; peers and live completion do
		// not exist without a handle.
		var mi *metainfo.MetaInfo
		if len(it.rec.Metainfo) > 0 {
			if loaded, err := metainfo.Load(bytes.NewReader(it.rec.Metainfo)); err == nil {
				mi = loaded
				if info, err := loaded.UnmarshalInfo(); err == nil {
					detail.Files = filesFromInfo(&info, it.priorities())
				}
			}
		}
		detail.Trackers = trackerDetails(it.rec.Magnet, mi)
		return detail, nil
	}
	if info := it.handle.Info(); info != nil {
		prios := it.priorities()
		for _, f := range it.handle.Files() {
			detail.Files = append(detail.Files, FileDetail{
				Path:      f.DisplayPath(),
				Length:    f.Length(),
				Completed: f.BytesCompleted(),
				Priority:  normalizePriority(f.Priority(), prios[f.DisplayPath()]),
			})
		}
		mi := it.handle.Metainfo()
		detail.Trackers = trackerDetails(it.rec.Magnet, &mi)
	}
	for _, conn := range it.handle.PeerConns() {
		addr := ""
		if conn.RemoteAddr != nil {
			addr = conn.RemoteAddr.String()
		}
		clientName := ""
		if loaded, ok := conn.PeerClientName.Load().(string); ok {
			clientName = loaded
		}
		detail.Peers = append(detail.Peers, PeerDetail{Addr: addr, Client: clientName})
	}
	return detail, nil
}

// filesFromInfo lists files of a metainfo without a live handle (paused,
// stopped or completed). The paths match File.DisplayPath so the priority
// endpoint accepts both, and the stored administrator choices are applied.
func filesFromInfo(info *metainfo.Info, prios map[string]string) []FileDetail {
	var out []FileDetail
	for _, fi := range info.UpvertedFiles() {
		path := fi.DisplayPath(info)
		priority := PriorityNormal
		if stored, ok := prios[path]; ok && stored != "" {
			priority = stored
		}
		out = append(out, FileDetail{Path: path, Length: fi.Length, Priority: priority})
	}
	return out
}

// normalizePriority prefers the stored administrator choice over the live
// library value, which only distinguishes none/normal/high anyway.
func normalizePriority(live torrent.PiecePriority, stored string) string {
	switch {
	case stored == PrioritySkip, live == torrent.PiecePriorityNone:
		return PrioritySkip
	case stored == PriorityHigh, live == torrent.PiecePriorityHigh:
		return PriorityHigh
	default:
		return PriorityNormal
	}
}

// trackerDetails sanitises tracker addresses: scheme and host only, because
// the path and query of a private tracker hold the user's passkey.
func trackerDetails(magnet string, mi *metainfo.MetaInfo) []TrackerDetail {
	raw := map[string]bool{}
	add := func(tr string) {
		if tr == "" {
			return
		}
		parsed, err := url.Parse(tr)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" && parsed.Scheme != "udp" &&
			parsed.Scheme != "udp4" && parsed.Scheme != "udp6" {
			return
		}
		raw[parsed.Scheme+"://"+parsed.Host] = true
	}
	if mi != nil {
		for _, tracker := range mi.UpvertedAnnounceList().DistinctValues() {
			add(tracker)
		}
		if len(raw) == 0 {
			add(mi.Announce)
		}
	}
	if len(raw) == 0 && magnet != "" {
		if parsed, err := url.Parse(magnet); err == nil {
			for _, tr := range parsed.Query()["tr"] {
				add(tr)
			}
		}
	}
	out := make([]TrackerDetail, 0, len(raw))
	for tracker := range raw {
		out = append(out, TrackerDetail{URL: tracker})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].URL < out[j].URL })
	return out
}

// Remove takes a torrent off the list and optionally deletes what it
// downloaded. Admin only.
func (s *Service) Remove(ctx context.Context, actor Actor, id string, deleteData bool) error {
	if !actor.IsAdmin() {
		return ErrForbidden
	}
	s.mu.Lock()
	it := s.items[id]
	if it == nil {
		s.mu.Unlock()
		return ErrNotFound
	}
	content := it.rec.ContentPath
	hash := it.rec.InfoHash
	// Without "delete data" the row is detached instead of deleted, so that
	// re-adding the same torrent verifies the files and continues.
	var err error
	if deleteData {
		err = s.store.Delete(ctx, id)
	} else {
		err = s.store.Detach(ctx, id)
	}
	if err != nil {
		s.mu.Unlock()
		return err
	}
	s.dropHandleLocked(it, false)
	delete(s.items, id)
	s.mu.Unlock()
	log.Printf("torrent %s: removed by admin %s (delete data: %t)", shortHash(hash), actor.ID, deleteData)
	if deleteData && content != "" {
		// Goes through the shared space so grants and public links pointing at
		// the removed folder are cleaned up as well. Called without s.mu held:
		// the shared space calls back into PathChanged.
		for _, name := range []string{content, content + ".part"} {
			if err := s.space.Delete(ctx, actor, name); err != nil && !errors.Is(err, sharedspace.ErrNotFound) {
				return fmt.Errorf("torrent kaldırıldı ancak dosyalar silinemedi: %w", err)
			}
		}
	}
	s.space.InvalidateUsage()
	return nil
}

// SetLimits changes the global speed limits; they apply immediately and are
// kept across restarts. Admin only.
func (s *Service) SetLimits(ctx context.Context, actor Actor, limits Limits) (Limits, error) {
	if !actor.IsAdmin() {
		return Limits{}, ErrForbidden
	}
	if limits.DownloadLimitKBps < 0 || limits.DownloadLimitKBps > MaxLimitKBps ||
		limits.UploadLimitKBps < 0 || limits.UploadLimitKBps > MaxLimitKBps {
		return Limits{}, ErrInvalidLimit
	}
	if err := s.store.SetSetting(ctx, settingDownloadLimit, strconv.FormatInt(limits.DownloadLimitKBps, 10)); err != nil {
		return Limits{}, err
	}
	if err := s.store.SetSetting(ctx, settingUploadLimit, strconv.FormatInt(limits.UploadLimitKBps, 10)); err != nil {
		return Limits{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.limits = limits
	s.applyLimitsLocked()
	return s.limits, nil
}
