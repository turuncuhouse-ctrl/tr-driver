package storage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalSaveAndOpen(t *testing.T) {
	root := t.TempDir()
	store, err := NewLocal(root)
	if err != nil {
		t.Fatalf("NewLocal failed: %v", err)
	}

	size, err := store.Save(context.Background(), "user/test/file.txt", strings.NewReader("hello"))
	if err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	if size != 5 {
		t.Fatalf("expected 5 bytes, got %d", size)
	}

	file, err := store.Open("user/test/file.txt")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	file.Close()

	if _, err := os.Stat(filepath.Join(root, "user", "test", "file.txt")); err != nil {
		t.Fatalf("expected file on disk: %v", err)
	}
}

func TestAppendTruncateAndFinalize(t *testing.T) {
	root := t.TempDir()
	store, err := NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureEmpty("tmp/a.part"); err != nil {
		t.Fatal(err)
	}
	written, err := store.AppendAt(context.Background(), "tmp/a.part", 0, strings.NewReader("abcd"))
	if err != nil || written != 4 {
		t.Fatalf("append failed: %v written=%d", err, written)
	}
	written, err = store.AppendAt(context.Background(), "tmp/a.part", 4, strings.NewReader("ef"))
	if err != nil || written != 2 {
		t.Fatalf("second append failed: %v written=%d", err, written)
	}
	size, err := store.Size("tmp/a.part")
	if err != nil || size != 6 {
		t.Fatalf("size=%d err=%v", size, err)
	}
	if err := store.Truncate("tmp/a.part", 4); err != nil {
		t.Fatal(err)
	}
	size, err = store.Size("tmp/a.part")
	if err != nil || size != 4 {
		t.Fatalf("truncated size=%d err=%v", size, err)
	}
	if err := store.Finalize("tmp/a.part", "user/final.bin"); err != nil {
		t.Fatal(err)
	}
	size, err = store.Size("user/final.bin")
	if err != nil || size != 4 {
		t.Fatalf("final size=%d err=%v", size, err)
	}
}

func TestAppendWrongOffset(t *testing.T) {
	root := t.TempDir()
	store, err := NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureEmpty("tmp/b.part"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendAt(context.Background(), "tmp/b.part", 0, strings.NewReader("ab")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendAt(context.Background(), "tmp/b.part", 10, strings.NewReader("x")); err == nil {
		t.Fatal("expected future offset to fail")
	}
}

func TestDeletePart(t *testing.T) {
	root := t.TempDir()
	store, err := NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureEmpty("tmp/c.part"); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete("tmp/c.part"); err != nil {
		t.Fatal(err)
	}
	size, err := store.Size("tmp/c.part")
	if err != nil || size != 0 {
		t.Fatalf("expected missing part size 0, got size=%d err=%v", size, err)
	}
}

func TestRejectPathEscape(t *testing.T) {
	root := t.TempDir()
	store, err := NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(context.Background(), "../escape.txt", strings.NewReader("x")); err == nil {
		t.Fatal("expected path escape to fail")
	}
}
