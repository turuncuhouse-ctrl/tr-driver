package localtel

import (
	"strings"
	"testing"
	"time"
)

func TestGatewayTokenIssueValidateAndExpiry(t *testing.T) {
	vault := testVault(t)
	service := NewTokenService(vault, time.Minute)
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }

	token, expiresAt, err := service.Issue("user-42")
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	if !expiresAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("unexpected expiry: %s", expiresAt)
	}
	userID, gotExpiry, err := service.Validate(token)
	if err != nil || userID != "user-42" || !gotExpiry.Equal(expiresAt) {
		t.Fatalf("validate token: user=%q expiry=%s err=%v", userID, gotExpiry, err)
	}

	parts := strings.Split(token, ".")
	parts[0] = parts[0] + "x"
	if _, _, err := service.Validate(strings.Join(parts, ".")); err == nil {
		t.Fatal("tampered token was accepted")
	}

	now = now.Add(time.Minute)
	if _, _, err := service.Validate(token); err != ErrTokenExpired {
		t.Fatalf("expected expired token, got %v", err)
	}
}
