package sharedspace

import (
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCleanRelRejectsTraversal(t *testing.T) {
	rejected := []string{
		"..", "../", "/..", "../x", "a/..", "a/../b", "a/b/../../..", "./a", "a/./b", ".",
		`..\x`, `a\..\b`, `a\b`, "a//b",
		"a/\x00/b", "a\x00", "a/b\n", "a/\x7f", "\x1b[31m",
		".trdriver", ".trdriver/tmp/x", ".TRDRIVER/tmp", "/.trdriver/",
		strings.Repeat("a", maxNameBytes+1),
		strings.Repeat("ab/", maxPathBytes),
		"a/\xff\xfe",
	}
	for _, raw := range rejected {
		if got, err := CleanRel(raw); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("CleanRel(%q) = %q, %v; want ErrInvalidPath", raw, got, err)
		}
	}
	accepted := map[string]string{
		"":                          "",
		"/":                         "",
		"//":                        "",
		"a":                         "a",
		"/a/b/":                     "a/b",
		"Filmler/2024/film.mkv":     "Filmler/2024/film.mkv",
		"Türkçe klasör/dosya ş.txt": "Türkçe klasör/dosya ş.txt",
		"a..b/..c/d..":              "a..b/..c/d..",
		"...":                       "...",
		"Alt/.trdriver":             "Alt/.trdriver", // only reserved at the top level
	}
	for raw, want := range accepted {
		got, err := CleanRel(raw)
		if err != nil || got != want {
			t.Errorf("CleanRel(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
}

func TestIsUnderComparesWholeComponents(t *testing.T) {
	cases := []struct {
		rel, ancestor string
		want          bool
	}{
		{"a/b", "a", true}, {"a", "a", true}, {"a", "", true}, {"", "", true},
		{"ab", "a", false}, {"a", "a/b", false}, {"", "a", false}, {"a/bc", "a/b", false},
	}
	for _, c := range cases {
		if got := isUnder(c.rel, c.ancestor); got != c.want {
			t.Errorf("isUnder(%q, %q) = %v, want %v", c.rel, c.ancestor, got, c.want)
		}
	}
}

// Every way of spelling "outside the shared folder" must fail before any file
// is touched, for every operation, even for an administrator.
func TestTraversalIsRejectedOverHTTP(t *testing.T) {
	env := newTestEnv(t)
	payloads := []string{
		"../disarida.txt",
		"..",
		"Filmler/../../disarida.txt",
		"Filmler/../Gizli/sir.txt",
		`..\disarida.txt`,
		"/../disarida.txt",
		"Filmler/./film.mkv",
		"Filmler/\x00/../../disarida.txt",
		filepath.Join(env.root, "disarida.txt"), // absolute path
		".trdriver/tmp",
	}
	for _, payload := range payloads {
		query := "?path=" + url.QueryEscape(payload)
		for _, target := range []string{BasePath + "download" + query, BasePath + "list" + query} {
			rec := env.do("admin", http.MethodGet, target, nil)
			if strings.Contains(rec.Body.String(), "DISARIDAKI-SIR") {
				t.Fatalf("%s leaked a file outside the shared folder", target)
			}
			// An absolute path loses its leading slash and simply does not
			// exist inside the folder; everything else is a bad request.
			if rec.Code != http.StatusBadRequest && rec.Code != http.StatusNotFound {
				t.Errorf("GET %s = %d, want 400 or 404; body: %s", target, rec.Code, rec.Body.String())
			}
		}
		for _, target := range []string{"delete", "rename", "folder"} {
			rec := env.doJSON("admin", http.MethodPost, BasePath+target, map[string]string{"path": payload, "name": "x"})
			if rec.Code != http.StatusBadRequest && rec.Code != http.StatusNotFound {
				t.Errorf("POST %s path=%q = %d, want 400 or 404", target, payload, rec.Code)
			}
		}
	}
	// Traversal smuggled through the name instead of the path.
	for _, name := range []string{"..", "../kacak.txt", `..\kacak.txt`, "a/b", ".", "", "x\x00y", ".trdriver"} {
		rec := env.do("admin", http.MethodPost, BasePath+"upload?path=&name="+url.QueryEscape(name), strings.NewReader("veri"))
		wantStatus(t, rec, http.StatusBadRequest)
		rec = env.doJSON("admin", http.MethodPost, BasePath+"folder", map[string]string{"path": "", "name": name})
		wantStatus(t, rec, http.StatusBadRequest)
		rec = env.doJSON("admin", http.MethodPost, BasePath+"rename", map[string]string{"path": "kok.txt", "name": name})
		wantStatus(t, rec, http.StatusBadRequest)
	}
	if data, err := os.ReadFile(filepath.Join(env.root, "disarida.txt")); err != nil || string(data) != "DISARIDAKI-SIR" {
		t.Fatalf("file outside the shared folder was modified: %q, %v", data, err)
	}
	entries, err := os.ReadDir(env.root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("something was created outside the shared folder: %v", entries)
	}
}

// A symbolic link placed in the folder (by someone with shell access, or by a
// restored backup) must not become a way out of it or around the grants.
func TestSymlinksAreNeverFollowed(t *testing.T) {
	env := newTestEnv(t)
	outsideDir := filepath.Join(env.root, "disari-klasor")
	if err := os.Mkdir(outsideDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outsideDir, "ic.txt"), []byte("DISARIDAKI-SIR"), 0o600); err != nil {
		t.Fatal(err)
	}
	links := map[string]string{
		"Filmler/kacak.txt":  filepath.Join(env.root, "disarida.txt"), // file outside
		"Filmler/kacak-dir":  outsideDir,                              // directory outside
		"Filmler/gizli-link": filepath.Join(env.dir, "Gizli"),         // inside, but another grant's area
		"Filmler/goreli":     "../../disarida.txt",
		// Relative and entirely inside the shared folder: nothing "escapes",
		// yet it would let a grant on Filmler read another grant's area.
		"Filmler/ic-link": "../Gizli",
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(env.dir, filepath.FromSlash(name))); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}

	listing := decode[Listing](t, env.do("admin", http.MethodGet, BasePath+"list?path=Filmler", nil))
	for _, name := range entryNames(listing) {
		if strings.HasPrefix(name, "kacak") || name == "gizli-link" || name == "goreli" || name == "ic-link" {
			t.Errorf("listing shows symbolic link %q", name)
		}
	}
	for _, target := range []string{
		"download?path=Filmler/kacak.txt",
		"download?path=Filmler/goreli",
		"download?path=Filmler/kacak-dir/ic.txt",
		"download?path=Filmler/gizli-link/sir.txt",
		"list?path=Filmler/kacak-dir",
		"list?path=Filmler/gizli-link",
		"download?path=Filmler/ic-link/sir.txt",
		"list?path=Filmler/ic-link",
	} {
		// "reader" may read Filmler; the links must not widen that.
		for _, user := range []string{"reader", "admin"} {
			rec := env.do(user, http.MethodGet, BasePath+target, nil)
			wantStatus(t, rec, http.StatusNotFound)
			if strings.Contains(rec.Body.String(), "SIR") || strings.Contains(rec.Body.String(), "gizli") {
				t.Fatalf("%s as %s leaked content through a symbolic link", target, user)
			}
		}
	}
	// Writing through a linked directory is refused as well.
	rec := env.do("writer", http.MethodPost, BasePath+"upload?path=Filmler/kacak-dir&name=yeni.txt", strings.NewReader("veri"))
	wantStatus(t, rec, http.StatusNotFound)
	rec = env.doJSON("writer", http.MethodPost, BasePath+"folder", map[string]string{"path": "Filmler/kacak-dir", "name": "yeni"})
	wantStatus(t, rec, http.StatusNotFound)
	if entries, _ := os.ReadDir(outsideDir); len(entries) != 1 {
		t.Fatalf("wrote through a symbolic link: %v", entries)
	}

	// Deleting the folder removes the links themselves, never their targets.
	wantStatus(t, env.doJSON("admin", http.MethodPost, BasePath+"delete", map[string]string{"path": "Filmler"}), http.StatusOK)
	if env.exists("Filmler") {
		t.Fatal("folder was not deleted")
	}
	for _, kept := range []string{filepath.Join(env.root, "disarida.txt"), filepath.Join(outsideDir, "ic.txt"), filepath.Join(env.dir, "Gizli", "sir.txt")} {
		if _, err := os.Stat(kept); err != nil {
			t.Fatalf("deleting a folder followed a symbolic link and removed %s", kept)
		}
	}
}
