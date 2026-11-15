package localtel

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Short-lived gateway tokens let the Android client open the public
// `/localtel/ws` WebSocket without exposing its long-lived device token.
// Tokens are stateless HMAC-SHA-256 values bound to the user ID and expiry.
var (
	ErrTokenInvalid = errors.New("invalid localtel gateway token")
	ErrTokenExpired = errors.New("localtel gateway token expired")
)

const (
	tokenParts         = 3
	maxGatewayTokenTTL = 24 * time.Hour
)

type TokenService struct {
	vault *Vault
	ttl   time.Duration
	now   func() time.Time
}

func NewTokenService(vault *Vault, ttl time.Duration) *TokenService {
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	if ttl > maxGatewayTokenTTL {
		ttl = maxGatewayTokenTTL
	}
	return &TokenService{vault: vault, ttl: ttl, now: time.Now}
}

// Issue creates a signed token for userID valid for the configured TTL.
func (t *TokenService) Issue(userID string) (string, time.Time, error) {
	if userID == "" || t.vault == nil {
		return "", time.Time{}, ErrTokenInvalid
	}
	expires := t.now().Add(t.ttl).UTC()
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return "", time.Time{}, err
	}
	payload := strings.Join([]string{
		userID,
		strconv.FormatInt(expires.Unix(), 10),
		base64.RawURLEncoding.EncodeToString(nonce),
	}, "|")
	encoded := base64.RawURLEncoding.EncodeToString([]byte(payload))
	sig := t.vault.MAC([]byte(encoded))
	return encoded + "." + base64.RawURLEncoding.EncodeToString(sig), expires, nil
}

// Validate verifies the signature and expiry and returns the owning user ID.
func (t *TokenService) Validate(token string) (string, time.Time, error) {
	if t == nil || t.vault == nil {
		return "", time.Time{}, ErrTokenInvalid
	}
	dot := strings.LastIndex(token, ".")
	if dot <= 0 || dot == len(token)-1 {
		return "", time.Time{}, ErrTokenInvalid
	}
	encoded, sigRaw := token[:dot], token[dot+1:]
	sig, err := base64.RawURLEncoding.DecodeString(sigRaw)
	if err != nil || !t.vault.VerifyMAC([]byte(encoded), sig) {
		return "", time.Time{}, ErrTokenInvalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", time.Time{}, ErrTokenInvalid
	}
	parts := strings.Split(string(raw), "|")
	if len(parts) != tokenParts || strings.TrimSpace(parts[0]) == "" {
		return "", time.Time{}, ErrTokenInvalid
	}
	expUnix, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
	if err != nil || expUnix <= 0 {
		return "", time.Time{}, ErrTokenInvalid
	}
	expires := time.Unix(expUnix, 0).UTC()
	if !t.now().Before(expires) {
		return "", time.Time{}, ErrTokenExpired
	}
	return parts[0], expires, nil
}
