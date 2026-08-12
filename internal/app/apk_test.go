package app

import "testing"

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
