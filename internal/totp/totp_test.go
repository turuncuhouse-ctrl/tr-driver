package totp

import (
	"testing"
	"time"
)

func TestRFC6238Vector(t *testing.T) {
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ" // ASCII "12345678901234567890"
	code, err := Code(secret, 59/Period)
	if err != nil {
		t.Fatal(err)
	}
	if code != "287082" {
		t.Fatalf("got %s", code)
	}
}

func TestWindowAndReplay(t *testing.T) {
	secret, err := GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(120, 0)
	code, err := Code(secret, now.Unix()/Period)
	if err != nil {
		t.Fatal(err)
	}
	counter, ok := Match(secret, code, now, -1)
	if !ok {
		t.Fatal("valid code rejected")
	}
	if _, ok := Match(secret, code, now, counter); ok {
		t.Fatal("replayed code accepted")
	}
	if _, ok := Match(secret, "000000", now, -1); ok {
		t.Fatal("invalid code accepted")
	}
}

func TestSealOpen(t *testing.T) {
	sealed, err := Seal("JBSWY3DPEHPK3PXP", "server-secret")
	if err != nil {
		t.Fatal(err)
	}
	got, err := Open(sealed, "server-secret")
	if err != nil || got != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("got %q err %v", got, err)
	}
	if _, err := Open(sealed, "other"); err == nil {
		t.Fatal("wrong key accepted")
	}
}
