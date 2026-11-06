package shares

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	shareArgonTime    = 1
	shareArgonMemory  = 64 * 1024
	shareArgonThreads = 4
	shareArgonKeyLen  = 32
)

func hashSharePassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, shareArgonTime, shareArgonMemory, shareArgonThreads, shareArgonKeyLen)
	return fmt.Sprintf("argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		shareArgonMemory, shareArgonTime, shareArgonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// verifySharePassword accepts current Argon2id hashes and legacy SHA-256 hashes.
// The second result reports that a legacy hash should be upgraded.
func verifySharePassword(password, stored, legacySalt string) (bool, bool) {
	if strings.HasPrefix(stored, "argon2id$") {
		parts := strings.Split(stored, "$")
		if len(parts) != 5 {
			return false, false
		}
		params := map[string]uint64{}
		for _, item := range strings.Split(parts[2], ",") {
			kv := strings.SplitN(item, "=", 2)
			if len(kv) != 2 {
				return false, false
			}
			n, err := strconv.ParseUint(kv[1], 10, 32)
			if err != nil {
				return false, false
			}
			params[kv[0]] = n
		}
		memory, time, threads := params["m"], params["t"], params["p"]
		if memory == 0 || time == 0 || threads == 0 {
			return false, false
		}
		salt, err := base64.RawStdEncoding.DecodeString(parts[3])
		if err != nil {
			return false, false
		}
		want, err := base64.RawStdEncoding.DecodeString(parts[4])
		if err != nil {
			return false, false
		}
		got := argon2.IDKey([]byte(password), salt, uint32(time), uint32(memory), uint8(threads), uint32(len(want)))
		return subtle.ConstantTimeCompare(got, want) == 1, false
	}
	sum := sha256.Sum256([]byte(legacySalt + ":" + password))
	return subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(stored)) == 1, true
}

func shareUnlockProof(secret, linkID, passwordHash string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte("trdriver-share:" + linkID + ":" + passwordHash))
	return hex.EncodeToString(mac.Sum(nil))
}

func validShareUnlockProof(secret, linkID, passwordHash, proof string) bool {
	if passwordHash == "" || proof == "" {
		return false
	}
	want := shareUnlockProof(secret, linkID, passwordHash)
	return hmac.Equal([]byte(want), []byte(proof))
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
