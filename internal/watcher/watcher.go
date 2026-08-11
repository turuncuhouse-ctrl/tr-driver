// Package watcher provides recursive, debounced filesystem events.
package watcher

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

type Event struct{ Root, RelPath, Op string }
type Watcher struct {
	root   string
	events chan Event
	stop   chan struct{}
	done   chan struct{}
	once   sync.Once
	rescan time.Duration
}

func New(root string) *Watcher {
	return &Watcher{root: filepath.Clean(root), events: make(chan Event, 128), stop: make(chan struct{}), done: make(chan struct{}), rescan: 2 * time.Minute}
}
func (w *Watcher) Events() <-chan Event              { return w.events }
func (w *Watcher) SetRescanInterval(d time.Duration) { w.rescan = d }
func (w *Watcher) Start() error {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	if err = w.addTree(fw, w.root); err != nil {
		fw.Close()
		return err
	}
	go w.run(fw)
	return nil
}
func (w *Watcher) Stop() { w.once.Do(func() { close(w.stop) }); <-w.done }
func (w *Watcher) addTree(fw *fsnotify.Watcher, root string) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && !ignored(filepath.Base(path)) {
			return fw.Add(path)
		}
		return nil
	})
}
func (w *Watcher) run(fw *fsnotify.Watcher) {
	defer close(w.done)
	defer close(w.events)
	defer fw.Close()
	pending := map[string]string{}
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	var tick <-chan time.Time
	var ticker *time.Ticker
	if w.rescan > 0 {
		ticker = time.NewTicker(w.rescan)
		defer ticker.Stop()
		tick = ticker.C
	}
	flush := func() {
		for rel, op := range pending {
			w.emit(Event{w.root, rel, op})
		}
		pending = map[string]string{}
	}
	for {
		select {
		case ev, ok := <-fw.Events:
			if !ok {
				return
			}
			if ignored(filepath.Base(ev.Name)) {
				continue
			}
			if ev.Op&fsnotify.Create != 0 {
				if info, e := os.Stat(ev.Name); e == nil && info.IsDir() {
					_ = w.addTree(fw, ev.Name)
				}
			}
			op := mapOp(ev.Op)
			if op == "" {
				continue
			}
			rel, e := filepath.Rel(w.root, ev.Name)
			if e != nil || strings.HasPrefix(rel, "..") {
				continue
			}
			pending[filepath.ToSlash(rel)] = op
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(250 * time.Millisecond)
		case <-timer.C:
			flush()
		case <-tick:
			w.emit(Event{Root: w.root, Op: "rescan"})
		case <-w.stop:
			return
		case <-fw.Errors: /* a later rescan reconciles transient watcher errors */
		}
	}
}
func (w *Watcher) emit(e Event) {
	select {
	case w.events <- e:
	case <-w.stop:
	}
}
func mapOp(op fsnotify.Op) string {
	if op&fsnotify.Remove != 0 {
		return "remove"
	}
	if op&fsnotify.Rename != 0 {
		return "rename"
	}
	if op&fsnotify.Create != 0 {
		return "create"
	}
	if op&fsnotify.Write != 0 {
		return "write"
	}
	return ""
}
func ignored(name string) bool {
	l := strings.ToLower(name)
	return strings.HasSuffix(l, ".partial") || strings.HasSuffix(l, ".tmp") || strings.HasPrefix(name, "~$") || l == ".ds_store" || l == "desktop.ini" || l == "thumbs.db" || strings.HasPrefix(l, ".necipdrive")
}
