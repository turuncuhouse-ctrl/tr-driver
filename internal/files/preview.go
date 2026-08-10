package files

import (
	"mime"
	"path/filepath"
	"strings"
)

func resolveContentType(name, mimeType string) string {
	mimeType = strings.TrimSpace(strings.ToLower(mimeType))
	if mimeType != "" && mimeType != "application/octet-stream" {
		return mimeType
	}
	if guessed := mime.TypeByExtension(strings.ToLower(filepath.Ext(name))); guessed != "" {
		return strings.ToLower(guessed)
	}
	if mimeType != "" {
		return mimeType
	}
	return "application/octet-stream"
}

func canInlinePreview(name, mimeType string) bool {
	contentType := resolveContentType(name, mimeType)
	switch {
	case strings.HasPrefix(contentType, "image/"):
		return contentType != "image/svg+xml"
	case strings.HasPrefix(contentType, "video/"):
		return true
	case strings.HasPrefix(contentType, "audio/"):
		return true
	case contentType == "application/pdf":
		return true
	}
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".avif", ".bmp",
		".mp4", ".webm", ".ogg", ".ogv",
		".mp3", ".wav", ".m4a", ".aac",
		".pdf":
		return true
	default:
		return false
	}
}
