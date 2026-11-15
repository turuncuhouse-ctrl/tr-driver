// Package sharedspace implements "Ortak Paylaşım": a server-wide shared folder
// (SHARED_DIR, a real directory on its own volume) that the embedded torrent
// client downloads into and that users browse from the web panel and the
// Android app.
//
// Security model in one place:
//   - Every client-supplied path goes through CleanRel, which rejects (never
//     "normalises") traversal components, absolute paths and control bytes.
//   - All file access goes through an os.Root opened on SHARED_DIR, so even a
//     path that slipped past validation cannot resolve outside the folder.
//   - Symbolic links are never followed and never listed.
//   - Access is deny-by-default: a non-admin user sees nothing without a grant.
package sharedspace

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// InternalDir is a reserved top-level directory inside the shared folder used
// for in-flight uploads. It is never listed and cannot be addressed by clients
// or by torrent content.
const InternalDir = ".trdriver"

const (
	maxPathBytes = 4096
	maxNameBytes = 255
)

var (
	// ErrInvalidPath is returned for any path that is not a plain, relative,
	// slash-separated path inside the shared folder.
	ErrInvalidPath = errors.New("geçersiz yol")
	// ErrInvalidName is returned for unusable file or folder names.
	ErrInvalidName = errors.New("geçersiz ad")
)

// ValidName reports whether name is usable as a single path component.
func ValidName(name string) error {
	if name == "" || name == "." || name == ".." {
		return ErrInvalidName
	}
	if len(name) > maxNameBytes || !utf8.ValidString(name) {
		return ErrInvalidName
	}
	for _, r := range name {
		// Separators of either flavour, NUL and control characters are refused:
		// they are how traversal and header/log injection are smuggled in.
		if r == '/' || r == '\\' || r < 0x20 || r == 0x7f {
			return ErrInvalidName
		}
	}
	return nil
}

// CleanRel validates a client-supplied path and returns its canonical form:
// slash-separated, relative to the shared root, without leading or trailing
// slashes. The empty string is the root itself.
//
// Traversal is rejected outright instead of being resolved: "a/../b" is an
// error, not "b". That keeps the permission check and the file access looking
// at exactly the same string.
func CleanRel(raw string) (string, error) {
	if len(raw) > maxPathBytes || !utf8.ValidString(raw) {
		return "", ErrInvalidPath
	}
	trimmed := strings.Trim(raw, "/")
	if trimmed == "" {
		// "", "/" and "//" all mean the root; anything else made only of
		// slashes was already reduced to it.
		return "", nil
	}
	parts := strings.Split(trimmed, "/")
	for _, part := range parts {
		if ValidName(part) != nil {
			return "", ErrInvalidPath
		}
	}
	if strings.EqualFold(parts[0], InternalDir) {
		return "", ErrInvalidPath
	}
	return strings.Join(parts, "/"), nil
}

// joinRel appends a validated name to a canonical path.
func joinRel(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

// parentRel returns the canonical parent of rel ("" for top-level entries).
func parentRel(rel string) string {
	if i := strings.LastIndexByte(rel, '/'); i >= 0 {
		return rel[:i]
	}
	return ""
}

// baseRel returns the last component of rel.
func baseRel(rel string) string {
	if i := strings.LastIndexByte(rel, '/'); i >= 0 {
		return rel[i+1:]
	}
	return rel
}

// isUnder reports whether rel equals ancestor or lives below it. Both must be
// canonical. It compares whole components, so "ab" is not under "a".
func isUnder(rel, ancestor string) bool {
	if ancestor == "" || rel == ancestor {
		return true
	}
	return strings.HasPrefix(rel, ancestor+"/")
}

// osName maps the canonical root ("") to the name os.Root expects.
func osName(rel string) string {
	if rel == "" {
		return "."
	}
	return rel
}
