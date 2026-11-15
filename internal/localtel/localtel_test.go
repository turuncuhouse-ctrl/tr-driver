package localtel

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"necipdrive/internal/auth"
	"necipdrive/internal/domain"
)

func testVault(t *testing.T) *Vault {
	t.Helper()
	v, err := NewVault("localtel-unit-test-secret-012345")
	if err != nil {
		t.Fatalf("new vault: %v", err)
	}
	return v
}

func testHandler(t *testing.T) (*Handler, *Hub) {
	t.Helper()
	svc, err := NewService(nil, testVault(t))
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	hub := NewHub()
	return NewHandler(svc, hub), hub
}

func authedReq(method, path string, body []byte, userID string) *http.Request {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if userID != "" {
		req = req.WithContext(auth.WithUser(context.Background(), &domain.User{
			ID:    userID,
			Email: "driver@example.com",
		}))
	}
	return req
}

func TestVaultSealOpenAndUserIsolation(t *testing.T) {
	v := testVault(t)
	plain := []byte(`{"username":"1001","secret":"super-sip-password"}`)
	sealed, err := v.Seal(plain, aadFor("user-1"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if bytes.Contains(sealed, []byte("super-sip-password")) {
		t.Fatal("ciphertext must not contain secret in plaintext")
	}
	got, err := v.Open(sealed, aadFor("user-1"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("roundtrip mismatch")
	}
	if _, err := v.Open(sealed, aadFor("user-2")); err == nil {
		t.Fatal("foreign user must not decrypt config")
	}
	sealed[len(sealed)-1] ^= 0xff
	if _, err := v.Open(sealed, aadFor("user-1")); err == nil {
		t.Fatal("tampered ciphertext must fail")
	}
}

func TestNormalizeInputValidation(t *testing.T) {
	cfg, err := normalizeInput(ConfigInput{
		Enabled:      true,
		ProviderType: "SIP",
		ServerURL:    "pbx.localtel.example",
		Username:     "1001",
		Secret:       "sip-secret-123",
	})
	if err != nil {
		t.Fatalf("valid sip config rejected: %v", err)
	}
	if cfg.ProviderType != ProviderSIP || cfg.SIPPort != 5060 || cfg.Transport != TransportUDP || cfg.DefaultCountryCode != "90" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}

	tlsCfg, err := normalizeInput(ConfigInput{
		Enabled:      true,
		ProviderType: "sip",
		SIPDomain:    "sip.localtel.net",
		Transport:    "tls",
		Username:     "1002",
	})
	if err != nil {
		t.Fatalf("tls config rejected: %v", err)
	}
	if tlsCfg.SIPPort != 5061 {
		t.Fatalf("expected default TLS port 5061, got %d", tlsCfg.SIPPort)
	}

	badCases := []ConfigInput{
		{Enabled: true, ProviderType: "unknown", ServerURL: "pbx.local", Username: "1001"},
		{Enabled: true, ProviderType: "sip", ServerURL: "https://user:pass@pbx.local", Username: "1001"},
		{Enabled: true, ProviderType: "sip", ServerURL: "pbx.local", Transport: "wss", Username: "1001"},
		{Enabled: true, ProviderType: "sip", ServerURL: "", SIPDomain: "", Username: "1001"},
		{Enabled: true, ProviderType: "sip", ServerURL: "pbx.local", SIPPort: 70000, Username: "1001"},
		{Enabled: true, ProviderType: "sip", ServerURL: "pbx.local\nX-Inject:1", Username: "1001"},
		{Enabled: true, ProviderType: "gateway", DefaultCountryCode: "TR", Username: "1001"},
		{Enabled: true, ProviderType: "gateway", OutboundPrefix: "9abc", Username: "1001"},
		{Enabled: true, ProviderType: "gateway", ServerURL: "ftp://pbx.local", Username: "1001"},
	}
	for i, bad := range badCases {
		if _, err := normalizeInput(bad); err == nil {
			t.Fatalf("case %d should be rejected: %+v", i, bad)
		}
	}
}

func TestToViewNeverExposesSecret(t *testing.T) {
	view := toView(storedConfig{
		Enabled:      true,
		ProviderType: ProviderSIP,
		ServerURL:    "sip.localtel.example",
		SIPPort:      5060,
		Transport:    TransportUDP,
		Username:     "1001",
		Secret:       "very-sensitive-sip-secret",
	}, nil)
	if !view.HasSecret {
		t.Fatal("HasSecret should be true")
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal view: %v", err)
	}
	if strings.Contains(string(encoded), "very-sensitive-sip-secret") {
		t.Fatal("ConfigView JSON leaked secret")
	}
}

func TestHandlerAuthAndValidation(t *testing.T) {
	h, _ := testHandler(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedReq(http.MethodGet, BasePath+"config", nil, ""))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without auth, got %d", rec.Code)
	}

	// Unknown JSON fields rejected.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, authedReq(http.MethodPut, BasePath+"config", []byte(`{"extraField":true}`), "u1"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on unknown field, got %d", rec.Code)
	}

	// Oversized PUT rejected with 413.
	rec = httptest.NewRecorder()
	huge := bytes.Repeat([]byte("a"), maxConfigBodyBytes+16)
	h.ServeHTTP(rec, authedReq(http.MethodPut, BasePath+"config", huge, "u1"))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 on oversized PUT, got %d", rec.Code)
	}

	// Provision only accepts GET.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, authedReq(http.MethodPost, BasePath+"config/provision", nil, "u1"))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 on provision POST, got %d", rec.Code)
	}
}

