package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"
	"necipdrive/internal/auth"
	"necipdrive/internal/localtel"
)

func TestLocalTelEncryptedConfigLifecycleAndIsolation(t *testing.T) {
	db, cfg := testDB(t)
	ctx := context.Background()
	authSvc, emailA := registerUser(t, db, cfg)
	_, emailB := registerUser(t, db, cfg)

	var userA, userB string
	if err := db.QueryRow(ctx, `select id::text from users where email = $1`, emailA).Scan(&userA); err != nil {
		t.Fatalf("query user a: %v", err)
	}
	if err := db.QueryRow(ctx, `select id::text from users where email = $1`, emailB).Scan(&userB); err != nil {
		t.Fatalf("query user b: %v", err)
	}

	vault, err := localtel.NewVault(cfg.SessionSecret)
	if err != nil {
		t.Fatalf("vault: %v", err)
	}
	svc, err := localtel.NewService(db, vault)
	if err != nil {
		t.Fatalf("service: %v", err)
	}

	_, tokenA, err := authSvc.CreateDevice(ctx, userA, "android-a")
	if err != nil {
		t.Fatalf("device a: %v", err)
	}
	_, tokenB, err := authSvc.CreateDevice(ctx, userB, "android-b")
	if err != nil {
		t.Fatalf("device b: %v", err)
	}

	authHandler := auth.NewHandler(authSvc, nil)
	srv := httptest.NewServer(authHandler.RequireAuth(localtel.NewHandler(svc, localtel.NewHub())))
	defer srv.Close()

	// Unauthenticated request fails with 401.
	resp, err := http.Get(srv.URL + localtel.BasePath + "config")
	if err != nil {
		t.Fatalf("unauth get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}

	// Save encrypted LocalTel config for User A.
	putBody := `{
		"enabled": true,
		"providerType": "sip",
		"serverUrl": "sip.localtel.example",
		"sipDomain": "localtel.example",
		"sipPort": 5060,
		"transport": "udp",
		"username": "1001",
		"extension": "1001",
		"displayName": "Ahmet",
		"defaultCountryCode": "90",
		"autoRegister": true,
		"secret": "super-secret-sip-pass"
	}`
	req, _ := http.NewRequest(http.MethodPut, srv.URL+localtel.BasePath+"config", bytes.NewBufferString(putBody))
	req.Header.Set("Authorization", "Bearer "+tokenA)
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("put config: %v", err)
	}
	var view localtel.ConfigView
	if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
		t.Fatalf("decode view: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !view.HasSecret || !view.Configured || view.Username != "1001" {
		t.Fatalf("unexpected put response: %d %+v", resp.StatusCode, view)
	}

	// Database must store ciphertext only — no plaintext secret or username.
	var storedBlob []byte
	if err := db.QueryRow(ctx, `select payload from localtel_configs where user_id = $1::uuid`, userA).Scan(&storedBlob); err != nil {
		t.Fatalf("query stored blob: %v", err)
	}
	if bytes.Contains(storedBlob, []byte("super-secret-sip-pass")) || bytes.Contains(storedBlob, []byte("sip.localtel.example")) {
		t.Fatal("stored localtel_configs payload must not contain plaintext credentials or URL")
	}

	// GET /config returns metadata (hasSecret=true) without the raw secret.
	req, _ = http.NewRequest(http.MethodGet, srv.URL+localtel.BasePath+"config", nil)
	req.Header.Set("Authorization", "Bearer "+tokenA)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	var rawMap map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&rawMap); err != nil {
		t.Fatalf("decode map: %v", err)
	}
	resp.Body.Close()
	if _, hasRawSecretField := rawMap["secret"]; hasRawSecretField {
		t.Fatal("GET /config must not return raw secret field")
	}

	// Updating without sending secret preserves existing secret.
	updateBody := `{
		"enabled": true,
		"providerType": "sip",
		"serverUrl": "sip2.localtel.example",
		"sipDomain": "localtel.example",
		"sipPort": 5061,
		"transport": "tls",
		"username": "1001",
		"autoRegister": true
	}`
	req, _ = http.NewRequest(http.MethodPut, srv.URL+localtel.BasePath+"config", bytes.NewBufferString(updateBody))
	req.Header.Set("Authorization", "Bearer "+tokenA)
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("update config: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update status: %d", resp.StatusCode)
	}

	// Provision returns decrypted secret with Cache-Control: no-store.
	req, _ = http.NewRequest(http.MethodGet, srv.URL+localtel.BasePath+"config/provision", nil)
	req.Header.Set("Authorization", "Bearer "+tokenA)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	var prov localtel.ConfigProvision
	if err := json.NewDecoder(resp.Body).Decode(&prov); err != nil {
		t.Fatalf("decode provision: %v", err)
	}
	cc := resp.Header.Get("Cache-Control")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || prov.Secret != "super-secret-sip-pass" || prov.ServerURL != "sip2.localtel.example" {
		t.Fatalf("provision failed: %d %+v", resp.StatusCode, prov)
	}
	if cc != "no-store" {
		t.Fatalf("expected Cache-Control: no-store, got %q", cc)
	}

	// User B cannot see or provision User A's config.
	req, _ = http.NewRequest(http.MethodGet, srv.URL+localtel.BasePath+"config/provision", nil)
	req.Header.Set("Authorization", "Bearer "+tokenB)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("user b provision: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for user b provision, got %d", resp.StatusCode)
	}

	// User A can delete its config.
	req, _ = http.NewRequest(http.MethodDelete, srv.URL+localtel.BasePath+"config", nil)
	req.Header.Set("Authorization", "Bearer "+tokenA)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete config: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete status: %d", resp.StatusCode)
	}

	// Verify WebSocket signaling relay for User A (two devices) and isolation from User B.
	wsBase := "ws" + strings.TrimPrefix(srv.URL, "http") + localtel.BasePath + "ws"
	dialWS := func(token string) *websocket.Conn {
		cfgWS, err := websocket.NewConfig(wsBase, srv.URL)
		if err != nil {
			t.Fatalf("ws config: %v", err)
		}
		cfgWS.Header = http.Header{
			"Authorization": []string{"Bearer " + token},
		}
		conn, err := websocket.DialConfig(cfgWS)
		if err != nil {
			t.Fatalf("ws dial: %v", err)
		}
		return conn
	}

	// Unauthenticated WS upgrade fails.
	badWS, _ := websocket.NewConfig(wsBase, srv.URL)
	if conn, err := websocket.DialConfig(badWS); err == nil {
		conn.Close()
		t.Fatal("unauthenticated websocket should fail")
	}

	wsA1 := dialWS(tokenA)
	defer wsA1.Close()
	wsA2 := dialWS(tokenA)
	defer wsA2.Close()
	wsB := dialWS(tokenB)
	defer wsB.Close()

	// Wait briefly for both connections to register inside the Hub.
	time.Sleep(50 * time.Millisecond)

	if err := websocket.Message.Send(wsA1, `{"type":"invite","callId":"ws-call-1","number":"+905551112233"}`); err != nil {
		t.Fatalf("ws send invite: %v", err)
	}

	// The dialed number maps to no account: the calling client itself gets a
	// reject (so the app can end the call) and no other device rings.
	_ = wsA1.SetReadDeadline(time.Now().Add(2 * time.Second))
	var received string
	if err := websocket.Message.Receive(wsA1, &received); err != nil {
		t.Fatalf("caller did not receive unreachable-target reject: %v", err)
	}
	if !strings.Contains(received, "ws-call-1") || !strings.Contains(received, `"reject"`) {
		t.Fatalf("unexpected ws payload: %s", received)
	}
	_ = wsA2.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	var extra string
	if err := websocket.Message.Receive(wsA2, &extra); err == nil {
		t.Fatalf("caller's other device must not ring for an unresolved target, got: %s", extra)
	}

	_ = wsB.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	var leaked string
	if err := websocket.Message.Receive(wsB, &leaked); err == nil {
		t.Fatalf("wsB must never receive wsA1 signals, got: %s", leaked)
	}
}
