package app

import (
	"encoding/json"
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
