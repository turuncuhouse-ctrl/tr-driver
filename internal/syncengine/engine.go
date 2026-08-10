// Package syncengine coordinates local watching, durable jobs, and remote changes.
package syncengine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"necipdrive/internal/syncclient"
	"necipdrive/internal/syncstore"
	"necipdrive/internal/watcher"
)

type API interface {
	Changes(context.Context, int64, int) (syncclient.ChangesResponse, error)
	UploadFile(context.Context, string, string, syncclient.Manifest) (syncclient.Entry, error)
	Trash(context.Context, string) error
	Download(context.Context, string, string, bool) error
}
type Config struct {
	ServerURL, Email, Token, DeviceName string
	PollInterval                        time.Duration
}
type Status struct {
	State       string `json:"State"`
	LastError   string `json:"LastError"`
	CurrentFile string `json:"CurrentFile"`
	BytesSynced int64  `json:"BytesSynced"`
	BytesTotal  int64  `json:"BytesTotal"`
	BytesDone   int64  `json:"BytesDone"`
	PendingJobs int64  `json:"PendingJobs"`
	Percent     int    `json:"Percent"`
	Message     string `json:"Message"`
}
type Event struct {
	Type     string
	Status   Status
	Activity *syncstore.Activity
}
type Engine struct {
	store      *syncstore.Store
	api        API
	cfg        Config
	mu         sync.RWMutex
	status     Status
	watchers   map[string]*watcher.Watcher
	rootCtx    context.Context
	rootCancel context.CancelFunc
	pausedAll  bool
	listeners  []chan Event
	listenMu   sync.Mutex
}

