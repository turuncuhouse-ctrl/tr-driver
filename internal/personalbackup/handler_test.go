package personalbackup

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"necipdrive/internal/auth"
	"necipdrive/internal/domain"
)

// newTestHandler wires the real handler with a service that has a vault but no
// database: request validation, routing and auth mapping are exercised before
// any SQL runs.
func newTestService(t *testing.T) *Service {
	t.Helper()
	vault, err := NewVault("handler-test-secret-0123456789")
	if err != nil {
		t.Fatalf("vault: %v", err)
	}
	service, err := NewService(nil, vault)
	if err != nil {
		t.Fatalf("service: %v", err)
	}
	return service
}

func newTestHandler(t *testing.T) *Handler {
	t.Helper()
	return NewHandler(newTestService(t))
}

func do(t *testing.T, h *Handler, method, path string, body []byte, withUser bool) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if withUser {
		req = req.WithContext(auth.WithUser(context.Background(), &domain.User{ID: "user-1", Email: "u@example.com"}))
	}
	rec := httptest.NewRecorder()
	h.Root(rec, req)
	return rec
}

func TestHandlerRequiresAuthentication(t *testing.T) {
	h := newTestHandler(t)
	rec := do(t, h, http.MethodGet, BasePath+"contacts", nil, false)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestHandlerUnknownKindIsNotFound(t *testing.T) {
	h := newTestHandler(t)
	rec := do(t, h, http.MethodGet, BasePath+"calls", nil, true)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
	// Trailing segments must not be treated as another resource.
	rec = do(t, h, http.MethodGet, BasePath+"contacts/extra", nil, true)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for nested path, got %d", rec.Code)
	}
}

func TestHandlerRejectsBadMethods(t *testing.T) {
	h := newTestHandler(t)
	rec := do(t, h, http.MethodPut, BasePath+"contacts", nil, true)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
	rec = do(t, h, http.MethodPost, BasePath+"status", nil, true)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for status POST, got %d", rec.Code)
	}
}

func TestHandlerRejectsInvalidJSON(t *testing.T) {
	h := newTestHandler(t)
	rec := do(t, h, http.MethodPost, BasePath+"contacts", []byte(`{"items":[`), true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "items") {
		t.Fatalf("response must not echo the payload: %s", rec.Body.String())
	}
	// Unknown fields are rejected rather than silently ignored.
	rec = do(t, h, http.MethodPost, BasePath+"contacts", []byte(`{"unexpected":true}`), true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown field, got %d", rec.Code)
	}
}

func TestHandlerRejectsOversizedBody(t *testing.T) {
	h := newTestHandler(t)
	body := make([]byte, MaxRequestBytes+64)
	for i := range body {
		body[i] = 'a'
	}
	rec := do(t, h, http.MethodPost, BasePath+"sms", body, true)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d", rec.Code)
	}
}

func TestHandlerRejectsInvalidListParameters(t *testing.T) {
	h := newTestHandler(t)
	cases := []string{
		BasePath + "contacts?limit=abc",
		BasePath + "contacts?since=yesterday",
		BasePath + "contacts?after=zzzz",
	}
	for _, path := range cases {
		rec := do(t, h, http.MethodGet, path, nil, true)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: expected 400, got %d (%s)", path, rec.Code, rec.Body.String())
		}
	}
}

func TestHandlerInstallsWriteRateLimiter(t *testing.T) {
	if NewHandler(newTestService(t)).limiter == nil {
		t.Fatal("handler must install a write rate limiter")
	}
}

func TestHandlerHeadIsNotAllowed(t *testing.T) {
	h := newTestHandler(t)
	rec := do(t, h, http.MethodHead, BasePath+"contacts", nil, true)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for HEAD, got %d", rec.Code)
	}
}
