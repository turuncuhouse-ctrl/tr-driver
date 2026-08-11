package webdavx

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"necipdrive/internal/auth"
	"necipdrive/internal/domain"
	"necipdrive/internal/files"

	"golang.org/x/net/webdav"
)

// Handler serves WebDAV backed by TR Driver storage (same files as web UI).
type Handler struct {
	auth  *auth.Service
	files *files.Service
	locks webdav.LockSystem
	authC sync.Map // basic auth cache: user -> *cachedAuth
}

type cachedAuth struct {
	user    *domain.User
	expires time.Time
}

func New(authService *auth.Service, fileService *files.Service) *Handler {
	return &Handler{
		auth:  authService,
		files: fileService,
		locks: webdav.NewMemLS(),
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	user, err := h.user(r)
	if err != nil || user == nil {
		w.Header().Set("WWW-Authenticate", `Basic realm="TR Driver"`)
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	fs := &driveFS{h: h, user: user}
	handler := &webdav.Handler{
		Prefix:     "/dav",
		FileSystem: fs,
		LockSystem: h.locks,
	}
	handler.ServeHTTP(w, r)
}

func (h *Handler) user(r *http.Request) (*domain.User, error) {
	if u, p, ok := r.BasicAuth(); ok {
		key := u + "\x00" + p
		if v, ok := h.authC.Load(key); ok {
			c := v.(*cachedAuth)
			if time.Now().Before(c.expires) {
				return c.user, nil
			}
			h.authC.Delete(key)
		}
		result, err := h.auth.BeginDeviceLogin(r.Context(), clientIP(r), u, p)
		if err != nil {
			return nil, err
		}
		if result.Requires2FA {
			return nil, errors.New("email 2FA is enabled; sign in via the web app and use the session, or disable 2FA for WebDAV password login")
		}
		h.authC.Store(key, &cachedAuth{user: result.User, expires: time.Now().Add(10 * time.Minute)})
		return result.User, nil
	}
	if c, err := r.Cookie("session_token"); err == nil && c.Value != "" {
		return h.auth.UserBySession(r.Context(), c.Value)
	}
	return nil, errors.New("unauthorized")
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.Split(xff, ",")[0])
	}
	return r.RemoteAddr
}

type driveFS struct {
	h    *Handler
	user *domain.User
}

func (d *driveFS) Mkdir(ctx context.Context, name string, _ os.FileMode) error {
	parent, base, err := d.resolveParent(ctx, name)
	if err != nil {
		return err
	}
	_, err = d.h.files.CreateFolder(ctx, d.user.ID, d.user.Role, parent, base, "")
	return err
}

func (d *driveFS) OpenFile(ctx context.Context, name string, flag int, _ os.FileMode) (webdav.File, error) {
	name = clean(name)
	if name == "/" || name == "" {
		root := d.user.StorageRootID
		return &davDir{d: d, id: root, name: "/", path: "/"}, nil
	}
	entry, err := d.lookup(ctx, name)
	creating := flag&(os.O_CREATE|os.O_WRONLY|os.O_RDWR) != 0
	if err != nil {
		if !creating {
			return nil, os.ErrNotExist
		}
		parent, base, err2 := d.resolveParent(ctx, name)
		if err2 != nil {
			return nil, err2
		}
		return &davFile{d: d, parentID: parent, name: base, path: name, writing: true, create: true}, nil
	}
	if entry.Kind == "folder" {
		return &davDir{d: d, id: entry.ID, name: entry.Name, path: name}, nil
	}
	if flag&os.O_WRONLY != 0 || flag&os.O_RDWR != 0 || flag&os.O_TRUNC != 0 {
		var buf []byte
		dirty := flag&os.O_TRUNC != 0
		if flag&os.O_TRUNC == 0 {
			if rc, err := d.h.files.OpenStorage(entry.StorageKey); err == nil {
				buf, _ = io.ReadAll(rc)
				_ = rc.Close()
			}
		}
		return &davFile{d: d, entry: entry, path: name, writing: true, create: false, buf: buf, dirty: dirty}, nil
	}
	rc, err := d.h.files.OpenStorage(entry.StorageKey)
	if err != nil {
		return nil, err
	}
	return &davFile{d: d, entry: entry, path: name, reader: rc}, nil
}

