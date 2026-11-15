package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestIsAPKRequest(t *testing.T) {
	cases := map[string]bool{
		"/download/TRDriver.apk":   true,
		"/apps/TRDriver.apk":       true,
		"/download/trdriver.apk":   true,
		"/apps/foo.apk":            true,
		"/index.html":              false,
		"/api/files":               false,
		"/download/TRDriver.apk?x": false, // path only; query stripped by net/http before Path
	}
	for path, want := range cases {
		if got := isAPKRequest(path); got != want {
			t.Fatalf("%s: got %v want %v", path, got, want)
		}
	}
}

func TestStripUTF8BOM(t *testing.T) {
	raw := []byte{0xEF, 0xBB, 0xBF, '{', '"', 'a', '"', ':', '1', '}'}
	got := stripUTF8BOM(raw)
	if got[0] != '{' {
		t.Fatalf("bom not stripped: %v", got)
	}
	var m map[string]any
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatal(err)
	}
}

func TestAndroidAppsCandidatesPreferPublic(t *testing.T) {
	c := androidAppsCandidates("android-version.json")
	if len(c) == 0 {
		t.Fatal("empty candidates")
	}
	foundPublic := false
	foundDist := false
	publicIdx, distIdx := -1, -1
	for i, p := range c {
		if stringsHasSuffixFold(p, "web/public/apps/android-version.json") ||
			stringsHasSuffixFold(p, `web\public\apps\android-version.json`) {
			foundPublic = true
			if publicIdx < 0 {
				publicIdx = i
			}
		}
		if stringsHasSuffixFold(p, "web/dist/apps/android-version.json") ||
			stringsHasSuffixFold(p, `web\dist\apps\android-version.json`) {
			foundDist = true
			if distIdx < 0 {
				distIdx = i
			}
		}
	}
	if !foundPublic || !foundDist {
		t.Fatalf("expected public+dist candidates, got %#v", c)
	}
	if publicIdx > distIdx {
		t.Fatalf("public should be preferred before dist: public=%d dist=%d", publicIdx, distIdx)
	}
}

func stringsHasSuffixFold(s, suffix string) bool {
	s = stringsToLower(s)
	suffix = stringsToLower(suffix)
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}

func stringsToLower(s string) string {
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c == '\\' {
			c = '/'
		}
		b[i] = c
	}
	return string(b)
}

func TestSHA256File(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sample.bin")
	if err := os.WriteFile(p, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	sum, err := sha256File(p)
	if err != nil {
		t.Fatal(err)
	}
	// NIST vector for "abc".
	const want = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if sum != want {
		t.Fatalf("sha256File = %s, want %s", sum, want)
	}
	if _, err := sha256File(filepath.Join(dir, "missing.bin")); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestCurrentAPKSHA256CachesPerFileState(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "TRDriver.apk")
	if err := os.WriteFile(p, []byte("first-version"), 0o644); err != nil {
		t.Fatal(err)
	}
	st1, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	sum1, ok := currentAPKSHA256(p, st1.Size(), st1.ModTime())
	if !ok || sum1 == "" {
		t.Fatalf("first hash failed: ok=%v sum=%q", ok, sum1)
	}
	// Second call with the same state must return the cached value.
	sum1b, ok := currentAPKSHA256(p, st1.Size(), st1.ModTime())
	if !ok || sum1b != sum1 {
		t.Fatalf("cache miss: %q vs %q (ok=%v)", sum1b, sum1, ok)
	}
	// A changed file must produce a new hash.
	if err := os.WriteFile(p, []byte("second-version-longer"), 0o644); err != nil {
		t.Fatal(err)
	}
	st2, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	sum2, ok := currentAPKSHA256(p, st2.Size(), st2.ModTime())
	if !ok {
		t.Fatal("hash after change failed")
	}
	if sum2 == sum1 {
		t.Fatal("hash did not change after file content changed")
	}
}

func TestAndroidVersionInfoJSONHasSHAField(t *testing.T) {
	raw, err := json.Marshal(androidVersionInfo{
		VersionCode: 44,
		VersionName: "0.10.0",
		ApkSha256:   "deadbeef",
	})
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	got, _ := parsed["apkSha256"].(string)
	if got != "deadbeef" {
		t.Fatalf("apkSha256 = %q, want deadbeef", got)
	}
}
