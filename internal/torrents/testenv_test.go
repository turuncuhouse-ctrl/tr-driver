package torrents

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"

	"necipdrive/internal/sharedspace"
)

var (
	admin   = Actor{ID: "admin-1", Role: "admin"}
	regular = Actor{ID: "user-1", Role: "user"}
)

// memStore is an in-memory Store; the SQL one is covered by internal/integration.
type memStore struct {
	mu       sync.Mutex
	seq      int
	records  map[string]Record
	settings map[string]string
	notices  []string
}

func newMemStore() *memStore {
	return &memStore{records: map[string]Record{}, settings: map[string]string{}}
}

func (m *memStore) List(context.Context) ([]Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Record, 0, len(m.records))
	for _, rec := range m.records {
		out = append(out, rec)
	}
	return out, nil
}

func (m *memStore) Insert(_ context.Context, rec Record) (Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.records {
		if existing.InfoHash == rec.InfoHash {
			return Record{}, ErrDuplicate
		}
	}
	m.seq++
	rec.ID = fmt.Sprintf("torrent-%d", m.seq)
	rec.AddedAt = time.Now()
	m.records[rec.ID] = rec
	return rec, nil
}

func (m *memStore) Save(_ context.Context, rec Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.records[rec.ID]; ok {
		m.records[rec.ID] = rec
	}
	return nil
}

func (m *memStore) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.records, id)
	return nil
}

func (m *memStore) Setting(_ context.Context, key string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	value, ok := m.settings[key]
	return value, ok, nil
}

func (m *memStore) SetSetting(_ context.Context, key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.settings[key] = value
	return nil
}

func (m *memStore) Notify(_ context.Context, userID, _, title, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.notices = append(m.notices, userID+": "+title+" "+body)
	return nil
}

func (m *memStore) record(id string) Record {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.records[id]
}

func (m *memStore) noticeCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.notices)
}

// fakeSpace stands in for the shared space: a directory and a free-space figure.
type fakeSpace struct {
	dir string

	mu       sync.Mutex
	free     int64
	hardFull bool
	deleted  []string
}

func (f *fakeSpace) Dir() string { return f.dir }

func (f *fakeSpace) Quota(context.Context) (sharedspace.QuotaStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	status := sharedspace.QuotaStatus{FreeBytes: f.free, Level: sharedspace.QuotaOK, DiskTotalBytes: 1 << 40, DiskFreeBytes: 1 << 39}
	if f.free <= 0 {
		status.Level = sharedspace.QuotaFull
	}
	if f.hardFull {
		status.Level = sharedspace.QuotaFull
		status.DiskFreeBytes = 0
	}
	return status, nil
}

func (f *fakeSpace) InvalidateUsage() {}

func (f *fakeSpace) Delete(_ context.Context, actor sharedspace.Actor, path string) error {
	if !actor.IsAdmin() {
		return sharedspace.ErrForbidden
	}
	rel, err := sharedspace.CleanRel(path)
	if err != nil || rel == "" {
		return sharedspace.ErrInvalidPath
	}
	full := filepath.Join(f.dir, filepath.FromSlash(rel))
	if _, err := os.Lstat(full); err != nil {
		return sharedspace.ErrNotFound
	}
	f.mu.Lock()
	f.deleted = append(f.deleted, rel)
	f.mu.Unlock()
	return os.RemoveAll(full)
}

func (f *fakeSpace) setFree(n int64) {
	f.mu.Lock()
	f.free = n
	f.mu.Unlock()
}

func (f *fakeSpace) setHardFull(full bool) {
	f.mu.Lock()
	f.hardFull = full
	f.mu.Unlock()
}

type testEnv struct {
	t       *testing.T
	root    string
	space   *fakeSpace
	store   *memStore
	service *Service
	// sources remembers which seeder serves which torrent, so a dropped
	// connection can be re-established while a test waits.
	sources map[string]*seeder
}

// loopback keeps a test client off the network: no DHT, no trackers, and only
// the loopback interface.
func loopback(cfg *torrent.ClientConfig) {
	cfg.NoDHT = true
	cfg.DisableTrackers = true
	cfg.DisablePEX = true
	cfg.DisableIPv6 = true
	cfg.ListenPort = 0
	cfg.ListenHost = func(string) string { return "127.0.0.1" }
	cfg.NoDefaultPortForwarding = true
}

func newTestEnv(t *testing.T, mutate ...func(*Options)) *testEnv {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "paylasim")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	env := &testEnv{t: t, root: root, space: &fakeSpace{dir: dir, free: 1 << 30}, store: newMemStore()}
	env.service = env.start(mutate...)
	return env
}

func (e *testEnv) start(mutate ...func(*Options)) *Service {
	e.t.Helper()
	opts := Options{Enabled: true, TickInterval: 40 * time.Millisecond, Configure: loopback}
	for _, m := range mutate {
		m(&opts)
	}
	service := New(e.store, e.space, opts)
	if opts.Enabled && !service.Available() {
		e.t.Fatalf("torrent client did not start: %s", service.reason)
	}
	if err := service.Start(context.Background()); err != nil {
		e.t.Fatalf("Start: %v", err)
	}
	e.t.Cleanup(service.Close)
	return service
}

