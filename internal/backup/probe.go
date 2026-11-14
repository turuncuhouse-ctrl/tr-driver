package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// skippedTopEntries are never chosen as the verification probe and are also
// excluded from restic backups (transient / filesystem-internal data).
var skippedTopEntries = map[string]bool{
	"tmp":        true,
	"lost+found": true,
}

// PickProbe selects a small real file under dataDir whose restorability is
// verified after every backup (restore from snapshot → compare SHA-256).
// It returns the slash-separated path relative to dataDir and its hex digest.
// ErrNoProbe is returned when the tree holds no suitable file (e.g. a fresh
// install); callers treat that as "skip the probe step", not a failure.
func PickProbe(dataDir string) (relPath, sha256hex string, err error) {
	const maxEntries = 5000
	var best string
	var bestSize int64 = -1
	entries := 0
	walkErr := filepath.WalkDir(dataDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable subtree: skip, restic will report it
		}
		entries++
		if entries > maxEntries {
			return filepath.SkipAll
		}
		rel, relErr := filepath.Rel(dataDir, path)
		if relErr != nil || rel == "." {
			return nil
		}
		top := rel
		if i := strings.IndexByte(rel, filepath.Separator); i >= 0 {
			top = rel[:i]
		}
		if skippedTopEntries[top] {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil || info.Size() <= 0 || info.Size() > 64*1024*1024 {
			return nil
		}
		if bestSize < 0 || info.Size() < bestSize {
			bestSize = info.Size()
			best = path
		}
		return nil
	})
	if walkErr != nil {
		return "", "", walkErr
	}
	if best == "" {
		return "", "", ErrNoProbe
	}
	rel, err := filepath.Rel(dataDir, best)
	if err != nil {
		return "", "", err
	}
	digest, err := HashFile(best)
	if err != nil {
		return "", "", err
	}
	return filepath.ToSlash(rel), digest, nil
}

// ErrNoProbe signals that no probe file exists yet.
var ErrNoProbe = fmt.Errorf("no probe file found")

// HashFile returns the hex SHA-256 of a file.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// FindSingleFile returns the one regular file under root. It is used after a
// single-path restic restore, where the layout under the target dir depends on
// how restic stored the original path (absolute vs relative).
func FindSingleFile(root string) (string, error) {
	var found string
	count := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		count++
		found = path
		return nil
	})
	if err != nil {
		return "", err
	}
	if count != 1 {
		return "", fmt.Errorf("expected 1 restored file, found %d under %s", count, root)
	}
	return found, nil
}
