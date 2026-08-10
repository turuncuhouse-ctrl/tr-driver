package app

import (
	"crypto/sha256"
	"encoding/hex"
	"log"
	"net/http"
	"strings"

	"necipdrive/internal/httpx"
)

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s", r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
	})
}

func csrfMiddleware(next http.Handler, secret string) http.Handler {
	sum := sha256.Sum256([]byte(secret))
	token := hex.EncodeToString(sum[:])

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{
			Name:     "csrf_token",
			Value:    token,
			Path:     "/",
			HttpOnly: false,
			SameSite: http.SameSiteLaxMode,
			Secure:   isHTTPS(r),
		})
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			w.Header().Set("X-CSRF-Token", token)
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(r.Header.Get("Authorization"))), "bearer ") {
			next.ServeHTTP(w, r)
			return
		}
		// Public auth endpoints bootstrap the session/device token before CSRF is available.
		switch r.URL.Path {
		case "/api/auth/login", "/api/auth/register", "/api/auth/device-login":
			next.ServeHTTP(w, r)
			return
		}
		// Public share unlock (password form) is cookie-less.
		if strings.HasPrefix(r.URL.Path, "/s/") {
			next.ServeHTTP(w, r)
			return
		}
		if r.Header.Get("X-CSRF-Token") != token {
			httpx.Error(w, http.StatusForbidden, "invalid csrf token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	proto := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")))
	return proto == "https"
}
