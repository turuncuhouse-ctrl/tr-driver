package torrents

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
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
)

const (
	// MaxMetainfoBytes bounds an uploaded .torrent file.
	MaxMetainfoBytes = 8 << 20
	// MaxMagnetBytes bounds a magnet link.
	MaxMagnetBytes = 16 << 10
	// MaxLimitKBps is the highest accepted speed limit (about 10 GiB/s).
	MaxLimitKBps = 10 * 1024 * 1024

	defaultMaxTorrents = 200
	defaultTick        = 2 * time.Second
	stallCheckEvery    = 5 // ticks
	persistEvery       = 8 // ticks

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
	// Configure adjusts the client configuration last. It exists for tests.
	Configure func(*torrent.ClientConfig)
}

// Limits are the global speed limits in KiB/s; 0 means unlimited.
type Limits struct {
	DownloadLimitKBps int64 `json:"downloadLimitKBps"`
	UploadLimitKBps   int64 `json:"uploadLimitKBps"`
}

// View is a torrent as shown to the administrator. It deliberately has no
// magnet link, tracker list or metainfo.
type View struct {
	ID             string     `json:"id"`
	InfoHash       string     `json:"infoHash"`
	Name           string     `json:"name"`
	Status         string     `json:"status"`
	Message        string     `json:"message,omitempty"`
	Path           string     `json:"path,omitempty"`
	TotalBytes     int64      `json:"totalBytes"`
	CompletedBytes int64      `json:"completedBytes"`
	Progress       float64    `json:"progress"`
	DownloadRate   int64      `json:"downloadRate"`
	UploadRate     int64      `json:"uploadRate"`
	Peers          int        `json:"peers"`
	Seeders        int        `json:"seeders"`
	AddedAt        time.Time  `json:"addedAt"`
	CompletedAt    *time.Time `json:"completedAt,omitempty"`
}

// Statuses reported in View.Status.
const (
	StatusMetadata    = "metadata"
	StatusDownloading = "downloading"
	StatusStalled     = "stalled"
	StatusPaused      = "paused"
	StatusCompleted   = "completed"
	StatusSeeding     = "seeding"
	StatusError       = "error"
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

	lastDown, lastUp int64
	sampledAt        time.Time
	downRate, upRate float64
	peers, seeders   int
}

// Service owns the embedded BitTorrent client.
type Service struct {
	opts  Options
	store Store
	space Space

	client      *torrent.Client
	guard       *guardStorage
	fileStorage storage.ClientImplCloser
	dl, ul      *rate.Limiter
	reason      string // why the client is unavailable, if it is

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
	files := storage.NewFileOpts(storage.NewFileClientOpts{
		ClientBaseDir: dir,
		// Completion is derived from ".part" file names and kept in memory, so
		// no database file is written into the shared folder.
		PieceCompletion: storage.NewMapPieceCompletion(),
		Logger:          discard,
	})
	guard := newGuardStorage(dir, files)
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
	s.client, s.guard, s.fileStorage, s.dl, s.ul = client, guard, files, dl, ul
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
			s.guard.restore(rec.InfoHash, rec.ContentPath)
			continue
		}
		resume := rec.State == StateActive ||
			(rec.State == StateCompleted && s.opts.SeedAfterComplete && rec.ContentPath != "")
		if !resume {
			continue
		}
		s.guard.restore(rec.InfoHash, rec.ContentPath)
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
	it.handle = handle
	it.stalled = false
	it.sampledAt = time.Time{}
	it.downRate, it.upRate = 0, 0
	s.wg.Add(1)
	go s.awaitInfo(it.rec.ID, handle)
	return nil
}

