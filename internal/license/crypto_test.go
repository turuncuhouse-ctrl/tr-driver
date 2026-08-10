package license

import (
	"os"
	"testing"
	"time"
)

func TestSignVerifyRoundTrip(t *testing.T) {
	t.Setenv("LICENSE_ALLOW_DEV_SIGNING", "1")
	_ = os.Unsetenv("LICENSE_PUBLIC_KEY")
	_ = os.Unsetenv("LICENSE_PRIVATE_KEY")

	key, err := Sign(Payload{Tier: TierSmall, MaxUsers: 10, Exp: time.Now().Add(24 * time.Hour).Unix(), Customer: "test"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := Verify(key)
	if err != nil {
		t.Fatal(err)
	}
	if p.Tier != TierSmall || p.MaxUsers != 10 {
		t.Fatalf("unexpected payload: %+v", p)
	}
}

func TestVerifyRejectsTamper(t *testing.T) {
	t.Setenv("LICENSE_ALLOW_DEV_SIGNING", "1")
	key, err := Sign(Payload{Tier: TierPersonal, MaxUsers: 1})
	if err != nil {
		t.Fatal(err)
	}
	tampered := key[:len(key)-4] + "AAAA"
	if _, err := Verify(tampered); err == nil {
		t.Fatal("expected verify failure")
	}
}
