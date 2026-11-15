package sharedspace

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
	argonTime    = 1
	argonMemory  = 64 * 1024 // KiB
	argonThreads = 4
	argonKeyLen  = 32

	// Upper bounds accepted when verifying a stored hash, so a corrupted row
	// cannot make one request allocate gigabytes.
	argonMaxMemory  = 256 * 1024
	argonMaxTime    = 8
	argonMaxThreads = 16
)

// argonSlots caps concurrent Argon2 computations. Each one holds 64 MiB; without
// a cap, a burst of unlock attempts from many addresses could exhaust memory
// before the per-address limiter notices anything.
var argonSlots = make(chan struct{}, 2)

func hashLinkPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	argonSlots <- struct{}{}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	<-argonSlots
	return fmt.Sprintf("argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

func verifyLinkPassword(password, stored string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 5 || parts[0] != "argon2id" {
		return false
	}
	params := map[string]uint64{}
	for _, item := range strings.Split(parts[2], ",") {
		kv := strings.SplitN(item, "=", 2)
		if len(kv) != 2 {
			return false
		}
		n, err := strconv.ParseUint(kv[1], 10, 32)
		if err != nil {
			return false
		}
		params[kv[0]] = n
	}
	memory, iterations, threads := params["m"], params["t"], params["p"]
	if memory == 0 || iterations == 0 || threads == 0 ||
		memory > argonMaxMemory || iterations > argonMaxTime || threads > argonMaxThreads {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(want) == 0 || len(want) > 64 {
		return false
	}
	argonSlots <- struct{}{}
	got := argon2.IDKey([]byte(password), salt, uint32(iterations), uint32(memory), uint8(threads), uint32(len(want)))
	<-argonSlots
	return subtle.ConstantTimeCompare(got, want) == 1
}

// newLinkToken returns a 256-bit URL-safe token and the hash that is stored.
func newLinkToken() (token, hash string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	token = base64.RawURLEncoding.EncodeToString(buf)
	return token, hashLinkToken(token), nil
}

func hashLinkToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// unlockProof is what the unlock cookie carries after a correct password. It is
// bound to the link and to the current password hash, so changing or removing
// the link invalidates it, and it reveals nothing about the password.
func unlockProof(secret, linkID, passwordHash string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte("trdriver-shared-link:" + linkID + ":" + passwordHash))
	return hex.EncodeToString(mac.Sum(nil))
}

func validUnlockProof(secret, linkID, passwordHash, presented string) bool {
	if presented == "" {
		return false
	}
	expected := unlockProof(secret, linkID, passwordHash)
	return hmac.Equal([]byte(expected), []byte(presented))
}
