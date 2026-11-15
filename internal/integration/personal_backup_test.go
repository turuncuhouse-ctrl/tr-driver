package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"necipdrive/internal/auth"
	"necipdrive/internal/personalbackup"
)

// personalFixture registers one user and returns everything a case needs.
type personalFixture struct {
	userID string
	svc    *personalbackup.Service
	api    *auth.Service
}

func newPersonalFixture(t *testing.T, cfgSecret string) personalFixture {
	t.Helper()
	db, cfg := testDB(t)
	cfg.PersonalBackupKey = cfgSecret
	authSvc, email := registerUser(t, db, cfg)
	var userID string
	if err := db.QueryRow(context.Background(), `select id::text from users where email = $1`, email).Scan(&userID); err != nil {
		t.Fatalf("query user: %v", err)
	}
	vault, err := personalbackup.NewVault(cfg.PersonalBackupKey)
	if err != nil {
		t.Fatalf("vault: %v", err)
	}
	svc, err := personalbackup.NewService(db, vault)
	if err != nil {
		t.Fatalf("service: %v", err)
	}
	return personalFixture{userID: userID, svc: svc, api: authSvc}
}

func contactItem(t *testing.T, key, name, phone string, revision int64, hash string) personalbackup.Item {
	t.Helper()
	data := fmt.Sprintf(`{"displayName":%q,"phones":[{"type":"mobile","value":%q}]}`, name, phone)
	return personalbackup.Item{
		Key:      key,
		Revision: revision,
		Hash:     hash,
		Data:     json.RawMessage(data),
	}
}

func sha256HexOf(s string) string {
	// Content hashes are opaque to the server; any 64-char hex value works and
	// keeps this test independent of the client hashing format.
	return strings.Repeat("0", 60) + fmt.Sprintf("%04x", len(s))
}

func TestPersonalBackupIncrementalSync(t *testing.T) {
	db, cfg := testDB(t)
	ctx := context.Background()
	authSvc, email := registerUser(t, db, cfg)
	var userID string
	if err := db.QueryRow(ctx, `select id::text from users where email = $1`, email).Scan(&userID); err != nil {
		t.Fatalf("query user: %v", err)
	}
	vault, err := personalbackup.NewVault(cfg.SessionSecret)
	if err != nil {
		t.Fatalf("vault: %v", err)
	}
	svc, err := personalbackup.NewService(db, vault)
	if err != nil {
		t.Fatalf("service: %v", err)
	}
	_ = authSvc

	keyA := "contact-key-aaaaaaaaaaaa"
	keyB := "contact-key-bbbbbbbbbbbb"
	hashA := sha256HexOf("a")
	hashB := sha256HexOf("bb")

	first, err := svc.Sync(ctx, userID, personalbackup.KindContacts, &personalbackup.SyncRequest{
		DeviceID: "Pixel 7",
		Items: []personalbackup.Item{
			contactItem(t, keyA, "Ayşe Yılmaz", "+905551112233", 100, hashA),
			contactItem(t, keyB, "Mehmet Demir", "+905554445566", 100, hashB),
		},
	})
	if err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if first.Inserted != 2 || first.Total != 2 || first.Updated != 0 {
		t.Fatalf("unexpected first result: %+v", first)
	}

	// The same payload again must not rewrite rows.
	again, err := svc.Sync(ctx, userID, personalbackup.KindContacts, &personalbackup.SyncRequest{
		Items: []personalbackup.Item{
			contactItem(t, keyA, "Ayşe Yılmaz", "+905551112233", 100, hashA),
			contactItem(t, keyB, "Mehmet Demir", "+905554445566", 100, hashB),
		},
	})
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if again.Unchanged != 2 || again.Inserted != 0 || again.Updated != 0 {
		t.Fatalf("resync should be a no-op: %+v", again)
	}

	// A changed record (new revision, new hash) updates exactly one row.
	newHash := sha256HexOf("changed")
	changed, err := svc.Sync(ctx, userID, personalbackup.KindContacts, &personalbackup.SyncRequest{
		Items: []personalbackup.Item{
			contactItem(t, keyA, "Ayşe Yılmaz", "+905551112233", 200, newHash),
			contactItem(t, keyB, "Mehmet Demir", "+905554445566", 100, hashB),
		},
	})
	if err != nil {
		t.Fatalf("change sync: %v", err)
	}
	if changed.Updated != 1 || changed.Unchanged != 1 || changed.Total != 2 {
		t.Fatalf("unexpected change result: %+v", changed)
	}

	// An older revision (queued retry replayed after restore) must never win.
	stale, err := svc.Sync(ctx, userID, personalbackup.KindContacts, &personalbackup.SyncRequest{
		Items: []personalbackup.Item{
			contactItem(t, keyA, "Ayşe Eski", "+905551112233", 50, sha256HexOf("old")),
		},
	})
	if err != nil {
		t.Fatalf("stale sync: %v", err)
	}
	if stale.Stale != 1 || stale.Updated != 0 {
		t.Fatalf("stale write must be rejected: %+v", stale)
	}

	// Deletions are explicit and only affect the given record.
	deleted, err := svc.Sync(ctx, userID, personalbackup.KindContacts, &personalbackup.SyncRequest{
		Deleted: []string{keyB},
	})
	if err != nil {
		t.Fatalf("delete sync: %v", err)
	}
	if deleted.Deleted != 1 || deleted.Total != 1 {
		t.Fatalf("unexpected delete result: %+v", deleted)
	}

	page, err := svc.List(ctx, userID, personalbackup.KindContacts, personalbackup.ListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].Key != keyA {
		t.Fatalf("restore list should contain only keyA: %+v", page.Items)
	}
	if page.Items[0].Revision != 200 {
		t.Fatalf("stale write changed the stored revision: %d", page.Items[0].Revision)
	}
	var decoded map[string]any
	if err := json.Unmarshal(page.Items[0].Data, &decoded); err != nil {
		t.Fatalf("restore data must be valid JSON: %v", err)
	}
	if decoded["displayName"] != "Ayşe Yılmaz" {
		t.Fatalf("restore data lost content: %v", decoded)
	}

	status, err := svc.Status(ctx, userID)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if len(status.Kinds) != 2 {
		t.Fatalf("status must report both kinds: %+v", status.Kinds)
	}
	for _, kind := range status.Kinds {
		switch kind.Kind {
		case personalbackup.KindContacts:
			if kind.Count != 1 {
				t.Fatalf("contacts count: %d", kind.Count)
			}
			if kind.LastSyncAt == nil {
				t.Fatal("contacts last sync missing")
			}
		case personalbackup.KindSMS:
			if kind.Count != 0 || kind.LastSyncAt != nil {
				t.Fatalf("sms should be empty: %+v", kind)
			}
		}
	}
}

