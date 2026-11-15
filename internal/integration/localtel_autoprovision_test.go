package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"necipdrive/internal/auth"
	"necipdrive/internal/localtel"
)

func TestLocalTelAutoProvisionConfigAndUserIsolation(t *testing.T) {
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
	service, err := localtel.NewService(db, vault)
	if err != nil {
		t.Fatalf("service: %v", err)
	}
	service.Configure("drive.example.test", time.Minute, true)
	testPhone := fmt.Sprintf("90555%09d", time.Now().UnixNano()%1_000_000_000)
	if _, err := db.Exec(ctx, `update users set phone = $1 where id = $2::uuid`, testPhone, userA); err != nil {
		t.Fatalf("set test phone: %v", err)
	}
	handler := localtel.NewHandler(service, localtel.NewHub())

	_, tokenA, err := authSvc.CreateDevice(ctx, userA, "android-a")
	if err != nil {
		t.Fatalf("device a: %v", err)
	}
	api := httptest.NewServer(auth.NewHandler(authSvc, nil).RequireAuth(handler))
	defer api.Close()
	request, _ := http.NewRequest(http.MethodGet, api.URL+localtel.BasePath+"config", nil)
	request.Header.Set("Authorization", "Bearer "+tokenA)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("fetch config: %v", err)
	}
	var view localtel.ConfigView
	if err := json.NewDecoder(response.Body).Decode(&view); err != nil {
		response.Body.Close()
		t.Fatalf("decode config: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("config status = %d", response.StatusCode)
	}
	if view.Mode != localtel.ModeAuto || !view.Enabled || view.Gateway == nil {
		t.Fatalf("missing auto gateway config: %+v", view)
	}
	if view.Gateway.WSSUrl != "wss://drive.example.test"+localtel.GatewayPath {
		t.Fatalf("unexpected gateway URL: %q", view.Gateway.WSSUrl)
	}
	if view.Gateway.Username == "" || view.Gateway.Secret == "" || view.Gateway.Extension == "" || view.Gateway.Token == "" {
		t.Fatal("zero-config response missing account credentials or token")
	}
	if view.Gateway.Extension != testPhone {
		t.Fatalf("extension did not use the user's canonical phone: %q", view.Gateway.Extension)
	}
	owner, _, err := service.ValidateToken(ctx, view.Gateway.Token)
	if err != nil || owner != userA {
		t.Fatalf("issued token owner=%q err=%v", owner, err)
	}
	if _, _, err := service.ValidateToken(ctx, view.Gateway.Token+"x"); err == nil {
		t.Fatal("tampered gateway token was accepted")
	}

	var encrypted []byte
	if err := db.QueryRow(ctx, `select password_enc from localtel_accounts where user_id = $1::uuid`, userA).Scan(&encrypted); err != nil {
		t.Fatalf("load encrypted account: %v", err)
	}
	if strings.Contains(string(encrypted), view.Gateway.Secret) {
		t.Fatal("account password was not encrypted at rest")
	}
	accountB, err := service.EnsureAccount(ctx, userB)
	if err != nil {
		t.Fatalf("second user's account was not provisioned: %v", err)
	}
	if accountB.UserID != userB || accountB.Extension == view.Gateway.Extension {
		t.Fatalf("second user's account mismatch (user=%q, extension=%q)", accountB.UserID, accountB.Extension)
	}
	_, tokenB, err := authSvc.CreateDevice(ctx, userB, "android-b")
	if err != nil {
		t.Fatalf("device b: %v", err)
	}
	requestB, _ := http.NewRequest(http.MethodGet, api.URL+localtel.BasePath+"config", nil)
	requestB.Header.Set("Authorization", "Bearer "+tokenB)
	responseB, err := http.DefaultClient.Do(requestB)
	if err != nil {
		t.Fatalf("fetch config b: %v", err)
	}
	var viewB localtel.ConfigView
	if err := json.NewDecoder(responseB.Body).Decode(&viewB); err != nil {
		responseB.Body.Close()
		t.Fatalf("decode config b: %v", err)
	}
	responseB.Body.Close()
	if responseB.StatusCode != http.StatusOK || viewB.Gateway == nil || viewB.Gateway.Extension != accountB.Extension || viewB.Gateway.Secret == view.Gateway.Secret {
		gotExtension := ""
		if viewB.Gateway != nil {
			gotExtension = viewB.Gateway.Extension
		}
		t.Fatalf("user B account mismatch: status=%d extension=%q", responseB.StatusCode, gotExtension)
	}
	other, ok := service.ResolveCallTarget(ctx, "+"+testPhone)
	if !ok || other != userA {
		t.Fatalf("phone contact resolved to %q, want %q", other, userA)
	}
	if got, ok := service.ResolveCallTarget(ctx, "99999"); ok && got == userA {
		t.Fatal("unowned extension resolved to another user's account")
	}

	// A phone set after the account was provisioned (random extension) must
	// still be dialable by any national/international spelling.
	latePhone := fmt.Sprintf("90544%09d", time.Now().UnixNano()%1_000_000_000)
	if _, err := db.Exec(ctx, `update users set phone = $1 where id = $2::uuid`, latePhone, userB); err != nil {
		t.Fatalf("set late phone: %v", err)
	}
	for _, dial := range []string{"+" + latePhone, "0" + latePhone[2:], latePhone[2:], "0 (544) " + latePhone[5:8] + " " + latePhone[8:]} {
		got, ok := service.ResolveCallTarget(ctx, dial)
		if !ok || got != userB {
			t.Fatalf("dial %q resolved to %q (ok=%v), want %q", dial, got, ok, userB)
		}
	}
}