func New(store *syncstore.Store, api API, cfg Config) *Engine {
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 30 * time.Second
	}
	return &Engine{store: store, api: api, cfg: cfg, watchers: map[string]*watcher.Watcher{}}
}
func (e *Engine) Status() Status             { e.mu.RLock(); defer e.mu.RUnlock(); return e.status }
func (e *Engine) setStatus(fn func(*Status)) {
	e.mu.Lock()
	fn(&e.status)
	status := e.status
	e.mu.Unlock()
	e.emit(Event{Type: "status", Status: status})
}
func (e *Engine) setProgress(fn func(*Status)) {
	e.mu.Lock()
	fn(&e.status)
	status := e.status
	e.mu.Unlock()
	e.emit(Event{Type: "progress", Status: status})
}
func (e *Engine) Subscribe(buffer int) (<-chan Event, func()) {
	if buffer < 0 {
		buffer = 0
	}
	ch := make(chan Event, buffer)
	e.listenMu.Lock()
	e.listeners = append(e.listeners, ch)
	e.listenMu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			e.listenMu.Lock()
			defer e.listenMu.Unlock()
			for i, listener := range e.listeners {
				if listener == ch {
					e.listeners = append(e.listeners[:i], e.listeners[i+1:]...)
					close(ch)
					return
				}
			}
		})
	}
}
func (e *Engine) emit(ev Event) {
	e.listenMu.Lock()
	defer e.listenMu.Unlock()
	for _, listener := range e.listeners {
		select {
		case listener <- ev:
		default:
		}
	}
}
func (e *Engine) Start(ctx context.Context) error {
	roots, err := e.store.ListRoots()
	if err != nil {
		return err
	}
	e.mu.Lock()
	if e.rootCancel != nil {
		e.mu.Unlock()
		return nil
	}
	e.rootCtx, e.rootCancel = context.WithCancel(ctx)
	rootCtx := e.rootCtx
	e.mu.Unlock()
	e.setStatus(func(s *Status) { s.State, s.LastError, s.Message = "connecting", "", "" })
	for _, root := range roots {
		if root.Paused {
			continue
		}
		if err := e.AddRootWatch(rootCtx, root); err != nil {
			e.Stop()
			return err
		}
	}
	e.setStatus(func(s *Status) { s.State = "syncing" })
	go e.loop(rootCtx)
	return nil
}
func (e *Engine) Stop() {
	e.mu.Lock()
	cancel := e.rootCancel
	e.rootCancel = nil
	e.rootCtx = nil
	watchers := e.watchers
	e.watchers = map[string]*watcher.Watcher{}
	e.pausedAll = false
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	for _, w := range watchers {
		w.Stop()
	}
	e.setStatus(func(s *Status) { s.State = "stopped" })
}
func (e *Engine) Pause(id string) error {
	if err := e.store.SetPaused(id, true); err != nil {
		return err
	}
	e.mu.Lock()
	w := e.watchers[id]
	delete(e.watchers, id)
	e.mu.Unlock()
	if w != nil {
		w.Stop()
	}
	roots, err := e.store.ListRoots()
	if err != nil {
		return err
	}
	allPaused := len(roots) > 0
	for _, root := range roots {
		allPaused = allPaused && root.Paused
	}
	e.mu.Lock()
	e.pausedAll = allPaused
	e.mu.Unlock()
	if allPaused {
		e.setStatus(func(s *Status) { s.State = "paused"; s.Message = "All folders are paused" })
	}
	return nil
}
func (e *Engine) Resume(ctx context.Context, id string) error {
	if err := e.store.SetPaused(id, false); err != nil {
		return err
	}
	roots, err := e.store.ListRoots()
	if err != nil {
		return err
	}
	for _, r := range roots {
		if r.ID == id {
			e.mu.RLock()
			rootCtx := e.rootCtx
			e.mu.RUnlock()
			if rootCtx != nil {
				if err = e.AddRootWatch(rootCtx, r); err != nil {
					return err
				}
			}
			e.mu.Lock()
			e.pausedAll = false
			e.mu.Unlock()
			e.setStatus(func(s *Status) { s.State, s.Message = "syncing", "" })
			break
		}
	}
	return nil
}
func (e *Engine) AddRootWatch(ctx context.Context, root syncstore.Root) error {
	e.mu.Lock()
	if _, exists := e.watchers[root.ID]; exists {
		e.mu.Unlock()
		return nil
	}
	if e.rootCtx != nil {
		ctx = e.rootCtx
	}
	w := watcher.New(root.LocalPath)
	if err := w.Start(); err != nil {
		e.mu.Unlock()
		return err
	}
	e.watchers[root.ID] = w
	e.mu.Unlock()
	go e.consume(ctx, root, w)
	return nil
}
func (e *Engine) RemoveRootWatch(id string) error {
	e.mu.Lock()
	w := e.watchers[id]
	delete(e.watchers, id)
	e.mu.Unlock()
	if w != nil {
		w.Stop()
	}
	return e.store.RemoveRoot(id)
}
func (e *Engine) consume(ctx context.Context, root syncstore.Root, w *watcher.Watcher) {
	for {
		select {
		case ev, ok := <-w.Events():
			if !ok {
				return
			}
			if ev.Op == "rescan" {
				e.scan(root)
				continue
			}
			e.localEvent(root, ev)
		case <-ctx.Done():
			return
		}
	}
}
func (e *Engine) scan(root syncstore.Root) {
	_ = filepath.WalkDir(root.LocalPath, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root.LocalPath, p)
		e.queueUpload(root, filepath.ToSlash(rel))
		return nil
	})
}
func (e *Engine) localEvent(root syncstore.Root, ev watcher.Event) {
	if ev.RelPath == "" {
		return
	}
	if ev.Op == "remove" || ev.Op == "rename" {
		n, err := e.store.GetNodeByRel(root.ID, ev.RelPath)
		if err == nil && n.RemoteID != "" {
			b, _ := json.Marshal(map[string]string{"remoteId": n.RemoteID})
			_, _ = e.store.EnqueueJob(root.ID, "trash_remote", string(b))
		}
		return
	}
	e.queueUpload(root, ev.RelPath)
}
func (e *Engine) queueUpload(root syncstore.Root, rel string) {
	path := filepath.Join(root.LocalPath, filepath.FromSlash(rel))
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return
	}
	hash, err := fileHash(path)
	if err != nil {
		return
	}
	n, _ := e.store.GetNodeByRel(root.ID, rel)
	if n.SyncState == "syncing" && n.ContentHash == hash {
		return
	}
	payload, _ := json.Marshal(map[string]string{"path": path, "rel": rel, "hash": hash})
	_, _ = e.store.EnqueueJob(root.ID, "upload", string(payload))
}
func (e *Engine) loop(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	poll := time.NewTicker(e.cfg.PollInterval)
	defer tick.Stop()
	defer poll.Stop()
	for {
		select {
		case <-tick.C:
			e.process(ctx)
		case <-poll.C:
			e.pull(ctx)
		case <-ctx.Done():
			e.Stop()
			return
		}
	}
}
func (e *Engine) process(ctx context.Context) {
	jobs, err := e.store.ListDueJobs(20)
	if err != nil {
		return
	}
	roots, _ := e.store.ListRoots()
	byID := map[string]syncstore.Root{}
	for _, r := range roots {
		byID[r.ID] = r
	}
	for _, j := range jobs {
		root, ok := byID[j.RootID]
		if !ok || root.Paused {
			continue
		}
		var err error
		switch j.Kind {
		case "upload":
			var p struct{ Path, Rel, Hash string }
			_ = json.Unmarshal([]byte(j.Payload), &p)
			info, statErr := os.Stat(p.Path)
			if statErr != nil {
				err = statErr
				break
			}
			e.setProgress(func(s *Status) { s.State = "syncing"; s.CurrentFile = p.Path; s.BytesTotal = info.Size(); s.BytesDone = 0; s.Percent = 0 })
			entry, upErr := e.api.UploadFile(ctx, root.RemoteParentID, p.Path, syncclient.Manifest{RelativePath: p.Rel, FileName: filepath.Base(p.Path), ExpectedSize: info.Size(), LastModifiedMs: info.ModTime().UnixMilli(), ContentHash: p.Hash})
			err = upErr
			if err == nil {
				_ = e.store.UpsertNode(syncstore.Node{ID: entry.ID, RootID: j.RootID, LocalRel: p.Rel, RemoteID: entry.ID, Kind: "file", Size: info.Size(), MtimeMS: info.ModTime().UnixMilli(), ContentHash: p.Hash, ContentVersion: entry.Version, SyncState: "synced"})
				e.setProgress(func(s *Status) { s.BytesSynced += info.Size(); s.BytesDone = info.Size(); s.Percent = 100 })
				e.addActivity(j.RootID, "upload", p.Rel, "Uploaded file")
			}
		case "trash_remote":
			var p struct {
				RemoteID string `json:"remoteId"`
			}
			_ = json.Unmarshal([]byte(j.Payload), &p)
			err = e.api.Trash(ctx, p.RemoteID)
			if err == nil {
				e.addActivity(j.RootID, "trash", p.RemoteID, "Moved remote item to trash")
			}
		}
		if err == nil {
			_ = e.store.MarkJobDone(j.ID)
		} else {
			_ = e.store.MarkJobRetry(j.ID, j.Attempts+1, err.Error())
			e.setStatus(func(s *Status) { s.State, s.LastError = "error", err.Error() })
		}
	}
	pending, err := e.store.CountPendingJobs()
	if err == nil {
		e.setProgress(func(s *Status) {
			s.PendingJobs = pending
			if pending == 0 && s.LastError == "" && s.State != "paused" && s.State != "stopped" {
				s.State, s.CurrentFile, s.BytesTotal, s.BytesDone, s.Percent = "synced", "", 0, 0, 0
			}
		})
	}
}
func (e *Engine) pull(ctx context.Context) {
	roots, err := e.store.ListRoots()
	if err != nil {
		return
	}
	for _, r := range roots {
		if r.Paused {
			continue
		}
		changes, err := e.api.Changes(ctx, r.Cursor, 200)
		if err != nil {
			e.setStatus(func(s *Status) { s.State, s.LastError = "offline", err.Error() })
			continue
		}
		e.setStatus(func(s *Status) { s.State, s.LastError = "syncing", "" })
		for _, change := range changes.Changes {
			e.apply(ctx, r, change)
		}
		if changes.Cursor > r.Cursor {
			_ = e.store.SetCursor(r.ID, changes.Cursor)
		}
	}
}
func (e *Engine) apply(ctx context.Context, r syncstore.Root, c syncclient.Change) {
	n, _ := e.store.GetNodeByRemote(r.ID, c.Entry.ID)
	rel := n.LocalRel
	if rel == "" {
		rel = c.Entry.Name
	}
	path := filepath.Join(r.LocalPath, filepath.FromSlash(rel))
	if c.Entry.Deleted() || c.Type == "trash" || c.Type == "purge" {
		if err := moveToTrash(path); err != nil {
			e.setStatus(func(s *Status) { s.State, s.LastError = "error", err.Error() })
			return
		}
		e.addActivity(r.ID, "trash", rel, "Removed local item")
		return
	}
	if n.SyncState == "dirty" {
		conflict := conflictPath(path, e.cfg.DeviceName)
		_ = os.Rename(path, conflict)
		e.queueUpload(r, filepath.ToSlash(strings.TrimPrefix(conflict, r.LocalPath+string(os.PathSeparator))))
	}
	e.setProgress(func(s *Status) { s.State = "syncing"; s.CurrentFile = path; s.BytesTotal, s.BytesDone, s.Percent = 0, 0, 0 })
	if err := e.api.Download(ctx, c.Entry.ID, path, true); err != nil {
		e.setStatus(func(s *Status) { s.State, s.LastError = "offline", err.Error() })
		return
	}
	info, _ := os.Stat(path)
	var size, mtime int64
	if info != nil {
		size = info.Size()
		mtime = info.ModTime().UnixMilli()
	}
	_ = e.store.UpsertNode(syncstore.Node{ID: c.Entry.ID, RootID: r.ID, LocalRel: rel, RemoteID: c.Entry.ID, Kind: c.Entry.Kind, Size: size, MtimeMS: mtime, ContentVersion: c.Entry.Version, SyncState: "syncing"})
	e.setProgress(func(s *Status) { s.BytesSynced += size; s.BytesTotal = size; s.BytesDone = size; s.Percent = 100 })
	e.addActivity(r.ID, "download", rel, "Downloaded file")
}
func (e *Engine) addActivity(rootID, kind, path, message string) {
	if err := e.store.AddActivity(rootID, kind, path, message); err != nil {
		return
	}
	activities, err := e.store.ListActivities(1)
	if err == nil && len(activities) == 1 {
		e.emit(Event{Type: "activity", Status: e.Status(), Activity: &activities[0]})
	}
}
func fileHash(path string) (string, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	if _, e = io.Copy(h, f); e != nil {
		return "", e
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func conflictPath(path, device string) string {
	if device == "" {
		device = "device"
	}
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	return fmt.Sprintf("%s (çakışan kopya - %s - %s)%s", base, device, time.Now().Format("2006-01-02"), ext)
}
