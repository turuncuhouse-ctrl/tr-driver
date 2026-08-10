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

	key, err := Sign(Payload{Tier: TierSmall, MaxUsers: 10, Exp: time.Now().Add(24 * time.Hour).Unix(), Customer: "test", InstanceID: "inst-1"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := Verify(key)
	if err != nil {
		t.Fatal(err)
	}
	if p.Tier != TierSmall || p.MaxUsers != 10 || p.InstanceID != "inst-1" {
		t.Fatalf("unexpected payload: %+v", p)
	}
}

func TestVerifyRejectsTamper(t *testing.T) {
	t.Setenv("LICENSE_ALLOW_DEV_SIGNING", "1")
	key, err := Sign(Payload{Tier: TierPersonal, MaxUsers: 1, InstanceID: "x"})
	if err != nil {
		t.Fatal(err)
	}
	tampered := key[:len(key)-4] + "AAAA"
	if _, err := Verify(tampered); err == nil {
		t.Fatal("expected verify failure")
	}
}

func TestRequestRoundTripAndSign(t *testing.T) {
	t.Setenv("LICENSE_ALLOW_DEV_SIGNING", "1")
	code, err := EncodeRequest(RequestPayload{
		InstanceID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		Tier:       TierMedium,
		MaxUsers:   50,
		UserCount:  3,
		CreatedAt:  time.Now().Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := ParseRequest(code)
	if err != nil {
		t.Fatal(err)
	}
	key, err := SignFromRequest(req, "", 1, "Acme", "")
	if err != nil {
		t.Fatal(err)
	}
	p, err := Verify(key)
	if err != nil {
		t.Fatal(err)
	}
	if p.InstanceID != req.InstanceID || p.Tier != TierMedium {
		t.Fatalf("bad issued license: %+v", p)
	}
}
