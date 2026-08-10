package storage

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type Local struct {
	root string
}

func NewLocal(root string) (*Local, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("create storage root: %w", err)
	}
	return &Local{root: root}, nil
}

func (l *Local) resolve(key string) (string, error) {
	clean := filepath.Clean(strings.ReplaceAll(key, "\\", "/"))
	if clean == "." || strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
		return "", fmt.Errorf("invalid storage key")
	}
	full := filepath.Join(l.root, filepath.FromSlash(clean))
	rel, err := filepath.Rel(l.root, full)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("storage key escapes root")
	}
	return full, nil
}

func (l *Local) Save(ctx context.Context, key string, src io.Reader) (int64, error) {
	path, err := l.resolve(key)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return 0, err
	}
	file, err := os.Create(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()

	written, err := copyWithContext(ctx, file, src)
	if err != nil {
		_ = os.Remove(path)
		return written, err
	}
	return written, nil
}

func (l *Local) Open(key string) (*os.File, error) {
	path, err := l.resolve(key)
	if err != nil {
		return nil, err
	}
	return os.Open(path)
}

func (l *Local) Delete(key string) error {
	path, err := l.resolve(key)
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (l *Local) Size(key string) (int64, error) {
	path, err := l.resolve(key)
	if err != nil {
		return 0, err
	}
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func (l *Local) EnsureEmpty(key string) error {
	path, err := l.resolve(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	return file.Close()
}

func (l *Local) Truncate(key string, size int64) error {
	path, err := l.resolve(key)
	if err != nil {
		return err
	}
	if size == 0 {
		return l.EnsureEmpty(key)
	}
	return os.Truncate(path, size)
}

func (l *Local) AppendAt(ctx context.Context, key string, offset int64, src io.Reader) (int64, error) {
	path, err := l.resolve(key)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return 0, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return 0, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return 0, err
	}
	if info.Size() != offset {
		if info.Size() > offset {
			if err := file.Truncate(offset); err != nil {
				return 0, err
			}
		} else if info.Size() < offset {
			return 0, fmt.Errorf("unexpected file offset")
		}
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return 0, err
	}
	return copyWithContext(ctx, file, src)
}

func (l *Local) Finalize(tempKey, finalKey string) error {
	src, err := l.resolve(tempKey)
	if err != nil {
		return err
	}
	dst, err := l.resolve(finalKey)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err != nil {
		in, openErr := os.Open(src)
		if openErr != nil {
			return err
		}
		defer in.Close()
		out, createErr := os.Create(dst)
		if createErr != nil {
			return createErr
		}
		defer out.Close()
		if _, copyErr := io.Copy(out, in); copyErr != nil {
			_ = os.Remove(dst)
			return copyErr
		}
		_ = os.Remove(src)
	}
	return nil
}

func copyWithContext(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	buf := make([]byte, 32*1024)
	var total int64
	for {
		select {
		case <-ctx.Done():
			return total, ctx.Err()
		default:
		}
		nr, er := src.Read(buf)
		if nr > 0 {
			nw, ew := dst.Write(buf[:nr])
			total += int64(nw)
			if ew != nil {
				return total, ew
			}
			if nw != nr {
				return total, io.ErrShortWrite
			}
		}
		if er != nil {
			if er == io.EOF {
				return total, nil
			}
			return total, er
		}
	}
}
