package sharedspace

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"necipdrive/internal/auth"
	"necipdrive/internal/domain"
)

const testSecret = "test-secret-with-enough-entropy-0123456789"

// seedBytes is the total size of the files newTestEnv creates.
const seedBytes = int64(len("film-icerigi") + len("derin") + len("cok-gizli") + len("rapor") + len("kokteki-dosya"))

// testEnv is a shared space on a temp directory with a few users:
// admin, reader (read on Filmler), writer (write on Filmler), stranger (no
// grant) and member (in a group with read on Belgeler).
type testEnv struct {
	t       *testing.T
	root    string // parent of the shared folder; holds files that must stay private
	dir     string
	store   *memStore
	service *Service
	handler *Handler

	clockMu sync.Mutex
	now     time.Time

	diskMu    sync.Mutex
	diskTotal int64
	diskFree  int64
}

type envOption func(*Options)

func newTestEnv(t *testing.T, options ...envOption) *testEnv {
	t.Helper()
	root := t.TempDir()
	env := &testEnv{
		t:         t,
		root:      root,
		dir:       filepath.Join(root, "paylasim"),
		store:     newMemStore(),
		now:       time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC),
		diskTotal: 100 << 30,
		diskFree:  50 << 30,
	}
	opts := Options{
		Dir:            env.dir,
		WarnPercent:    90,
		MaxUploadBytes: 64 << 20,
		LinkMaxTTL:     30 * 24 * time.Hour,
		Secret:         testSecret,
		Now:            env.clock,
		DiskSpace: func(string) (int64, int64, error) {
			env.diskMu.Lock()
			defer env.diskMu.Unlock()
			return env.diskTotal, env.diskFree, nil
		},
	}
	for _, option := range options {
		option(&opts)
	}
	service, err := New(env.store, opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	env.service = service
	env.handler = NewHandler(service, nil)

	for _, user := range []struct{ id, role string }{
		{"admin", "admin"}, {"reader", "user"}, {"writer", "user"}, {"stranger", "user"}, {"member", "user"},
	} {
		env.store.addUser(user.id, user.role)
	}
	env.write("Filmler/film.mkv", "film-icerigi")
	env.write("Filmler/Alt/derin.txt", "derin")
	env.write("Gizli/sir.txt", "cok-gizli")
	env.write("Belgeler/rapor.pdf", "rapor")
	env.write("kok.txt", "kokteki-dosya")
	// A file next to the shared folder: reachable only by escaping it.
	if err := os.WriteFile(filepath.Join(root, "disarida.txt"), []byte("DISARIDAKI-SIR"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	admin := Actor{ID: "admin", Role: "admin"}
	must := func(_ Grant, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("grant: %v", err)
		}
	}
	must(service.PutGrant(ctx, admin, GrantInput{Path: "Filmler", SubjectType: SubjectUser, UserID: "reader", Permission: "read"}))
	must(service.PutGrant(ctx, admin, GrantInput{Path: "Filmler", SubjectType: SubjectUser, UserID: "writer", Permission: "write"}))
	group, err := service.CreateGroup(ctx, admin, "Muhasebe")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.AddGroupMember(ctx, admin, group.ID, "member"); err != nil {
		t.Fatal(err)
	}
	must(service.PutGrant(ctx, admin, GrantInput{Path: "Belgeler", SubjectType: SubjectGroup, GroupID: group.ID, Permission: "read"}))
	return env
}

func (e *testEnv) clock() time.Time {
	e.clockMu.Lock()
	defer e.clockMu.Unlock()
	return e.now
}

func (e *testEnv) advance(d time.Duration) {
	e.clockMu.Lock()
	e.now = e.now.Add(d)
	e.clockMu.Unlock()
}

func (e *testEnv) setDisk(total, free int64) {
	e.diskMu.Lock()
	e.diskTotal, e.diskFree = total, free
	e.diskMu.Unlock()
	e.service.InvalidateUsage()
}

func (e *testEnv) write(rel, content string) {
	e.t.Helper()
	full := filepath.Join(e.dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
	if e.service != nil {
		e.service.InvalidateUsage()
	}
}

func (e *testEnv) exists(rel string) bool {
	_, err := os.Lstat(filepath.Join(e.dir, filepath.FromSlash(rel)))
	return err == nil
}

func (e *testEnv) actor(id string) Actor {
	role, err := e.store.UserRole(context.Background(), id)
	if err != nil {
		e.t.Fatalf("unknown test user %q", id)
	}
	return Actor{ID: id, Role: role}
}

// do sends a request through the authenticated user API as the given user.
// The path and query are passed raw so tests control the exact encoding.
func (e *testEnv) do(user, method, target string, body io.Reader) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(method, target, body)
	if user != "" {
		actor := e.actor(user)
		req = req.WithContext(auth.WithUser(req.Context(), &domain.User{ID: actor.ID, Role: actor.Role}))
	}
	rec := httptest.NewRecorder()
	if strings.HasPrefix(req.URL.Path, AdminBasePath) {
		e.handler.ServeAdmin(rec, req)
	} else {
		e.handler.ServeHTTP(rec, req)
	}
	return rec
}

func (e *testEnv) doJSON(user, method, target string, payload any) *httptest.ResponseRecorder {
	e.t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		e.t.Fatal(err)
	}
	return e.do(user, method, target, bytes.NewReader(data))
}

// public sends an unauthenticated request to the public link surface.
func (e *testEnv) public(method, target string, body io.Reader, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(method, target, body)
	req.RemoteAddr = "203.0.113.7:4000"
	if body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	e.handler.ServePublic(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decoding %q: %v", rec.Body.String(), err)
	}
	return out
}

func entryNames(listing Listing) []string {
	names := make([]string, 0, len(listing.Entries))
	for _, entry := range listing.Entries {
		names = append(names, entry.Name)
	}
	return names
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, want, rec.Body.String())
	}
}
