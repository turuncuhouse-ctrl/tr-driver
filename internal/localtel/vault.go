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
// rest using AES-256-GCM bound to the owning user ID.
type Vault struct {
	aead cipher.AEAD
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
	return &Vault{aead: aead}, nil
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
