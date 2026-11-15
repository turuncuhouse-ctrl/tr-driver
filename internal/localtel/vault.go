package localtel

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
)

const vaultKeyInfo = "trdriver/localtel/aes-256-gcm/v1"

var (
	errVaultSecretTooShort = errors.New("localtel encryption secret must be at least 16 characters")
	errVaultCiphertext     = errors.New("localtel config could not be decrypted")
)

// Vault encrypts per-user LocalTel SIP/gateway configuration and credentials at
// rest using AES-256-GCM bound to the owning user ID. It also derives an HMAC
// key for short-lived gateway tokens.
type Vault struct {
	aead   cipher.AEAD
	macKey []byte
}

func NewVault(secret string) (*Vault, error) {
	if len(secret) < 16 {
		return nil, errVaultSecretTooShort
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(vaultKeyInfo))
	key := mac.Sum(nil)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	macKey := hmac.New(sha256.New, []byte(secret))
	_, _ = macKey.Write([]byte(vaultKeyInfo + "/token-mac"))
	return &Vault{aead: aead, macKey: macKey.Sum(nil)}, nil
}

// MAC returns a keyed HMAC-SHA-256 over data. Used to sign short-lived gateway
// tokens; the signature never leaves the server in logs.
func (v *Vault) MAC(data []byte) []byte {
	if v == nil {
		return nil
	}
	m := hmac.New(sha256.New, v.macKey)
	_, _ = m.Write(data)
	return m.Sum(nil)
}

// VerifyMAC reports whether mac is a valid MAC for data.
func (v *Vault) VerifyMAC(data, mac []byte) bool {
	if v == nil {
		return false
	}
	expected := v.MAC(data)
	return hmac.Equal(expected, mac)
}

func (v *Vault) Seal(plaintext, aad []byte) ([]byte, error) {
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return v.aead.Seal(nonce, nonce, plaintext, aad), nil
}

func (v *Vault) Open(blob, aad []byte) ([]byte, error) {
	if len(blob) < v.aead.NonceSize() {
		return nil, errVaultCiphertext
	}
	nonce, ciphertext := blob[:v.aead.NonceSize()], blob[v.aead.NonceSize():]
	plaintext, err := v.aead.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return nil, errVaultCiphertext
	}
	return plaintext, nil
}

func aadFor(userID string) []byte {
	return []byte(userID + "\x00localtel")
}
