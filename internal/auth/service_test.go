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

func TestLoginLimiterSweepsExpiredKeys(t *testing.T) {
	l := &loginLimiter{attempt: map[string][]time.Time{}}
	old := time.Now().Add(-20 * time.Minute)
	l.attempt["stale"] = []time.Time{old}
	l.attempt["fresh"] = []time.Time{time.Now()}
	l.sweepLocked(time.Now())
	if _, ok := l.attempt["stale"]; ok {
		t.Fatal("expected stale key to be removed")
	}
	if _, ok := l.attempt["fresh"]; !ok {
		t.Fatal("expected fresh key to remain")
	}
}