func (d *driveFS) RemoveAll(ctx context.Context, name string) error {
	entry, err := d.lookup(ctx, name)
	if err != nil {
		return err
	}
	return d.h.files.Delete(ctx, d.user.ID, d.user.Role, entry.ID, "")
}

func (d *driveFS) Rename(ctx context.Context, oldName, newName string) error {
	entry, err := d.lookup(ctx, oldName)
	if err != nil {
		return err
	}
	newParent, newBase, err := d.resolveParent(ctx, newName)
	if err != nil {
		return err
	}
	if newBase == "" {
		return os.ErrInvalid
	}

	oldParent := d.user.StorageRootID
	if entry.ParentID != nil && *entry.ParentID != "" {
		oldParent = *entry.ParentID
	}

	// Cross-folder move (Windows Explorer drag into another folder).
	if oldParent != newParent {
		if err := d.h.files.Move(ctx, d.user.ID, d.user.Role, entry.ID, newParent, ""); err != nil {
			return err
		}
	}
	// Rename within (or after) move when the basename changes.
	if !strings.EqualFold(entry.Name, newBase) {
		if err := d.h.files.Rename(ctx, d.user.ID, d.user.Role, entry.ID, newBase, ""); err != nil {
			return err
		}
	}
	return nil
}

func (d *driveFS) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	name = clean(name)
	if name == "/" || name == "" {
		return &davInfo{name: "/", dir: true, mod: time.Now()}, nil
	}
	entry, err := d.lookup(ctx, name)
	if err != nil {
		return nil, os.ErrNotExist
	}
	return infoFromEntry(entry), nil
}

func (d *driveFS) lookup(ctx context.Context, name string) (*domain.FileEntry, error) {
	name = clean(name)
	parts := split(name)
	cur := d.user.StorageRootID
	var entry *domain.FileEntry
	for _, part := range parts {
		items, err := d.h.files.List(ctx, d.user.ID, d.user.Role, cur)
		if err != nil {
			return nil, err
		}
		found := false
		for i := range items {
			if strings.EqualFold(items[i].Name, part) {
				entry = &items[i]
				cur = entry.ID
				found = true
				break
			}
		}
		if !found {
			return nil, os.ErrNotExist
		}
	}
	if entry == nil {
		return nil, os.ErrNotExist
	}
	return entry, nil
}

func (d *driveFS) resolveParent(ctx context.Context, name string) (parentID, base string, err error) {
	name = clean(name)
	dir, base := path.Split(name)
	dir = clean(dir)
	if base == "" || base == "." || base == ".." {
		return "", "", os.ErrInvalid
	}
	if dir == "/" || dir == "" {
		return d.user.StorageRootID, base, nil
	}
	parent, err := d.lookup(ctx, dir)
	if err != nil {
		return "", "", err
	}
	if parent.Kind != "folder" {
		return "", "", os.ErrInvalid
	}
	return parent.ID, base, nil
}

func clean(p string) string {
	p = path.Clean("/" + strings.TrimPrefix(p, "/"))
	if p == "." {
		return "/"
	}
	return p
}

