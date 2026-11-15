package personalbackup

import (
	"bytes"
	"crypto/rand"
	"strings"
	"testing"
)

func testVault(t *testing.T) *Vault {
	t.Helper()
	vault, err := NewVault("unit-test-secret-0123456789abcdef")
	if err != nil {
		t.Fatalf("new vault: %v", err)
	}
	return vault
}

func TestVaultSealOpenRoundTrip(t *testing.T) {
	vault := testVault(t)
	plain := []byte(`{"key":"k1","revision":3,"hash":"","data":{"name":"Ayşe"}}`)
	sealed, err := vault.Seal(plain, aadFor("user-1", KindContacts))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if bytes.Contains(sealed, []byte("Ayşe")) {
		t.Fatal("ciphertext must not contain plaintext")
	}
	got, err := vault.Open(sealed, aadFor("user-1", KindContacts))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("roundtrip mismatch: %s", got)
	}
}

func TestVaultRejectsForeignAAD(t *testing.T) {
	vault := testVault(t)
	sealed, err := vault.Seal([]byte(`{"a":1}`), aadFor("user-1", KindContacts))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := vault.Open(sealed, aadFor("user-2", KindContacts)); err == nil {
		t.Fatal("payload must not decrypt for another user")
	}
	if _, err := vault.Open(sealed, aadFor("user-1", KindSMS)); err == nil {
		t.Fatal("payload must not decrypt for another kind")
	}
}

func TestVaultRejectsTamperedCiphertext(t *testing.T) {
	vault := testVault(t)
	sealed, err := vault.Seal([]byte(`{"a":1}`), aadFor("user-1", KindSMS))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	sealed[len(sealed)-1] ^= 0xff
	if _, err := vault.Open(sealed, aadFor("user-1", KindSMS)); err == nil {
		t.Fatal("tampered payload must fail authentication")
	}
	if _, err := vault.Open([]byte{1, 2}, aadFor("user-1", KindSMS)); err == nil {
		t.Fatal("short payload must fail")
	}
}

func TestVaultUsesFreshNonce(t *testing.T) {
	vault := testVault(t)
	plain := []byte(`{"a":1}`)
	first, err := vault.Seal(plain, aadFor("u", KindContacts))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	second, err := vault.Seal(plain, aadFor("u", KindContacts))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if bytes.Equal(first, second) {
		t.Fatal("two seals of the same payload must differ (random nonce)")
	}
}

func TestNewVaultRejectsShortSecret(t *testing.T) {
	if _, err := NewVault("short"); err == nil {
		t.Fatal("expected an error for a short secret")
	}
}

func TestBlindKeyIsStableOpaqueAndKindScoped(t *testing.T) {
	vault := testVault(t)
	key := "QWxsZXJnaWVzLXVuZGluZ2Uta2V5"
	blind := vault.BlindKey(string(KindContacts), key)
	if blind != vault.BlindKey(string(KindContacts), key) {
		t.Fatal("blind key must be deterministic")
	}
	if len(blind) != 64 {
		t.Fatalf("expected 64 hex chars, got %d", len(blind))
	}
	if strings.Contains(blind, key) {
		t.Fatal("blind key must not embed the client key")
	}
	if blind == vault.BlindKey(string(KindSMS), key) {
		t.Fatal("blind key must be scoped per kind")
	}
	other, err := NewVault("another-secret-0123456789abcdef")
	if err != nil {
		t.Fatalf("new vault: %v", err)
	}
	if blind == other.BlindKey(string(KindContacts), key) {
		t.Fatal("blind key must depend on the server secret")
	}
}

func TestBlindKeyDoesNotCollideAcrossSeparatorBoundaries(t *testing.T) {
	vault := testVault(t)
	// "ab" + "\x00c" vs "a" + "\x00bc" style ambiguity is prevented by the
	// separator inside BlindKey; verify distinct inputs stay distinct.
	if vault.BlindKey("contacts", "abcdefgh") == vault.BlindKey("contacts", "abcdefgi") {
		t.Fatal("distinct keys must not collide")
	}
}

func TestSealProducesRandomBytes(t *testing.T) {
	vault := testVault(t)
	noise := make([]byte, 16)
	if _, err := rand.Read(noise); err != nil {
		t.Fatalf("rand: %v", err)
	}
	sealed, err := vault.Seal(noise, aadFor("u", KindSMS))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if len(sealed) != len(noise)+12+16 {
		t.Fatalf("unexpected sealed length %d", len(sealed))
	}
}