func TestPersonalBackupStoresOnlyCiphertext(t *testing.T) {
	db, cfg := testDB(t)
	ctx := context.Background()
	authSvc, email := registerUser(t, db, cfg)
	var userID string
	if err := db.QueryRow(ctx, `select id::text from users where email = $1`, email).Scan(&userID); err != nil {
		t.Fatalf("query user: %v", err)
	}
	_ = authSvc
	vault, err := personalbackup.NewVault(cfg.SessionSecret)
	if err != nil {
		t.Fatalf("vault: %v", err)
	}
	svc, err := personalbackup.NewService(db, vault)
	if err != nil {
		t.Fatalf("service: %v", err)
	}
	key := "sms-key-0123456789abcdef"
	body := "Arabayı saat 18:00'de getir, kapıda bekle."
	hash := sha256HexOf(body)
	_, err = svc.Sync(ctx, userID, personalbackup.KindSMS, &personalbackup.SyncRequest{
		DeviceID: "Android-Pixel",
		Items: []personalbackup.Item{{
			Key:      key,
			Revision: 1,
			Hash:     hash,
			Data:     json.RawMessage(fmt.Sprintf(`{"address":"+905559998877","body":%q,"dateMs":1700000000000,"type":"inbox"}`, body)),
		}},
	})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}

	var blind string
	var payload []byte
	if err := db.QueryRow(ctx, `
		select blind_key, payload from personal_backup_items
		where user_id = $1::uuid and kind = 'sms'`, userID).Scan(&blind, &payload); err != nil {
		t.Fatalf("query stored row: %v", err)
	}
	if bytes.Contains(payload, []byte("Arabayı")) || bytes.Contains(payload, []byte("905559998877")) {
		t.Fatal("stored payload must not contain plaintext personal data")
	}
	if blind == key || strings.Contains(blind, key) {
		t.Fatal("row key must be a blind index, not the client key")
	}
	// The client item key is not stored anywhere in clear text either.
	var keyHits int
	if err := db.QueryRow(ctx, `
		select count(*) from personal_backup_items
		where user_id = $1::uuid and position($2 in blind_key) > 0`, userID, key).Scan(&keyHits); err != nil {
		t.Fatalf("query key hits: %v", err)
	}
	if keyHits != 0 {
		t.Fatal("client item key leaked into the database")
	}
}

