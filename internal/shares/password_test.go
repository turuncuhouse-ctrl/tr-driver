package shares

import "testing"

func TestSharePasswordArgon2RoundTrip(t *testing.T) {
	hash, err := hashSharePassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	ok, legacy := verifySharePassword("correct horse", hash, "salt")
	if !ok || legacy {
		t.Fatalf("ok=%v legacy=%v", ok, legacy)
	}
	ok, _ = verifySharePassword("wrong", hash, "salt")
	if ok {
		t.Fatal("wrong password accepted")
	}
}

func TestSharePasswordLegacyUpgradeFlag(t *testing.T) {
	legacy := legacyShareHash("pw", "salt")
	ok, upgrade := verifySharePassword("pw", legacy, "salt")
	if !ok || !upgrade {
		t.Fatalf("ok=%v upgrade=%v", ok, upgrade)
	}
	ok, _ = verifySharePassword("pw", legacy, "other")
	if ok {
		t.Fatal("wrong salt accepted")
	}
}

func TestShareUnlockProofBoundToHash(t *testing.T) {
	proof := shareUnlockProof("secret", "link", "hash1")
	if !validShareUnlockProof("secret", "link", "hash1", proof) {
		t.Fatal("valid proof rejected")
	}
	if validShareUnlockProof("secret", "link", "hash2", proof) {
		t.Fatal("proof not bound to password hash")
	}
	if validShareUnlockProof("secret", "other", "hash1", proof) {
		t.Fatal("proof not bound to link")
	}
}

func legacyShareHash(password, salt string) string {
	// mirror of the pre-Argon2 format for migration tests
	return sha256Hex(salt + ":" + password)
}
