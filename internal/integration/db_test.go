package integration

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"necipdrive/internal/access"
	"necipdrive/internal/auth"
	"necipdrive/internal/config"
	"necipdrive/internal/files"
	"necipdrive/internal/storage"
	"necipdrive/internal/store"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testDB(t *testing.T) (*pgxpool.Pool, config.Config) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	db, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(db.Close)
	cfg := config.Config{
		SessionSecret:       "integration-test-session-secret-123456",
		FreeQuotaBytes:      1 << 30,
		MaxUploadBytes:      1 << 20,
		MaxUploadBatchBytes: 1 << 20,
		UploadChunkBytes:    1 << 20,
		SessionTTL:          time.Hour,
		DeviceTokenTTL:      0,
	}
	if err := store.Migrate(ctx, db, cfg.FreeQuotaBytes, cfg.DeviceTokenTTL); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db, cfg
}

func registerUser(t *testing.T, db *pgxpool.Pool, cfg config.Config) (*auth.Service, string) {
	t.Helper()
	svc := auth.NewService(db, cfg, nil)
	email := fmt.Sprintf("integration-%s@example.com", uuid.NewString())
	if _, _, err := svc.Register(context.Background(), email, "integration-password-123", "Integration"); err != nil {
		t.Fatalf("register: %v", err)
	}
	return svc, email
}

func TestPersistentDeviceSessions(t *testing.T) {
	db, cfg := testDB(t)
	ctx := context.Background()
	svc, email := registerUser(t, db, cfg)
	result, err := svc.Login(ctx, "127.0.0.1", email, "integration-password-123")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	user, err := svc.UserBySession(ctx, result.SessionToken)
	if err != nil {
		t.Fatalf("session user: %v", err)
	}
	device, token, err := svc.CreateDevice(ctx, user.ID, "Android test")
	if err != nil {
		t.Fatalf("create device: %v", err)
	}
	var expiresAt *time.Time
	if err := db.QueryRow(ctx, `select expires_at from devices where id = $1::uuid`, device.ID).Scan(&expiresAt); err != nil {
		t.Fatalf("query device: %v", err)
	}
	if expiresAt != nil {
		t.Fatalf("persistent device should have null expiry, got %v", expiresAt)
	}
	if _, _, err := svc.UserByDeviceToken(ctx, token); err != nil {
		t.Fatalf("device token should authenticate: %v", err)
	}
	if err := svc.RevokeDevice(ctx, user.ID, device.ID); err != nil {
		t.Fatalf("revoke device: %v", err)
	}
	if _, _, err := svc.UserByDeviceToken(ctx, token); err == nil {
		t.Fatal("revoked device token should not authenticate")
	}

	// Legacy expiring devices are converted to persistent devices when TTL <= 0.
	legacyToken := uuid.NewString()
	if _, err := db.Exec(ctx, `
		insert into devices (user_id, name, token_hash, expires_at)
		values ($1::uuid, 'legacy', $2, now() - interval '1 day')`, user.ID, legacyToken); err != nil {
		t.Fatalf("insert legacy device: %v", err)
	}
	if err := store.Migrate(ctx, db, cfg.FreeQuotaBytes, 0); err != nil {
		t.Fatalf("remigrate: %v", err)
	}
	var legacyExpiry *time.Time
	if err := db.QueryRow(ctx, `select expires_at from devices where token_hash = $1`, legacyToken).Scan(&legacyExpiry); err != nil {
		t.Fatalf("query legacy device: %v", err)
	}
	if legacyExpiry != nil {
		t.Fatal("legacy expiry should be cleared in persistent mode")
	}
}

