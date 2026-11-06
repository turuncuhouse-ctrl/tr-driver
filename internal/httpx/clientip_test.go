package httpx

import (
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestClientIPIgnoresUntrustedForwarded(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "203.0.113.9:1234"
	r.Header.Set("X-Forwarded-For", "198.51.100.1")
	if got := ClientIP(r, nil); got != "203.0.113.9" {
		t.Fatalf("got %q", got)
	}
}

func TestClientIPTrustedProxyRightmostUntrusted(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.2:1234"
	r.Header.Set("X-Forwarded-For", "198.51.100.7, 203.0.113.9, 10.0.0.1")
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	if got := ClientIP(r, trusted); got != "203.0.113.9" {
		t.Fatalf("got %q", got)
	}
}

func TestIsHTTPSRequiresTrustedProxy(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "203.0.113.9:1234"
	r.Header.Set("X-Forwarded-Proto", "https")
	if IsHTTPS(r, nil) {
		t.Fatal("untrusted XFP must be ignored")
	}
	r.RemoteAddr = "10.0.0.2:1234"
	if !IsHTTPS(r, []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}) {
		t.Fatal("trusted XFP must be honored")
	}
}
