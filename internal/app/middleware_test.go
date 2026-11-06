package app

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestCSRFMiddlewareRejectsInvalidPost(t *testing.T) {
	handler := csrfMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), "secret")

	req := httptest.NewRequest(http.MethodPost, "/api/files", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid csrf token") {
		t.Fatalf("expected json csrf error, got %q", rec.Body.String())
	}
}

func TestIsHTTPSUsesForwardedProtoOnlyFromTrustedProxy(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.9:1234"
	req.Header.Set("X-Forwarded-Proto", "https")
	if isHTTPS(req, nil) {
		t.Fatal("untrusted forwarded proto must be ignored")
	}
	req.RemoteAddr = "10.0.0.2:1234"
	if !isHTTPS(req, []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}) {
		t.Fatal("expected https from trusted proxy")
	}
}

func TestCSRFMiddlewareSetsHeaderOnGet(t *testing.T) {
	handler := csrfMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), "secret")

	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Header().Get("X-CSRF-Token") == "" {
		t.Fatal("expected csrf header to be set")
	}
}

func TestCSRFAllowsDeviceLoginAndBearer(t *testing.T) {
	handler := csrfMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), "secret")

	req := httptest.NewRequest(http.MethodPost, "/api/auth/device-login", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("device-login expected 204, got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/sync/trash", nil)
	req.Header.Set("Authorization", "Bearer abc")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("bearer expected 204, got %d", rec.Code)
	}
}

func TestCSRFTokenIsSessionBound(t *testing.T) {
	handler := csrfMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), "secret")

	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: "session_token", Value: "session-a"})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	token := rec.Header().Get("X-CSRF-Token")
	if token == "" {
		t.Fatal("expected csrf token")
	}

	post := httptest.NewRequest(http.MethodPost, "/api/files", nil)
	post.AddCookie(&http.Cookie{Name: "session_token", Value: "session-a"})
	post.Header.Set("X-CSRF-Token", token)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, post)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("valid session csrf expected 204, got %d", rec.Code)
	}

	post = httptest.NewRequest(http.MethodPost, "/api/files", nil)
	post.AddCookie(&http.Cookie{Name: "session_token", Value: "session-b"})
	post.Header.Set("X-CSRF-Token", token)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, post)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-session csrf expected 403, got %d", rec.Code)
	}
}

func TestCSRFCookieFollowsLoginSetCookie(t *testing.T) {
	handler := csrfMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "session_token", Value: "new-session", Path: "/"})
		w.WriteHeader(http.StatusNoContent)
	}), "secret")
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	resp := rec.Result()
	defer resp.Body.Close()
	var csrf string
	for _, c := range resp.Cookies() {
		if c.Name == "csrf_token" {
			csrf = c.Value
		}
	}
	if csrf != csrfTokenFor("secret", "new-session") {
		t.Fatalf("csrf cookie not bound to new session: %q", csrf)
	}
}