func TestRecursiveTrashRestorePurgeQuotaAndChangelog(t *testing.T) {
	db, cfg := testDB(t)
	ctx := context.Background()
	authSvc, email := registerUser(t, db, cfg)
	var userID, rootID, role string
	if err := db.QueryRow(ctx, `select id::text, storage_root_id::text, role from users where email = $1`, email).Scan(&userID, &rootID, &role); err != nil {
		t.Fatalf("query user: %v", err)
	}
	_ = authSvc
	dir := t.TempDir()
	storeLocal, err := storage.NewLocal(dir)
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	fileSvc := files.NewService(db, storeLocal, cfg, access.New(db), nil)
	folder, err := fileSvc.CreateFolder(ctx, userID, role, rootID, "trash-test", "")
	if err != nil {
		t.Fatalf("create folder: %v", err)
	}
	child, err := fileSvc.CreateFolder(ctx, userID, role, folder.ID, "child", "")
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	data := []byte("hello quota")
	if err := fileSvc.UploadBytes(ctx, userID, role, child.ID, "a.txt", "text/plain", data, ""); err != nil {
		t.Fatalf("upload: %v", err)
	}
	var fileID, storageKey string
	if err := db.QueryRow(ctx, `select id::text, storage_key from file_entries where parent_id = $1::uuid and name = 'a.txt'`, child.ID).Scan(&fileID, &storageKey); err != nil {
		t.Fatalf("query file: %v", err)
	}
	versionKey := filepath.Join("versions", uuid.NewString())
	if _, err := storeLocal.Save(ctx, versionKey, bytesReader(data)); err != nil {
		t.Fatalf("save version blob: %v", err)
	}
	if _, err := db.Exec(ctx, `
		insert into file_versions (entry_id, version, storage_key, size_bytes, mime_type)
		values ($1::uuid, 0, $2, $3, 'text/plain')`, fileID, versionKey, len(data)); err != nil {
		t.Fatalf("insert version: %v", err)
	}
	var used int64
	if err := db.QueryRow(ctx, `select used_bytes from users where id = $1::uuid`, userID).Scan(&used); err != nil {
		t.Fatalf("query usage: %v", err)
	}
	if used != int64(len(data)) {
		t.Fatalf("used_bytes = %d, want %d", used, len(data))
	}
	var driveChanges int
	if err := db.QueryRow(ctx, `select count(*) from file_changes where entry_id = $1::uuid and drive_id is not null`, fileID).Scan(&driveChanges); err != nil {
		t.Fatalf("query changelog: %v", err)
	}
	if driveChanges == 0 {
		t.Fatal("changelog should include drive_id")
	}
	if err := fileSvc.Delete(ctx, userID, role, folder.ID, ""); err != nil {
		t.Fatalf("delete folder: %v", err)
	}
	var deleted int
	if err := db.QueryRow(ctx, `select count(*) from file_entries where id = any($1::uuid[]) and deleted_at is not null`, []string{folder.ID, child.ID, fileID}).Scan(&deleted); err != nil {
		t.Fatalf("query trashed: %v", err)
	}
	if deleted != 3 {
		t.Fatalf("expected 3 trashed entries, got %d", deleted)
	}
	if err := fileSvc.Restore(ctx, userID, role, folder.ID, ""); err != nil {
		t.Fatalf("restore folder: %v", err)
	}
	if err := db.QueryRow(ctx, `select count(*) from file_entries where id = any($1::uuid[]) and deleted_at is null`, []string{folder.ID, child.ID, fileID}).Scan(&deleted); err != nil {
		t.Fatalf("query restored: %v", err)
	}
	if deleted != 3 {
		t.Fatalf("expected 3 restored entries, got %d", deleted)
	}
	if err := fileSvc.Delete(ctx, userID, role, folder.ID, ""); err != nil {
		t.Fatalf("delete again: %v", err)
	}
	if err := fileSvc.Purge(ctx, userID, folder.ID, ""); err != nil {
		t.Fatalf("purge: %v", err)
	}
	var remaining int
	if err := db.QueryRow(ctx, `select count(*) from file_entries where id = any($1::uuid[])`, []string{folder.ID, child.ID, fileID}).Scan(&remaining); err != nil {
		t.Fatalf("query purged: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("expected purged entries to be gone, got %d", remaining)
	}
	if err := db.QueryRow(ctx, `select used_bytes from users where id = $1::uuid`, userID).Scan(&used); err != nil {
		t.Fatalf("query usage after purge: %v", err)
	}
	if used != 0 {
		t.Fatalf("used_bytes after purge = %d, want 0", used)
	}
	if _, err := os.Stat(filepath.Join(dir, storageKey)); !os.IsNotExist(err) {
		t.Fatalf("current blob should be deleted, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, versionKey)); !os.IsNotExist(err) {
		t.Fatalf("version blob should be deleted, stat err=%v", err)
	}
}

func TestLegacyOTPChallengesRetired(t *testing.T) {
	db, cfg := testDB(t)
	ctx := context.Background()
	_, email := registerUser(t, db, cfg)
	var userID string
	if err := db.QueryRow(ctx, `select id::text from users where email = $1`, email).Scan(&userID); err != nil {
		t.Fatalf("query user: %v", err)
	}
	legacyID := uuid.NewString()
	keyedID := uuid.NewString()
	if _, err := db.Exec(ctx, `
		insert into auth_challenges (id, user_id, purpose, code_hash, token_hash, expires_at)
		values ($1::uuid, $2::uuid, 'login_2fa', 'legacy-unsalted', $3, now() + interval '10 minutes')`,
		legacyID, userID, uuid.NewString()); err != nil {
		t.Fatalf("insert legacy challenge: %v", err)
	}
	if _, err := db.Exec(ctx, `
		insert into auth_challenges (id, user_id, purpose, code_hash, token_hash, expires_at)
		values ($1::uuid, $2::uuid, 'login_2fa', 'hmac-sha256:abc', $3, now() + interval '10 minutes')`,
		keyedID, userID, uuid.NewString()); err != nil {
		t.Fatalf("insert keyed challenge: %v", err)
	}
	if err := store.Migrate(ctx, db, cfg.FreeQuotaBytes, 0); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	var consumed bool
	if err := db.QueryRow(ctx, `select consumed_at is not null from auth_challenges where id = $1::uuid`, legacyID).Scan(&consumed); err != nil {
		t.Fatalf("query legacy challenge: %v", err)
	}
	if !consumed {
		t.Fatal("legacy OTP challenge should be retired")
	}
	if err := db.QueryRow(ctx, `select consumed_at is not null from auth_challenges where id = $1::uuid`, keyedID).Scan(&consumed); err != nil {
		t.Fatalf("query keyed challenge: %v", err)
	}
	if consumed {
		t.Fatal("keyed OTP challenge should remain open")
	}
}

func bytesReader(data []byte) *bytes.Reader { return bytes.NewReader(data) }
