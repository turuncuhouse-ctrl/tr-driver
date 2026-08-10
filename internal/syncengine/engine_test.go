package syncengine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"necipdrive/internal/syncclient"
	"necipdrive/internal/syncstore"
)

type fakeAPI struct{ uploaded string }

func (f *fakeAPI) Changes(context.Context, int64, int) (syncclient.ChangesResponse, error) {
	return syncclient.ChangesResponse{}, nil
}
func (f *fakeAPI) UploadFile(_ context.Context, _ string, path string, _ syncclient.Manifest) (syncclient.Entry, error) {
	f.uploaded = path
	return syncclient.Entry{ID: "remote"}, nil
}
func (f *fakeAPI) Trash(context.Context, string) error                  { return nil }
func (f *fakeAPI) Download(context.Context, string, string, bool) error { return nil }

func TestConflictPathKeepsBoth(t *testing.T) {
	got := conflictPath(`C:\Sync\report.pdf`, "PC1")
	if !strings.Contains(got, "çakışan kopya - PC1 -") || !strings.HasSuffix(got, ".pdf") {
		t.Fatalf("unexpected conflict path: %s", got)
	}
}

func TestLocalWriteQueuesAndUploads(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "note.txt")
	if err := os.WriteFile(path, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	s, err := syncstore.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.AddRoot(syncstore.Root{ID: "r", LocalPath: root}); err != nil {
		t.Fatal(err)
	}
	api := &fakeAPI{}
	e := New(s, api, Config{})
	e.queueUpload(syncstore.Root{ID: "r", LocalPath: root}, "note.txt")
	e.process(context.Background())
	if api.uploaded != path {
		t.Fatalf("uploaded %q", api.uploaded)
	}
	n, err := s.GetNodeByRel("r", "note.txt")
	if err != nil || n.RemoteID != "remote" {
		t.Fatalf("%+v %v", n, err)
	}
}

func TestSubscribeReceivesStatusEvents(t *testing.T) {
	s, err := syncstore.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e := New(s, &fakeAPI{}, Config{})
	ch, unsub := e.Subscribe(8)
	defer unsub()
	e.setStatus(func(st *Status) { st.State, st.Message = "syncing", "hello" })
	select {
	case ev := <-ch:
		if ev.Type != "status" || ev.Status.State != "syncing" {
			t.Fatalf("event=%+v", ev)
		}
	default:
		t.Fatal("expected status event")
	}
}

func TestAddActivityEmitsEvent(t *testing.T) {
	s, err := syncstore.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e := New(s, &fakeAPI{}, Config{})
	ch, unsub := e.Subscribe(8)
	defer unsub()
	e.addActivity("r", "download", "x.txt", "Downloaded file")
	select {
	case ev := <-ch:
		if ev.Type != "activity" || ev.Activity == nil || ev.Activity.Kind != "download" {
			t.Fatalf("event=%+v", ev)
		}
	default:
		t.Fatal("expected activity event")
	}
}

func TestProcessSkipsPausedRoot(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "paused.txt")
	if err := os.WriteFile(path, []byte("do not upload"), 0644); err != nil {
		t.Fatal(err)
	}
	s, err := syncstore.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.AddRoot(syncstore.Root{ID: "paused", LocalPath: root, Paused: true}); err != nil {
		t.Fatal(err)
	}
	api := &fakeAPI{}
	e := New(s, api, Config{})
	e.queueUpload(syncstore.Root{ID: "paused", LocalPath: root}, "paused.txt")
	e.process(context.Background())
	if api.uploaded != "" {
		t.Fatalf("paused root uploaded %q", api.uploaded)
	}
	if pending, err := s.CountPendingJobs(); err != nil || pending != 1 {
		t.Fatalf("pending=%d err=%v", pending, err)
	}
}