func split(p string) []string {
	p = strings.Trim(clean(p), "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

type davInfo struct {
	name string
	size int64
	dir  bool
	mod  time.Time
}

func (i *davInfo) Name() string       { return i.name }
func (i *davInfo) Size() int64        { return i.size }
func (i *davInfo) Mode() os.FileMode {
	if i.dir {
		return os.ModeDir | 0755
	}
	return 0644
}
func (i *davInfo) ModTime() time.Time { return i.mod }
func (i *davInfo) IsDir() bool        { return i.dir }
func (i *davInfo) Sys() any           { return nil }

func infoFromEntry(e *domain.FileEntry) os.FileInfo {
	return &davInfo{name: e.Name, size: e.SizeBytes, dir: e.Kind == "folder", mod: e.UpdatedAt}
}

type davDir struct {
	d    *driveFS
	id   string
	name string
	path string
	pos  int
	list []domain.FileEntry
}

func (f *davDir) Close() error                                 { return nil }
func (f *davDir) Read([]byte) (int, error)                     { return 0, os.ErrInvalid }
func (f *davDir) Write([]byte) (int, error)                    { return 0, os.ErrInvalid }
func (f *davDir) Seek(int64, int) (int64, error)               { return 0, os.ErrInvalid }
func (f *davDir) Stat() (os.FileInfo, error) {
	return &davInfo{name: f.name, dir: true, mod: time.Now()}, nil
}
func (f *davDir) Readdir(count int) ([]os.FileInfo, error) {
	if f.list == nil {
		items, err := f.d.h.files.List(context.Background(), f.d.user.ID, f.d.user.Role, f.id)
		if err != nil {
			return nil, err
		}
		f.list = items
	}
	if f.pos >= len(f.list) {
		if count > 0 {
			return nil, io.EOF
		}
		return nil, nil
	}
	end := len(f.list)
	if count > 0 && f.pos+count < end {
		end = f.pos + count
	}
	out := make([]os.FileInfo, 0, end-f.pos)
	for _, item := range f.list[f.pos:end] {
		e := item
		out = append(out, infoFromEntry(&e))
	}
	f.pos = end
	return out, nil
}

type davFile struct {
	d        *driveFS
	entry    *domain.FileEntry
	parentID string
	name     string
	path     string
	reader   io.ReadCloser
	writing  bool
	create   bool
	buf      []byte
	offset   int64
	dirty    bool
}

func (f *davFile) Close() error {
	if f.reader != nil {
		_ = f.reader.Close()
	}
	if f.writing && f.dirty {
		return f.d.h.files.UploadBytes(context.Background(), f.d.user.ID, f.d.user.Role, f.parentIDForWrite(), f.name, "application/octet-stream", f.buf, "")
	}
	// Empty create (0-byte file) still needs a DB row for Windows "New → Text Document".
	if f.writing && f.create && len(f.buf) == 0 {
		return f.d.h.files.UploadBytes(context.Background(), f.d.user.ID, f.d.user.Role, f.parentIDForWrite(), f.name, "application/octet-stream", []byte{}, "")
	}
	return nil
}
func (f *davFile) parentIDForWrite() string {
	if f.entry != nil && f.entry.ParentID != nil {
		return *f.entry.ParentID
	}
	return f.parentID
}
func (f *davFile) Read(p []byte) (int, error) {
	if f.reader == nil {
		return 0, io.EOF
	}
	return f.reader.Read(p)
}
func (f *davFile) Write(p []byte) (int, error) {
	if !f.writing {
		return 0, os.ErrInvalid
	}
	end := int(f.offset) + len(p)
	if end > len(f.buf) {
		nb := make([]byte, end)
		copy(nb, f.buf)
		f.buf = nb
	}
	copy(f.buf[int(f.offset):], p)
	f.offset += int64(len(p))
	f.dirty = true
	return len(p), nil
}
func (f *davFile) Seek(offset int64, whence int) (int64, error) {
	var next int64
	switch whence {
	case io.SeekStart:
		next = offset
	case io.SeekCurrent:
		next = f.offset + offset
	case io.SeekEnd:
		next = int64(len(f.buf)) + offset
	default:
		return 0, os.ErrInvalid
	}
	if next < 0 {
		return 0, os.ErrInvalid
	}
	f.offset = next
	return next, nil
}
func (f *davFile) Stat() (os.FileInfo, error) {
	if f.entry != nil && !f.dirty {
		return infoFromEntry(f.entry), nil
	}
	return &davInfo{name: f.name, size: int64(len(f.buf)), mod: time.Now()}, nil
}
func (f *davFile) Readdir(int) ([]os.FileInfo, error) { return nil, os.ErrInvalid }
