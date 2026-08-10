package auth

import (
	"testing"
	"time"
)

func TestHashPasswordAndVerify(t *testing.T) {
	hash, err := hashPassword("super-secret")
	if err != nil {
		t.Fatalf("hashPassword returned error: %v", err)
	}
	if !verifyPassword("super-secret", hash) {
		t.Fatal("expected password verification to succeed")
	}
	if verifyPassword("wrong", hash) {
		t.Fatal("expected wrong password to fail")
	}
}

func TestLoginLimiterBlocksAfterThreshold(t *testing.T) {
	limiter := &loginLimiter{attempt: map[string][]time.Time{}}
	key := "127.0.0.1"
	for i := 0; i < 10; i++ {
		limiter.add(key)
	}
	if !limiter.blocked(key) {
		t.Fatal("expected limiter to block after ten attempts")
	}
}

func TestHashTokenIsStableAndDoesNotExposeToken(t *testing.T) {
	token := "device-token-secret"
	hash := hashToken(token)
	if hash != hashToken(token) {
		t.Fatal("expected token hash to be stable")
	}
	if hash == token || len(hash) != 64 {
		t.Fatalf("unexpected token hash %q", hash)
	}
}
