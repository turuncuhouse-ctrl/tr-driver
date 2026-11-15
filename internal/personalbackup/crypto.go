package personalbackup

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

// Key separation labels. Changing any of these is a data migration.
const (
	vaultKeyInfo   = "trdriver/personal-backup/aes-256-gcm/v1"
	vaultIndexInfo = "trdriver/personal-backup/blind-index/v1"
)

var (
	errVaultSecretTooShort = errors.New("personal backup encryption secret must be at least 16 characters")
	errVaultCiphertext     = errors.New("personal backup payload could not be decrypted")
)

// Vault encrypts personal data (contacts / SMS) at rest and derives the
// unlinkable, deterministic row key used for incremental upserts.
//
// Two independent keys are derived from the configured secret with
// HMAC-SHA256 (domain separated):
//   - the AES-256-GCM key that protects the payload,
//   - the blind-index key that turns a client item key into a row key.
//
// The database therefore never stores a phone number, a name, a message body
// or even the client's own item key in clear text.
type Vault struct {
	aead     cipher.AEAD
	indexKey []byte
}

func NewVault(secret string) (*Vault, error) {
	if len(secret) < 16 {
		return nil, errVaultSecretTooShort
	}
	block, err := aes.NewCipher(deriveKey(secret, vaultKeyInfo))
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Vault{aead: aead, indexKey: deriveKey(secret, vaultIndexInfo)}, nil
}

func deriveKey(secret, info string) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(info))
	return mac.Sum(nil)
}

// Seal returns nonce||ciphertext. The additional data binds a payload to one
// user and one backup kind, so a row copied to another account no longer
// decrypts.
func (v *Vault) Seal(plaintext, aad []byte) ([]byte, error) {
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return v.aead.Seal(nonce, nonce, plaintext, aad), nil
}

// Open reverses Seal. Any tampering with the ciphertext, the nonce or the
// additional data fails authentication.
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

// BlindKey returns the deterministic row key for one client item key. The
// mapping is keyed with a server secret, so a database dump cannot be matched
// against a rainbow table of phone numbers.
func (v *Vault) BlindKey(kind, itemKey string) string {
	mac := hmac.New(sha256.New, v.indexKey)
	_, _ = mac.Write([]byte(kind))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(itemKey))
	return hex.EncodeToString(mac.Sum(nil))
}

func aadFor(userID string, kind Kind) []byte {
	return []byte(userID + "\x00" + string(kind))
}
