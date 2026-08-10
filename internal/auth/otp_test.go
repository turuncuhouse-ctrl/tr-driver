package auth

import "testing"

func TestOTPHashRoundTrip(t *testing.T) {
	code, err := generateOTPCode()
	if err != nil {
		t.Fatal(err)
	}
	if len(code) != 6 {
		t.Fatalf("expected 6 digits, got %q", code)
	}
	h1 := hashOTP(code)
	h2 := hashOTP(code)
	if h1 != h2 {
		t.Fatal("hash not stable")
	}
	if hashOTP("000000") == hashOTP("000001") {
		t.Fatal("different codes should hash differently")
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
