package app

import (
	"bufio"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log"
	"net"
	"net/http"
	"net/netip"
	"strings"

	"necipdrive/internal/httpx"
)

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s", r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
	})
}

func csrfMiddleware(next http.Handler, secret string, trusted ...netip.Prefix) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cw := &csrfResponseWriter{ResponseWriter: w, r: r, secret: secret, trusted: trusted}
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(cw, r)
			return
		}
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(r.Header.Get("Authorization"))), "bearer ") {
			next.ServeHTTP(cw, r)
			return
		}
		switch r.URL.Path {
		case "/api/auth/login", "/api/auth/register", "/api/auth/device-login",
			"/api/auth/login/2fa", "/api/auth/forgot-password", "/api/auth/reset-password",
			"/api/auth/qr/redeem":
			next.ServeHTTP(cw, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/s/") || strings.HasPrefix(r.URL.Path, "/dav") {
			next.ServeHTTP(cw, r)
			return
		}
		session := sessionCookieValue(r)
		expected := csrfTokenFor(secret, session)
		if session == "" || !hmac.Equal([]byte(r.Header.Get("X-CSRF-Token")), []byte(expected)) {
			httpx.Error(w, http.StatusForbidden, "invalid csrf token")
			return
		}
		next.ServeHTTP(cw, r)
	})
}

func csrfTokenFor(secret, session string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte("trdriver-csrf:" + session))
	return hex.EncodeToString(mac.Sum(nil))
}

func sessionCookieValue(r *http.Request) string {
	if c, err := r.Cookie("session_token"); err == nil {
		return c.Value
	}
	return ""
}

type csrfResponseWriter struct {
	http.ResponseWriter
	r        *http.Request
	secret   string
	trusted  []netip.Prefix
	injected bool
}

func (w *csrfResponseWriter) inject() {
	if w.injected {
		return
	}
	w.injected = true
	session := sessionCookieValue(w.r)
	// If login/logout changed the session cookie in this response, bind CSRF to the new value.
	for _, raw := range w.Header().Values("Set-Cookie") {
		if strings.HasPrefix(raw, "session_token=") {
			parts := strings.SplitN(strings.TrimPrefix(raw, "session_token="), ";", 2)
			session = parts[0]
		}
	}
	token := csrfTokenFor(w.secret, session)
	http.SetCookie(w, &http.Cookie{
		Name:     "csrf_token",
		Value:    token,
		Path:     "/",
		HttpOnly: false,
		SameSite: http.SameSiteLaxMode,
		Secure:   isHTTPS(w.r, w.trusted),
	})
	if w.r.Method == http.MethodGet || w.r.Method == http.MethodHead {
		w.Header().Set("X-CSRF-Token", token)
	}
}

func (w *csrfResponseWriter) WriteHeader(code int) {
	w.inject()
	w.ResponseWriter.WriteHeader(code)
}

func (w *csrfResponseWriter) Write(b []byte) (int, error) {
	w.inject()
	return w.ResponseWriter.Write(b)
}

func (w *csrfResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *csrfResponseWriter) Flush() {
	w.inject()
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *csrfResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	w.inject()
	if h, ok := w.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, errors.New("response writer does not support hijacking")
}

func isHTTPS(r *http.Request, trusted []netip.Prefix) bool {
	return httpx.IsHTTPS(r, trusted)
}