// seeder is an ordinary BitTorrent client holding complete content.
type seeder struct {
	client *torrent.Client
	dir    string
}

func newSeeder(t *testing.T) *seeder {
	t.Helper()
	dir := t.TempDir()
	cfg := torrent.NewDefaultClientConfig()
	loopback(cfg)
	cfg.Seed = true
	cfg.DataDir = dir
	cfg.Slogger = slog.New(slog.DiscardHandler)
	cfg.DefaultStorage = storage.NewFileOpts(storage.NewFileClientOpts{ClientBaseDir: dir, Logger: slog.New(slog.DiscardHandler)})
	client, err := torrent.NewClient(cfg)
	if err != nil {
		t.Fatalf("seeder: %v", err)
	}
	t.Cleanup(func() { client.Close() })
	return &seeder{client: client, dir: dir}
}

// share writes files under name in the seeder's directory, starts seeding
// them and returns the .torrent bytes and the info hash.
func (s *seeder) share(t *testing.T, name string, files map[string][]byte) ([]byte, string) {
	t.Helper()
	root := filepath.Join(s.dir, name)
	for rel, content := range files {
		full := root
		if rel != "" {
			full = filepath.Join(root, filepath.FromSlash(rel))
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	info := metainfo.Info{PieceLength: 32 << 10}
	if err := info.BuildFromFilePath(root); err != nil {
		t.Fatal(err)
	}
	mi := &metainfo.MetaInfo{InfoBytes: bencode.MustMarshal(info)}
	// A tracker with a passkey, as private trackers issue: it must never
	// appear in logs or API responses.
	mi.Announce = "https://tracker.example.com/announce?passkey=" + trackerPasskey
	handle, err := s.client.AddTorrent(mi)
	if err != nil {
		t.Fatal(err)
	}
	<-handle.GotInfo()
	if err := handle.VerifyData(); err != nil {
		t.Fatalf("seeder verify: %v", err)
	}
	var buf bytes.Buffer
	if err := mi.Write(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), mi.HashInfoBytes().HexString()
}

const trackerPasskey = "PASSKEY-9f3a71c2-do-not-leak"

func randomBytes(t *testing.T, n int) []byte {
	t.Helper()
	data := make([]byte, n)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	return data
}

// connect introduces the seeder to the service's torrent, standing in for the
// tracker/DHT discovery that tests keep switched off.
func (e *testEnv) connect(id string, s *seeder) {
	e.t.Helper()
	if e.sources == nil {
		e.sources = map[string]*seeder{}
	}
	e.sources[id] = s
	if !e.introduce(id) {
		e.t.Fatalf("torrent %s is not loaded in the client", id)
	}
}

func (e *testEnv) introduce(id string) bool {
	source := e.sources[id]
	e.service.mu.Lock()
	defer e.service.mu.Unlock()
	it := e.service.items[id]
	if source == nil || it == nil || it.handle == nil {
		return false
	}
	it.handle.AddClientPeer(source.client)
	return true
}

func (e *testEnv) view(id string) (View, bool) {
	e.t.Helper()
	overview, err := e.service.Overview(context.Background(), admin)
	if err != nil {
		e.t.Fatalf("Overview: %v", err)
	}
	for _, view := range overview.Torrents {
		if view.ID == id {
			return view, true
		}
	}
	return View{}, false
}

// waitStatus polls until the torrent reaches one of the wanted statuses.
func (e *testEnv) waitStatus(id string, timeout time.Duration, want ...string) View {
	e.t.Helper()
	deadline := time.Now().Add(timeout)
	nextIntro := time.Now().Add(2 * time.Second)
	var last View
	for time.Now().Before(deadline) {
		if time.Now().After(nextIntro) {
			// Without trackers nothing else would retry a failed first dial.
			e.introduce(id)
			nextIntro = time.Now().Add(2 * time.Second)
		}
		view, ok := e.view(id)
		if ok {
			last = view
			for _, status := range want {
				if view.Status == status {
					return view
				}
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	e.t.Fatalf("torrent %s did not reach %v within %v; last: %+v", id, want, timeout, last)
	return View{}
}

func (e *testEnv) sharedFile(rel string) ([]byte, error) {
	return os.ReadFile(filepath.Join(e.space.dir, filepath.FromSlash(rel)))
}

func (e *testEnv) sharedEntries() []string {
	e.t.Helper()
	var out []string
	err := filepath.WalkDir(e.space.dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if rel, _ := filepath.Rel(e.space.dir, path); rel != "." {
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return out
}

func isErr(err, target error) bool { return errors.Is(err, target) }

func magnetFor(hash, name string) string {
	return "magnet:?xt=urn:btih:" + hash + "&dn=" + strings.ReplaceAll(name, " ", "+") +
		"&tr=" + "https%3A%2F%2Ftracker.example.com%2Fannounce%3Fpasskey%3D" + trackerPasskey
}
