package auth

import (
	"strings"
	"testing"

	"necipdrive/internal/config"
)

func TestOTPHashRoundTrip(t *testing.T) {
	code, err := generateOTPCode()
	if err != nil {
		t.Fatal(err)
	}
	if len(code) != 6 {
		t.Fatalf("expected 6 digits, got %q", code)
	}
	s := &Service{cfg: config.Config{SessionSecret: "test-session-secret-123456"}}
	h1 := s.hashOTP(code)
	h2 := s.hashOTP(code)
	if h1 != h2 {
		t.Fatal("hash not stable")
	}
	if !strings.HasPrefix(h1, "hmac-sha256:") {
		t.Fatalf("expected keyed hash prefix, got %q", h1)
	}
	if s.hashOTP("000000") == s.hashOTP("000001") {
		t.Fatal("different codes should hash differently")
	}
	other := &Service{cfg: config.Config{SessionSecret: "another-session-secret-123456"}}
	if other.hashOTP(code) == h1 {
		t.Fatal("different server secrets should produce different hashes")
	}
}

func TestPasswordHashVerify(t *testing.T) {
	encoded, err := hashPassword("secretpass")
	if err != nil {
		t.Fatal(err)
	}
	if !verifyPassword("secretpass", encoded) {
		t.Fatal("expected match")
	}
	if verifyPassword("wrong", encoded) {
		t.Fatal("expected mismatch")
	}
}
