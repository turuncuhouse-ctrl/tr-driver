package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/jackc/pgx/v5/pgxpool"

	"necipdrive/internal/auth"
	"necipdrive/internal/config"
	"necipdrive/internal/domain"
	"necipdrive/internal/sharedspace"
	"necipdrive/internal/torrents"
)

// resetSharedState clears the server-wide shared-space tables, which other
// runs against the same test database would otherwise see.
func resetSharedState(t *testing.T, db *pgxpool.Pool) {
	t.Helper()
	for _, statement := range []string{
		`delete from shared_space_links`,
		`delete from shared_space_grants`,
		`delete from user_groups`,
		`delete from torrents`,
		`delete from app_settings where key like 'shared_quota%' or key like 'torrent_%'`,
	} {
		if _, err := db.Exec(context.Background(), statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
}

func sharedUser(t *testing.T, db *pgxpool.Pool, cfg config.Config, role string) *domain.User {
	t.Helper()
	_, email := registerUser(t, db, cfg)
	var id string
	if err := db.QueryRow(context.Background(), `update users set role = $2 where email = $1 returning id::text`, email, role).Scan(&id); err != nil {
		t.Fatalf("set role: %v", err)
	}
	return &domain.User{ID: id, Role: role, Email: email}
}

type sharedAPI struct {
	t       *testing.T
	handler *sharedspace.Handler
}

func (a sharedAPI) call(user *domain.User, method, target string, body any) *httptest.ResponseRecorder {
	a.t.Helper()
	var reader io.Reader
	switch value := body.(type) {
	case nil:
	case string:
		reader = strings.NewReader(value)
	default:
		data, err := json.Marshal(value)
		if err != nil {
			a.t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	}
	req := httptest.NewRequest(method, target, reader)
	req.RemoteAddr = "203.0.113.9:5000"
	rec := httptest.NewRecorder()
	switch {
	case strings.HasPrefix(req.URL.Path, sharedspace.PublicPrefix):
		a.handler.ServePublic(rec, req)
	case strings.HasPrefix(req.URL.Path, sharedspace.AdminBasePath):
		a.handler.ServeAdmin(rec, req.WithContext(auth.WithUser(req.Context(), user)))
	default:
		a.handler.ServeHTTP(rec, req.WithContext(auth.WithUser(req.Context(), user)))
	}
	return rec
}

func (a sharedAPI) want(rec *httptest.ResponseRecorder, status int, what string) {
	a.t.Helper()
	if rec.Code != status {
		a.t.Fatalf("%s: status %d, want %d; body: %s", what, rec.Code, status, rec.Body)
	}
}

func decodeBody[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(rec.Body.Bytes(), &value); err != nil {
		t.Fatalf("decode %s: %v", rec.Body, err)
	}
	return value
}

// The shared space end to end against PostgreSQL: grants, groups, the
// rename/delete bookkeeping, public links and quota notifications.
func TestSharedSpaceWithPostgres(t *testing.T) {
	db, cfg := testDB(t)
	resetSharedState(t, db)
	t.Cleanup(func() { resetSharedState(t, db) })
	ctx := context.Background()

	admin := sharedUser(t, db, cfg, "admin")
	reader := sharedUser(t, db, cfg, "user")
	writer := sharedUser(t, db, cfg, "user")
	member := sharedUser(t, db, cfg, "user")
	stranger := sharedUser(t, db, cfg, "user")

	dir := filepath.Join(t.TempDir(), "paylasim")
	diskFree := int64(50 << 30)
	service, err := sharedspace.New(sharedspace.NewPostgresStore(db), sharedspace.Options{
		Dir:            dir,
		WarnPercent:    90,
		MaxUploadBytes: 1 << 20,
		LinkMaxTTL:     30 * 24 * time.Hour,
		Secret:         cfg.SessionSecret,
		DiskSpace:      func(string) (int64, int64, error) { return 100 << 30, diskFree, nil },
	})
	if err != nil {
		t.Fatalf("shared space: %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	api := sharedAPI{t: t, handler: sharedspace.NewHandler(service, nil)}

	api.want(api.call(admin, http.MethodPost, sharedspace.BasePath+"folder", map[string]string{"path": "", "name": "Filmler"}), http.StatusCreated, "create folder")
	api.want(api.call(admin, http.MethodPost, sharedspace.BasePath+"folder", map[string]string{"path": "", "name": "Belgeler"}), http.StatusCreated, "create folder")
	api.want(api.call(admin, http.MethodPost, sharedspace.BasePath+"upload?path=Filmler&name=film.mkv", "film-icerigi"), http.StatusCreated, "upload")
	api.want(api.call(admin, http.MethodPost, sharedspace.BasePath+"upload?path=Belgeler&name=rapor.pdf", "rapor"), http.StatusCreated, "upload")

	// Default deny.
	api.want(api.call(reader, http.MethodGet, sharedspace.BasePath+"list?path=Filmler", nil), http.StatusForbidden, "list without a grant")

	// Grants: user, group, and replacing an existing grant instead of duplicating it.
	api.want(api.call(admin, http.MethodPost, sharedspace.AdminBasePath+"grants",
		sharedspace.GrantInput{Path: "Filmler", SubjectType: "user", UserID: reader.ID, Permission: "read"}), http.StatusOK, "grant reader")
	api.want(api.call(admin, http.MethodPost, sharedspace.AdminBasePath+"grants",
		sharedspace.GrantInput{Path: "Filmler", SubjectType: "user", UserID: writer.ID, Permission: "read"}), http.StatusOK, "grant writer (read)")
	api.want(api.call(admin, http.MethodPost, sharedspace.AdminBasePath+"grants",
		sharedspace.GrantInput{Path: "Filmler", SubjectType: "user", UserID: writer.ID, Permission: "write"}), http.StatusOK, "upgrade writer")
	groupRec := api.call(admin, http.MethodPost, sharedspace.AdminBasePath+"groups", map[string]string{"name": "Muhasebe"})
	api.want(groupRec, http.StatusCreated, "create group")
	group := decodeBody[sharedspace.Group](t, groupRec)
	api.want(api.call(admin, http.MethodPost, sharedspace.AdminBasePath+"groups", map[string]string{"name": "muhasebe"}), http.StatusConflict, "duplicate group name")
	api.want(api.call(admin, http.MethodPost, sharedspace.AdminBasePath+"groups/"+group.ID+"/members", map[string]string{"userId": member.ID}), http.StatusOK, "add member")
	api.want(api.call(admin, http.MethodPost, sharedspace.AdminBasePath+"groups/"+group.ID+"/members", map[string]string{"userId": member.ID}), http.StatusOK, "add member twice")
	api.want(api.call(admin, http.MethodPost, sharedspace.AdminBasePath+"grants",
		sharedspace.GrantInput{Path: "Belgeler", SubjectType: "group", GroupID: group.ID, Permission: "read"}), http.StatusOK, "grant group")
	api.want(api.call(admin, http.MethodPost, sharedspace.AdminBasePath+"grants",
		sharedspace.GrantInput{Path: "Filmler", SubjectType: "user", UserID: "00000000-0000-4000-8000-000000000000", Permission: "read"}), http.StatusNotFound, "grant to unknown user")

	grants := decodeBody[[]sharedspace.Grant](t, api.call(admin, http.MethodGet, sharedspace.AdminBasePath+"grants", nil))
	if len(grants) != 3 {
		t.Fatalf("grants = %d, want 3 (the upgrade must replace, not add): %+v", len(grants), grants)
	}
	for _, grant := range grants {
		if grant.UserID == writer.ID && grant.Permission != "write" {
			t.Fatalf("writer grant = %+v", grant)
		}
		if grant.UserID == reader.ID && grant.UserEmail != reader.Email {
			t.Fatalf("grant listing lacks the user's e-mail: %+v", grant)
		}
		if grant.GroupID == group.ID && grant.GroupName != "Muhasebe" {
			t.Fatalf("grant listing lacks the group name: %+v", grant)
		}
	}
	groups := decodeBody[[]sharedspace.Group](t, api.call(admin, http.MethodGet, sharedspace.AdminBasePath+"groups", nil))
	if len(groups) != 1 || len(groups[0].Members) != 1 || groups[0].Members[0].UserID != member.ID {
		t.Fatalf("groups = %+v", groups)
	}

	// Access as granted.
	api.want(api.call(reader, http.MethodGet, sharedspace.BasePath+"download?path=Filmler/film.mkv", nil), http.StatusOK, "reader download")
	api.want(api.call(reader, http.MethodPost, sharedspace.BasePath+"upload?path=Filmler&name=x.txt", "x"), http.StatusForbidden, "reader upload")
	api.want(api.call(writer, http.MethodPost, sharedspace.BasePath+"upload?path=Filmler&name=yeni.txt", "yeni"), http.StatusCreated, "writer upload")
	api.want(api.call(member, http.MethodGet, sharedspace.BasePath+"download?path=Belgeler/rapor.pdf", nil), http.StatusOK, "group member download")
	api.want(api.call(member, http.MethodGet, sharedspace.BasePath+"list?path=Filmler", nil), http.StatusForbidden, "group member elsewhere")
	api.want(api.call(stranger, http.MethodGet, sharedspace.BasePath+"download?path=Belgeler/rapor.pdf", nil), http.StatusForbidden, "stranger")
	// Admin endpoints are closed to everyone else.
	api.want(api.call(writer, http.MethodGet, sharedspace.AdminBasePath+"grants", nil), http.StatusForbidden, "non-admin grant list")

	// Public link: created by the writer, stored hashed, opened without a session.
	linkRec := api.call(writer, http.MethodPost, sharedspace.BasePath+"links", sharedspace.LinkInput{Path: "Filmler/film.mkv", Password: "Parola-123", ExpiresInHours: 24})
	api.want(linkRec, http.StatusCreated, "create link")
	link := decodeBody[sharedspace.Link](t, linkRec)
	api.want(api.call(reader, http.MethodPost, sharedspace.BasePath+"links", sharedspace.LinkInput{Path: "Filmler/film.mkv"}), http.StatusForbidden, "reader creates link")
	var storedHash, storedPassword string
	if err := db.QueryRow(ctx, `select token_hash, password_hash from shared_space_links where id = $1`, link.ID).Scan(&storedHash, &storedPassword); err != nil {
		t.Fatal(err)
	}
	if storedHash == link.Token || strings.Contains(storedHash, link.Token) || !strings.HasPrefix(storedPassword, "argon2id$") || strings.Contains(storedPassword, "Parola-123") {
		t.Fatalf("link secrets stored in the clear: token_hash=%q password_hash=%q", storedHash, storedPassword)
	}
	api.want(api.call(nil, http.MethodGet, sharedspace.PublicPrefix+link.Token+"/dl", nil), http.StatusUnauthorized, "locked link download")
	api.want(api.call(nil, http.MethodGet, sharedspace.PublicPrefix+"not-a-real-token", nil), http.StatusNotFound, "unknown token")
	if links := decodeBody[[]sharedspace.Link](t, api.call(writer, http.MethodGet, sharedspace.BasePath+"links", nil)); len(links) != 1 || links[0].Token != "" || !links[0].HasPassword {
		t.Fatalf("writer's links = %+v", links)
	}
	if links := decodeBody[[]sharedspace.Link](t, api.call(reader, http.MethodGet, sharedspace.BasePath+"links", nil)); len(links) != 0 {
		t.Fatalf("reader sees someone else's links: %+v", links)
	}

	// A second link with a download allowance, counted atomically in SQL.
	one := int64(1)
	limitedRec := api.call(writer, http.MethodPost, sharedspace.BasePath+"links", sharedspace.LinkInput{Path: "Filmler/yeni.txt", MaxDownloads: &one})
	api.want(limitedRec, http.StatusCreated, "create limited link")
	limited := decodeBody[sharedspace.Link](t, limitedRec)
	first := api.call(nil, http.MethodGet, sharedspace.PublicPrefix+limited.Token+"/dl", nil)
	api.want(first, http.StatusOK, "first public download")
	if first.Body.String() != "yeni" {
		t.Fatalf("public download body = %q", first.Body)
	}
	api.want(api.call(nil, http.MethodGet, sharedspace.PublicPrefix+limited.Token+"/dl", nil), http.StatusGone, "download past the allowance")

	// Renaming the folder carries grants and links along (substr arithmetic in SQL).
	api.want(api.call(admin, http.MethodPost, sharedspace.BasePath+"rename", map[string]string{"path": "Filmler", "name": "Arşiv"}), http.StatusOK, "rename folder")
	api.want(api.call(reader, http.MethodGet, sharedspace.BasePath+"download?path=Arşiv/film.mkv", nil), http.StatusOK, "reader after rename")
	api.want(api.call(reader, http.MethodGet, sharedspace.BasePath+"list?path=Filmler", nil), http.StatusForbidden, "old path after rename")
	var linkPath string
	if err := db.QueryRow(ctx, `select path from shared_space_links where id = $1`, link.ID).Scan(&linkPath); err != nil || linkPath != "Arşiv/film.mkv" {
		t.Fatalf("link path after rename = %q (%v)", linkPath, err)
	}

	// Taking away the creator's write access disables the link.
	for _, grant := range decodeBody[[]sharedspace.Grant](t, api.call(admin, http.MethodGet, sharedspace.AdminBasePath+"grants", nil)) {
		if grant.UserID == writer.ID {
			api.want(api.call(admin, http.MethodDelete, sharedspace.AdminBasePath+"grants/"+grant.ID, nil), http.StatusOK, "revoke writer grant")
		}
	}
	api.want(api.call(nil, http.MethodGet, sharedspace.PublicPrefix+link.Token, nil), http.StatusGone, "link after its creator lost access")

	// Deleting a folder drops its grants and links, so a new folder of the
	// same name starts closed.
	api.want(api.call(admin, http.MethodPost, sharedspace.BasePath+"delete", map[string]string{"path": "Arşiv"}), http.StatusOK, "delete folder")
	var grantsLeft, linksLeft int
	if err := db.QueryRow(ctx, `select (select count(*) from shared_space_grants where path = 'Arşiv' or path like 'Arşiv/%'),
		(select count(*) from shared_space_links where revoked_at is null and (path = 'Arşiv' or path like 'Arşiv/%'))`).Scan(&grantsLeft, &linksLeft); err != nil {
		t.Fatal(err)
	}
	if grantsLeft != 0 || linksLeft != 0 {
		t.Fatalf("after delete: %d grants, %d live links still point into the folder", grantsLeft, linksLeft)
	}
	api.want(api.call(admin, http.MethodPost, sharedspace.BasePath+"folder", map[string]string{"path": "", "name": "Arşiv"}), http.StatusCreated, "recreate folder")
	api.want(api.call(reader, http.MethodGet, sharedspace.BasePath+"list?path=Arşiv", nil), http.StatusForbidden, "old grant on a recreated folder")

	// Removing the member from the group, then deleting the group (cascade).
	api.want(api.call(admin, http.MethodDelete, sharedspace.AdminBasePath+"groups/"+group.ID+"/members?userId="+member.ID, nil), http.StatusOK, "remove member")
	api.want(api.call(member, http.MethodGet, sharedspace.BasePath+"download?path=Belgeler/rapor.pdf", nil), http.StatusForbidden, "former member")
	api.want(api.call(admin, http.MethodDelete, sharedspace.AdminBasePath+"groups/"+group.ID, nil), http.StatusOK, "delete group")
	if left := decodeBody[[]sharedspace.Grant](t, api.call(admin, http.MethodGet, sharedspace.AdminBasePath+"grants", nil)); len(left) != 0 {
		t.Fatalf("grants after deleting the group = %+v", left)
	}

	// Quota settings persist, and filling the space notifies administrators.
	api.want(api.call(admin, http.MethodPut, sharedspace.AdminBasePath+"settings", map[string]any{"quotaBytes": 1 << 30, "warnPercent": 80}), http.StatusOK, "save settings")
	var before int
	if err := db.QueryRow(ctx, `select count(*) from notifications where user_id = $1 and title like 'Ortak Paylaşım%'`, admin.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	diskFree = 1 << 20       // below the reserve: the disk is full
	for i := 0; i < 3; i++ { // the warning is raised once, not on every check
		if status, err := service.Quota(ctx); err != nil || status.Level != sharedspace.QuotaFull {
			t.Fatalf("quota = %+v, %v", status, err)
		}
	}
	var after int
	if err := db.QueryRow(ctx, `select count(*) from notifications where user_id = $1 and title like 'Ortak Paylaşım%'`, admin.ID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before+1 {
		t.Fatalf("admin notifications went from %d to %d, want exactly one more", before, after)
	}
	api.want(api.call(admin, http.MethodPost, sharedspace.BasePath+"upload?path=Belgeler&name=sigmaz.bin", "veri"), http.StatusInsufficientStorage, "upload to a full disk")
}

func TestTorrentsWithPostgres(t *testing.T) {
	db, cfg := testDB(t)
	resetSharedState(t, db)
	t.Cleanup(func() { resetSharedState(t, db) })
	ctx := context.Background()
	admin := sharedUser(t, db, cfg, "admin")
	actor := torrents.Actor{ID: admin.ID, Role: "admin"}

	dir := filepath.Join(t.TempDir(), "paylasim")
	space, err := sharedspace.New(sharedspace.NewPostgresStore(db), sharedspace.Options{
		Dir: dir, WarnPercent: 90, MaxUploadBytes: 1 << 20, LinkMaxTTL: time.Hour, Secret: cfg.SessionSecret,
		DiskSpace: func(string) (int64, int64, error) { return 100 << 30, 50 << 30, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = space.Close() })

	options := torrents.Options{Enabled: true, TickInterval: 50 * time.Millisecond, Configure: func(c *torrent.ClientConfig) {
		// Keep the test off the network.
		c.NoDHT, c.DisableTrackers, c.DisablePEX, c.DisableIPv6 = true, true, true, true
		c.ListenPort = 0
		c.ListenHost = func(string) string { return "127.0.0.1" }
	}}
	start := func() *torrents.Service {
		service := torrents.New(torrents.NewPostgresStore(db), space, options)
		if !service.Available() {
			t.Fatal("torrent client did not start")
		}
		if err := service.Start(ctx); err != nil {
			t.Fatalf("start: %v", err)
		}
		space.SetHooks(sharedspace.Hooks{Busy: service.Busy, PathChanged: service.PathChanged})
		return service
	}
	service := start()

	// A unique torrent: info hashes are unique in the table.
	payload := make([]byte, 40_000)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "Deneme")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "veri.bin"), payload, 0o644); err != nil {
		t.Fatal(err)
	}
	info := metainfo.Info{PieceLength: 16 << 10}
	if err := info.BuildFromFilePath(source); err != nil {
		t.Fatal(err)
	}
	var file bytes.Buffer
	if err := (&metainfo.MetaInfo{InfoBytes: bencode.MustMarshal(info)}).Write(&file); err != nil {
		t.Fatal(err)
	}

	view, err := service.AddFile(ctx, actor, file.Bytes())
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := service.AddFile(ctx, actor, file.Bytes()); !errors.Is(err, torrents.ErrDuplicate) {
		t.Fatalf("duplicate add: %v", err)
	}
	// The shared space refuses to touch the folder while the torrent owns it.
	if err := space.Delete(ctx, sharedspace.Actor{ID: admin.ID, Role: "admin"}, "Deneme"); !errors.Is(err, sharedspace.ErrBusy) {
		t.Fatalf("deleting a folder in use: %v, want ErrBusy", err)
	}
	if _, err := service.Pause(ctx, actor, view.ID); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if _, err := service.SetLimits(ctx, actor, torrents.Limits{DownloadLimitKBps: 512, UploadLimitKBps: 32}); err != nil {
		t.Fatalf("limits: %v", err)
	}
	var state, addedBy string
	var metaLen int
	if err := db.QueryRow(ctx, `select state, added_by_user_id::text, length(metainfo) from torrents where id = $1`, view.ID).Scan(&state, &addedBy, &metaLen); err != nil {
		t.Fatal(err)
	}
	if state != "paused" || addedBy != admin.ID || metaLen != file.Len() {
		t.Fatalf("stored torrent: state=%q added_by=%q metainfo=%d bytes", state, addedBy, metaLen)
	}
	service.Close()

	// Restart: the list, the paused state and the limits come back.
	service = start()
	t.Cleanup(service.Close)
	overview, err := service.Overview(ctx, actor)
	if err != nil {
		t.Fatal(err)
	}
	if len(overview.Torrents) != 1 || overview.Torrents[0].Status != torrents.StatusPaused || overview.Torrents[0].Name != "Deneme" ||
		overview.Limits != (torrents.Limits{DownloadLimitKBps: 512, UploadLimitKBps: 32}) {
		t.Fatalf("overview after restart = %+v", overview)
	}
	if _, err := service.Overview(ctx, torrents.Actor{ID: admin.ID, Role: "user"}); !errors.Is(err, torrents.ErrForbidden) {
		t.Fatalf("non-admin overview: %v", err)
	}
	if err := service.Remove(ctx, actor, view.ID, true); err != nil {
		t.Fatalf("remove: %v", err)
	}
	var left int
	if err := db.QueryRow(ctx, `select count(*) from torrents`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("torrents left = %d (%v)", left, err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) > 1 { // only the internal staging directory may remain
		t.Fatalf("data left in the shared folder: %v", entries)
	}
}
