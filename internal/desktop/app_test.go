package desktop

import (
	"os"
	"path/filepath"
	"testing"

	"necipdrive/internal/syncstore"

	"github.com/google/uuid"
)

func TestDefaultDataDir(t *testing.T) {
	dir := DefaultDataDir()
	if dir == "" {
		t.Fatal("empty data dir")
	}
}

func TestSnapshotIncludesFoldersAndActivities(t *testing.T) {
	root := t.TempDir()
	app, err := New(Config{DataDir: filepath.Join(root, "data")})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	folder := filepath.Join(root, "sync")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	if err := app.store.AddRoot(syncstore.Root{ID: id, LocalPath: folder, RemoteParentID: "remote"}); err != nil {
		t.Fatal(err)
	}
	if err := app.store.AddActivity(id, "upload", "a.txt", "Uploaded file"); err != nil {
		t.Fatal(err)
	}

	snap := app.Snapshot()
	if len(snap.Folders) != 1 || snap.Folders[0].ID != id {
		t.Fatalf("folders=%+v", snap.Folders)
	}
	if _, err := uuid.Parse(snap.Folders[0].ID); err != nil {
		t.Fatalf("folder id is not uuid: %v", err)
	}
	if len(snap.Activities) != 1 || snap.Activities[0].Kind != "upload" {
		t.Fatalf("activities=%+v", snap.Activities)
	}
}

func TestRemoveFolderDeletesRoot(t *testing.T) {
	root := t.TempDir()
	app, err := New(Config{DataDir: filepath.Join(root, "data")})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	id := uuid.NewString()
	folder := filepath.Join(root, "gone")
	_ = os.MkdirAll(folder, 0o755)
	if err := app.store.AddRoot(syncstore.Root{ID: id, LocalPath: folder, RemoteParentID: "r"}); err != nil {
		t.Fatal(err)
	}
	if err := app.RemoveFolder(id); err != nil {
		t.Fatal(err)
	}
	roots, err := app.ListFolders()
	if err != nil || len(roots) != 0 {
		t.Fatalf("roots=%+v err=%v", roots, err)
	}
}