func (s *Service) awaitInfo(id string, handle *torrent.Torrent) {
	defer s.wg.Done()
	select {
	case <-handle.GotInfo():
	case <-handle.Closed():
		return
	case <-s.runCtx.Done():
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	it := s.items[id]
	if it == nil || it.handle != handle {
		return
	}
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
	s.saveLocked(it)
	s.space.InvalidateUsage()
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
	it.rec.State = StateCompleted
	it.rec.Error = ""
	it.rec.CompletedAt = &now
	it.rec.CompletedBytes = it.rec.TotalBytes
	it.stalled = false
	if !s.opts.SeedAfterComplete {
		it.handle.Drop()
		it.handle = nil
		s.guard.release(it.rec.InfoHash)
		it.downRate, it.upRate, it.peers, it.seeders = 0, 0, 0, 0
	}
	log.Printf("torrent %s: download completed (%d bytes)", shortHash(it.rec.InfoHash), it.rec.TotalBytes)
	s.saveLocked(it)
	s.space.InvalidateUsage()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.store.Notify(ctx, it.rec.AddedBy, notificationKind, "Torrent indirildi",
		fmt.Sprintf("“%s” Ortak Paylaşım klasörüne indirildi.", it.rec.Name)); err != nil {
		log.Printf("torrent %s: notification failed: %v", shortHash(it.rec.InfoHash), err)
	}
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
		if it.admitted && it.rec.State != StateCompleted && it.handle.Complete().Bool() {
			s.completeLocked(it)
			continue
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
		ID:             it.rec.ID,
		InfoHash:       it.rec.InfoHash,
		Name:           it.rec.Name,
		Path:           it.rec.ContentPath,
		TotalBytes:     it.rec.TotalBytes,
		CompletedBytes: it.rec.CompletedBytes,
		AddedAt:        it.rec.AddedAt,
		CompletedAt:    it.rec.CompletedAt,
		Peers:          it.peers,
		Seeders:        it.seeders,
		DownloadRate:   int64(max(it.downRate, 0)),
		UploadRate:     int64(max(it.upRate, 0)),
	}
	if view.TotalBytes > 0 {
		view.Progress = min(1, float64(view.CompletedBytes)/float64(view.TotalBytes))
	}
	switch it.rec.State {
	case StateError:
		view.Status, view.Message = StatusError, it.rec.Error
	case StateCompleted:
		view.Status, view.Progress = StatusCompleted, 1
		if it.handle != nil {
			view.Status = StatusSeeding
		}
	case StatePaused:
		view.Status = StatusPaused
	default:
		switch {
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
	it.rec.State = StatePaused
	it.stalled = false
	if it.handle != nil {
		// Pausing takes the torrent out of the client altogether: connections
		// close and nothing more is read or written. Merely refusing new
		// requests would let data already in flight keep arriving. The claim
		// on the folder is kept, so the partial data stays reserved.
		if it.handle.Info() != nil {
			it.rec.CompletedBytes = it.handle.BytesCompleted()
		}
		it.handle.Drop()
		it.handle = nil
	}
	it.downRate, it.upRate, it.peers, it.seeders = 0, 0, 0, 0
	s.saveLocked(it)
	s.space.InvalidateUsage()
	return s.viewLocked(it), nil
}

// Resume continues a paused download or retries a failed one. Admin only.
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
	if it.rec.State != StatePaused && it.rec.State != StateError {
		return View{}, ErrInvalidState
	}
	if s.client == nil {
		return View{}, ErrUnavailable
	}
	it.rec.State = StateActive
	it.rec.Error = ""
	if it.handle == nil {
		s.guard.restore(it.rec.InfoHash, it.rec.ContentPath)
		if err := s.attachLocked(it); err != nil {
			s.failLocked(it, attachMessage(err))
			return s.viewLocked(it), nil
		}
	}
	s.saveLocked(it)
	return s.viewLocked(it), nil
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
	if it.handle != nil {
		it.handle.Drop()
		it.handle = nil
	}
	if s.guard != nil {
		s.guard.release(it.rec.InfoHash)
	}
	delete(s.items, id)
	content := it.rec.ContentPath
	hash := it.rec.InfoHash
	err := s.store.Delete(ctx, id)
	s.mu.Unlock()
	if err != nil {
		return err
	}
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
