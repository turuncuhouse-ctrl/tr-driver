package sharedspace

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

var (
	ErrNotFound = errors.New("bulunamadı")
	ErrExists   = errors.New("bu adda bir öğe zaten var")
	ErrNotDir   = errors.New("klasör değil")
	ErrIsDir    = errors.New("bu bir klasör")
)

// tree is the on-disk shared folder. Every access is funnelled through an
// os.Root, which the kernel-assisted implementation keeps from resolving
// outside the directory even when a symlink or a ".." slips into a name.
type tree struct {
	dir  string // absolute path of the shared folder
	root *os.Root
}

func openTree(dir string) (*tree, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(abs)
	if err != nil {
		return nil, err
	}
	t := &tree{dir: abs, root: root}
	tmp := filepath.Join(abs, InternalDir, "tmp")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		root.Close()
		return nil, err
	}
	// Uploads interrupted by a restart leave temp files behind; nothing can
	// resume them, so they are dropped at startup.
	if entries, err := os.ReadDir(tmp); err == nil {
		for _, entry := range entries {
			_ = os.Remove(filepath.Join(tmp, entry.Name()))
		}
	}
	return t, nil
}

func (t *tree) close() error { return t.root.Close() }

func mapFSError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, fs.ErrNotExist):
		return ErrNotFound
	case errors.Is(err, fs.ErrExist):
		return ErrExists
	default:
		return err
	}
}

// lstat resolves rel one component at a time and refuses symbolic links at
// every level, including the last one. os.Root already prevents escaping the
// shared folder; this additionally keeps a link from aliasing one part of the
// folder into another, which would let a narrow grant or a public link reach
// content it was never issued for.
func (t *tree) lstat(rel string) (os.FileInfo, error) {
	if rel == "" {
		info, err := t.root.Stat(".")
		return info, mapFSError(err)
	}
	parts := strings.Split(rel, "/")
	var info os.FileInfo
	for i := range parts {
		var err error
		info, err = t.root.Lstat(strings.Join(parts[:i+1], "/"))
		if err != nil {
			// ENOTDIR (a file used as a directory) is "not found" to callers.
			if errors.Is(err, fs.ErrNotExist) || !errors.Is(err, fs.ErrPermission) {
				return nil, ErrNotFound
			}
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, ErrNotFound
		}
		if i < len(parts)-1 && !info.IsDir() {
			return nil, ErrNotFound
		}
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		// Devices, sockets and pipes are never served.
		return nil, ErrNotFound
	}
	return info, nil
}

// open returns a read handle on a regular file.
func (t *tree) open(rel string) (*os.File, os.FileInfo, error) {
	info, err := t.lstat(rel)
	if err != nil {
		return nil, nil, err
	}
	if info.IsDir() {
		return nil, nil, ErrIsDir
	}
	file, err := t.root.Open(rel)
	if err != nil {
		return nil, nil, mapFSError(err)
	}
	// Re-stat the open handle: the size served must match what is streamed.
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() {
		file.Close()
		return nil, nil, ErrNotFound
	}
	return file, opened, nil
}

// readDir lists the regular files and directories directly inside rel.
func (t *tree) readDir(rel string) ([]os.FileInfo, error) {
	info, err := t.lstat(rel)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, ErrNotDir
	}
	dir, err := t.root.Open(osName(rel))
	if err != nil {
		return nil, mapFSError(err)
	}
	defer dir.Close()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	out := make([]os.FileInfo, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if ValidName(name) != nil {
			continue // unaddressable through the API
		}
		if rel == "" && strings.EqualFold(name, InternalDir) {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		item, err := entry.Info()
		if err != nil {
			continue // vanished between readdir and stat
		}
		if !item.IsDir() && !item.Mode().IsRegular() {
			continue
		}
		out = append(out, item)
	}
	return out, nil
}

func (t *tree) mkdir(rel string) error {
	if _, err := t.lstat(parentRel(rel)); err != nil {
		return err
	}
	return mapFSError(t.root.Mkdir(rel, 0o755))
}

// createTemp opens a new upload staging file inside the internal directory.
func (t *tree) createTemp() (*os.File, string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return nil, "", err
	}
	name := InternalDir + "/tmp/" + hex.EncodeToString(buf)
	file, err := t.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, "", err
	}
	return file, name, nil
}

func (t *tree) removeTemp(name string) { _ = t.root.Remove(name) }

// osPath is only used for rename, which os.Root does not offer on the Go
// version this module targets. Callers must have validated both ends with
// lstat first, so no component of the path is a symbolic link.
func (t *tree) osPath(rel string) string {
	return filepath.Join(t.dir, filepath.FromSlash(rel))
}

// commit moves a finished upload into place.
func (t *tree) commit(tempName, rel string, overwrite bool) error {
	parent, err := t.lstat(parentRel(rel))
	if err != nil {
		return err
	}
	if !parent.IsDir() {
		return ErrNotDir
	}
	existing, err := t.root.Lstat(rel)
	switch {
	case err == nil:
		if !overwrite || !existing.Mode().IsRegular() {
			return ErrExists
		}
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	return os.Rename(t.osPath(tempName), t.osPath(rel))
}

func (t *tree) rename(oldRel, newRel string) error {
	if _, err := t.lstat(oldRel); err != nil {
		return err
	}
	parent, err := t.lstat(parentRel(newRel))
	if err != nil {
		return err
	}
	if !parent.IsDir() {
		return ErrNotDir
	}
	if _, err := t.root.Lstat(newRel); err == nil {
		return ErrExists
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return os.Rename(t.osPath(oldRel), t.osPath(newRel))
}

// removeAll deletes rel recursively without ever following a symbolic link:
// a link is removed as a link, and its target is left alone.
func (t *tree) removeAll(rel string) error {
	if rel == "" {
		return ErrInvalidPath
	}
	info, err := t.root.Lstat(rel)
	if err != nil {
		return mapFSError(err)
	}
	if info.IsDir() {
		dir, err := t.root.Open(rel)
		if err != nil {
			return mapFSError(err)
		}
		names, err := dir.Readdirnames(-1)
		dir.Close()
		if err != nil {
			return err
		}
		for _, name := range names {
			if err := t.removeAll(rel + "/" + name); err != nil && !errors.Is(err, ErrNotFound) {
				return err
			}
		}
	}
	return mapFSError(t.root.Remove(rel))
}

// usage sums the logical size of every regular file in the shared folder.
// Partially downloaded torrent files count with their final size, which makes
// the figure a conservative reservation rather than an optimistic estimate.
func (t *tree) usage() (int64, error) {
	var total int64
	err := filepath.WalkDir(t.dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil // removed while walking
			}
			return err
		}
		if entry.IsDir() {
			if path != t.dir && filepath.Dir(path) == t.dir && strings.EqualFold(entry.Name(), InternalDir) {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		total += info.Size()
		return nil
	})
	return total, err
}