func TestHubIsolatesUsersAndDispatchesSignals(t *testing.T) {
	h, hub := testHandler(t)
	cUserA1 := hub.Register("user-a", "dev-1")
	cUserA2 := hub.Register("user-a", "dev-2")
	cUserB := hub.Register("user-b", "dev-b")
	defer hub.Unregister(cUserA1)
	defer hub.Unregister(cUserA2)
	defer hub.Unregister(cUserB)

	body := []byte(`{"type":"invite","callId":"call-123","number":"+905551112233","displayName":"Ayşe"}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedReq(http.MethodPost, BasePath+"signal", body, "user-a"))
	if rec.Code != http.StatusOK {
		t.Fatalf("signal post failed: %d %s", rec.Code, rec.Body.String())
	}

	select {
	case raw := <-cUserA1.SendChannel():
		var msg SignalMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if msg.CallID != "call-123" || msg.Type != "invite" {
			t.Fatalf("unexpected signal: %+v", msg)
		}
	default:
		t.Fatal("user-a client 1 did not receive signal")
	}

	select {
	case <-cUserA2.SendChannel():
	default:
		t.Fatal("user-a client 2 did not receive signal")
	}

	select {
	case <-cUserB.SendChannel():
		t.Fatal("user-b must NEVER receive user-a signals")
	default:
	}

	// Sender exclusion in Dispatch.
	delivered := hub.Dispatch("user-a", cUserA1.id, SignalMessage{Type: "hangup", CallID: "call-123"})
	if delivered != 1 {
		t.Fatalf("expected 1 delivered excluding sender, got %d", delivered)
	}
	select {
	case <-cUserA1.SendChannel():
		t.Fatal("sender must not receive its own broadcast")
	default:
	}
	select {
	case <-cUserA2.SendChannel():
	default:
		t.Fatal("peer device did not receive hangup")
	}
}

func TestValidateSignal(t *testing.T) {
	if err := ValidateSignal(&SignalMessage{Type: "invite", CallID: "call_01"}); err != nil {
		t.Fatalf("valid signal rejected: %v", err)
	}
	if err := ValidateSignal(&SignalMessage{Type: "ping"}); err != nil {
		t.Fatalf("ping rejected: %v", err)
	}
	invalid := []SignalMessage{
		{Type: "drop_table", CallID: "c1"},
		{Type: "invite", CallID: ""},
		{Type: "invite", CallID: "../evil"},
		{Type: "invite", CallID: "c1", Number: "123\r\nBYE"},
		{Type: "audio", CallID: "c1", Payload: strings.Repeat("x", maxSignalPayloadBytes+1)},
	}
	for i, s := range invalid {
		msg := s
		if err := ValidateSignal(&msg); err == nil {
			t.Fatalf("invalid signal %d accepted: %+v", i, s)
		}
	}
}