func TestPersonalBackupIsolatesUsers(t *testing.T) {
	db, cfg := testDB(t)
	ctx := context.Background()
	_, emailA := registerUser(t, db, cfg)
	_, emailB := registerUser(t, db, cfg)
	var userA, userB string
	if err := db.QueryRow(ctx, `select id::text from users where email = $1`, emailA).Scan(&userA); err != nil {
		t.Fatalf("user a: %v", err)
	}
	if err := db.QueryRow(ctx, `select id::text from users where email = $1`, emailB).Scan(&userB); err != nil {
		t.Fatalf("user b: %v", err)
	}
	vault, err := personalbackup.NewVault(cfg.SessionSecret)
	if err != nil {
		t.Fatalf("vault: %v", err)
	}
	svc, err := personalbackup.NewService(db, vault)
	if err != nil {
		t.Fatalf("service: %v", err)
	}

	key := "shared-looking-key-123456"
	_, err = svc.Sync(ctx, userA, personalbackup.KindContacts, &personalbackup.SyncRequest{
		Items: []personalbackup.Item{contactItem(t, key, "Ayşe", "+905551112233", 5, sha256HexOf("a"))},
	})
	if err != nil {
		t.Fatalf("user a sync: %v", err)
	}

	pageB, err := svc.List(ctx, userB, personalbackup.KindContacts, personalbackup.ListOptions{})
	if err != nil {
		t.Fatalf("user b list: %v", err)
	}
	if len(pageB.Items) != 0 || pageB.Total != 0 {
		t.Fatalf("user b must not see user a rows: %+v", pageB)
	}

	// The same client key belongs to both users without clobbering rows: the
	// primary key includes user_id.
	_, err = svc.Sync(ctx, userB, personalbackup.KindContacts, &personalbackup.SyncRequest{
		Items: []personalbackup.Item{contactItem(t, key, "Bora", "+905554445566", 1, sha256HexOf("b"))},
	})
	if err != nil {
		t.Fatalf("user b sync: %v", err)
	}
	pageA, err := svc.List(ctx, userA, personalbackup.KindContacts, personalbackup.ListOptions{})
	if err != nil {
		t.Fatalf("user a list: %v", err)
	}
	if len(pageA.Items) != 1 || pageA.Items[0].Key != key {
		t.Fatalf("user a lost its row: %+v", pageA.Items)
	}
	var decoded map[string]any
	if err := json.Unmarshal(pageA.Items[0].Data, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded["displayName"] != "Ayşe" {
		t.Fatalf("user a row was overwritten by user b: %v", decoded)
	}

	// Purging B's data must never touch A's.
	if deleted, err := svc.Purge(ctx, userB, personalbackup.KindContacts); err != nil || deleted != 1 {
		t.Fatalf("purge user b: %d %v", deleted, err)
	}
	pageA, err = svc.List(ctx, userA, personalbackup.KindContacts, personalbackup.ListOptions{})
	if err != nil {
		t.Fatalf("user a list after purge: %v", err)
	}
	if len(pageA.Items) != 1 {
		t.Fatalf("user a data disappeared after user b purge: %+v", pageA.Items)
	}
}

func TestPersonalBackupRejectsForeignCiphertext(t *testing.T) {
	db, cfg := testDB(t)
	ctx := context.Background()
	_, emailA := registerUser(t, db, cfg)
	_, emailB := registerUser(t, db, cfg)
	var userA, userB string
	if err := db.QueryRow(ctx, `select id::text from users where email = $1`, emailA).Scan(&userA); err != nil {
		t.Fatalf("user a: %v", err)
	}
	if err := db.QueryRow(ctx, `select id::text from users where email = $1`, emailB).Scan(&userB); err != nil {
		t.Fatalf("user b: %v", err)
	}
	vault, err := personalbackup.NewVault(cfg.SessionSecret)
	if err != nil {
		t.Fatalf("vault: %v", err)
	}
	svc, err := personalbackup.NewService(db, vault)
	if err != nil {
		t.Fatalf("service: %v", err)
	}
	if _, err := svc.Sync(ctx, userA, personalbackup.KindContacts, &personalbackup.SyncRequest{
		Items: []personalbackup.Item{contactItem(t, "key-aaaaaaaaaaaaaaa", "Ayşe", "+905551112233", 1, sha256HexOf("a"))},
	}); err != nil {
		t.Fatalf("sync: %v", err)
	}
	// Simulate an operator (or SQL injection) moving a row to another account.
	if _, err := db.Exec(ctx, `
		update personal_backup_items set user_id = $1::uuid where user_id = $2::uuid`,
		userB, userA); err != nil {
		t.Fatalf("move row: %v", err)
	}
	if _, err := svc.List(ctx, userB, personalbackup.KindContacts, personalbackup.ListOptions{}); err == nil {
		t.Fatal("foreign ciphertext must not decrypt for another user")
	}
}

func TestPersonalBackupRestorePagination(t *testing.T) {
	fx := newPersonalFixture(t, "integration-test-session-secret-123456")
	ctx := context.Background()
	keys := []string{"page-key-0000000000001", "page-key-0000000000002", "page-key-0000000000003"}
	for i, key := range keys {
		if _, err := fx.svc.Sync(ctx, fx.userID, personalbackup.KindContacts, &personalbackup.SyncRequest{
			Items: []personalbackup.Item{contactItem(t, key, fmt.Sprintf("Kişi %d", i), "+905550000000", int64(i+1), sha256HexOf(key))},
		}); err != nil {
			t.Fatalf("sync %d: %v", i, err)
		}
	}
	var seen []string
	after := ""
	for page := 0; page < 5; page++ {
		result, err := fx.svc.List(ctx, fx.userID, personalbackup.KindContacts, personalbackup.ListOptions{
			Limit: 1,
			After: after,
		})
		if err != nil {
			t.Fatalf("list page %d: %v", page, err)
		}
		if result.Total != len(keys) {
			t.Fatalf("total should stay %d, got %d", len(keys), result.Total)
		}
		if len(result.Items) == 0 {
			break
		}
		seen = append(seen, result.Items[0].Key)
		after = result.NextAfter
		if after == "" {
			break
		}
	}
	if len(seen) != len(keys) {
		t.Fatalf("pagination returned %d of %d records: %v", len(seen), len(keys), seen)
	}

	// since filters by update time; a future timestamp yields nothing.
	future := time.Now().Add(2 * time.Minute)
	empty, err := fx.svc.List(ctx, fx.userID, personalbackup.KindContacts, personalbackup.ListOptions{Since: &future})
	if err != nil {
		t.Fatalf("list since: %v", err)
	}
	if len(empty.Items) != 0 {
		t.Fatalf("future since must match nothing: %+v", empty.Items)
	}
}

func TestPersonalBackupEndpointAuthorization(t *testing.T) {
	db, cfg := testDB(t)
	ctx := context.Background()
	authSvc, email := registerUser(t, db, cfg)
	var userID string
	if err := db.QueryRow(ctx, `select id::text from users where email = $1`, email).Scan(&userID); err != nil {
		t.Fatalf("query user: %v", err)
	}
	vault, err := personalbackup.NewVault(cfg.SessionSecret)
	if err != nil {
		t.Fatalf("vault: %v", err)
	}
	svc, err := personalbackup.NewService(db, vault)
	if err != nil {
		t.Fatalf("service: %v", err)
	}
	_, token, err := authSvc.CreateDevice(ctx, userID, "integration-android")
	if err != nil {
		t.Fatalf("create device: %v", err)
	}
	authHandler := auth.NewHandler(authSvc, nil)
	mux := httptest.NewServer(authHandler.RequireAuth(personalbackup.NewHandler(svc)))
	defer mux.Close()

	// No token: rejected before reaching the service.
	resp, err := http.Get(mux.URL + personalbackup.BasePath + "contacts")
	if err != nil {
		t.Fatalf("unauthorized get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without a token, got %d", resp.StatusCode)
	}

	body := fmt.Sprintf(`{"deviceId":"Pixel 7","items":[{"key":"http-key-00000000001","revision":7,"hash":%q,"data":{"displayName":"Ayşe"}}]}`, sha256HexOf("x"))
	req, err := http.NewRequest(http.MethodPost, mux.URL+personalbackup.BasePath+"contacts", strings.NewReader(body))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("authorized post: %v", err)
	}
	var result personalbackup.SyncResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode sync result: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || result.Inserted != 1 {
		t.Fatalf("sync failed: status %d result %+v", resp.StatusCode, result)
	}

	req, _ = http.NewRequest(http.MethodGet, mux.URL+personalbackup.BasePath+"contacts", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var page personalbackup.ListPage
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("unexpected restore page: status %d %+v", resp.StatusCode, page)
	}

	req, _ = http.NewRequest(http.MethodGet, mux.URL+personalbackup.BasePath+"status", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	var status personalbackup.Status
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || len(status.Kinds) != 2 {
		t.Fatalf("unexpected status: %d %+v", resp.StatusCode, status)
	}

	// A revoked/blank token must not reach the data either.
	req, _ = http.NewRequest(http.MethodDelete, mux.URL+personalbackup.BasePath+"contacts", nil)
	req.Header.Set("Authorization", "Bearer "+strings.Repeat("x", 32))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("bogus token delete: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for a bogus token, got %d", resp.StatusCode)
	}

	// The owner can erase the stored copy.
	req, _ = http.NewRequest(http.MethodDelete, mux.URL+personalbackup.BasePath+"contacts", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("purge failed: %d", resp.StatusCode)
	}
	var remaining int
	if err := db.QueryRow(ctx, `select count(*) from personal_backup_items where user_id = $1::uuid`, userID).Scan(&remaining); err != nil {
		t.Fatalf("count remaining: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("purge left %d rows", remaining)
	}
}
