package backup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPickProbe(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "tmp", "uploads"), 0o755); err != nil {
		t.Fatal(err)
	}
	// tmp holds the smallest file but must never be picked.
	if err := os.WriteFile(filepath.Join(root, "tmp", "x.part"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "big.bin"), make([]byte, 1024), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "small.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	rel, digest, err := PickProbe(root)
	if err != nil {
		t.Fatal(err)
	}
	if rel != "docs/small.txt" {
		t.Fatalf("expected docs/small.txt, got %q", rel)
	}
	if digest == "" {
		t.Fatal("empty digest")
	}
	again, err := HashFile(filepath.Join(root, "docs", "small.txt"))
	if err != nil || again != digest {
		t.Fatalf("hash not stable: %q vs %q (%v)", again, digest, err)
	}
}

func TestPickProbeEmpty(t *testing.T) {
	root := t.TempDir()
	if _, _, err := PickProbe(root); err != ErrNoProbe {
		t.Fatalf("expected ErrNoProbe, got %v", err)
	}
}

func TestFindSingleFile(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(nested, "f.txt")
	if err := os.WriteFile(want, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := FindSingleFile(root)
	if err != nil || got != want {
		t.Fatalf("got %q err=%v", got, err)
	}
	if err := os.WriteFile(filepath.Join(root, "second"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := FindSingleFile(root); err == nil {
		t.Fatal("expected error with two files")
	}
}
