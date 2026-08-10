package files

import "testing"

func TestCanInlinePreview(t *testing.T) {
	cases := []struct {
		name, mime string
		want       bool
	}{
		{"photo.jpg", "image/jpeg", true},
		{"clip.mp4", "video/mp4", true},
		{"song.mp3", "audio/mpeg", true},
		{"doc.pdf", "application/pdf", true},
		{"logo.svg", "image/svg+xml", false},
		{"notes.txt", "text/plain", false},
		{"unknown.bin", "application/octet-stream", false},
		{"fallback.webp", "application/octet-stream", true},
		{"movie.webm", "", true},
	}
	for _, tc := range cases {
		if got := canInlinePreview(tc.name, tc.mime); got != tc.want {
			t.Fatalf("%s (%s): got %v want %v", tc.name, tc.mime, got, tc.want)
		}
	}
}

func TestResolveContentType(t *testing.T) {
	if got := resolveContentType("a.png", "application/octet-stream"); got != "image/png" {
		t.Fatalf("expected image/png, got %s", got)
	}
	if got := resolveContentType("a.bin", "video/mp4"); got != "video/mp4" {
		t.Fatalf("expected stored mime, got %s", got)
	}
}
