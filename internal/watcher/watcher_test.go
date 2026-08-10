package watcher

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWatcherEmitsCreatedFile(t *testing.T) {
	root := t.TempDir()
	w := New(root)
	w.SetRescanInterval(0)
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("ok"), 0644); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(3 * time.Second)
	for {
		select {
		case event := <-w.Events():
			if event.RelPath == "new.txt" && (event.Op == "create" || event.Op == "write") {
				return
			}
		case <-deadline:
			t.Fatal("no file event")
		}
	}
}
