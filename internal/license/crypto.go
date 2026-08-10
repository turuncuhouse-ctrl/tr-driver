package license

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// DefaultPublicKeyHex is the TR Driver vendor verify key (keys/public.key).
// Override with LICENSE_PUBLIC_KEY (base64/hex) if you use a different keypair.
const DefaultPublicKeyHex = "3c3b4490455e00e702efa5d6c3cb2365b6431e5912d069744bac7fb3bc2159ed"

// DevRFCPublicKeyHex pairs with DefaultPrivateSeedHex (RFC 8032 sample). Dev/tests only.
const DevRFCPublicKeyHex = "8de0c7c531b9b6983b4177ea8e9ca0f1afa7add693329323087a36de4a2633e0"

// DefaultPrivateSeedHex is ONLY for local/dev signing when LICENSE_ALLOW_DEV_SIGNING=1.
// It pairs with DevRFCPublicKeyHex — not with DefaultPublicKeyHex. Never sell keys signed with it.
const DefaultPrivateSeedHex = "9d61b19deffd5a60ba844af492ec2cc44449c5697b326919655bcc226d7ad573"

type Payload struct {
	Tier       string `json:"tier"`
	MaxUsers   int    `json:"maxUsers"`
	Exp        int64  `json:"exp"` // unix seconds; 0 = no expiry
	Iat        int64  `json:"iat"`
	Customer   string `json:"customer,omitempty"`
	Note       string `json:"note,omitempty"`
	InstanceID string `json:"instanceId,omitempty"` // required for production activations
}

func publicKey() (ed25519.PublicKey, error) {
	if raw := strings.TrimSpace(os.Getenv("LICENSE_PUBLIC_KEY")); raw != "" {
		b, err := decodeKey(raw)
		if err != nil {
			return nil, err
		}
		if len(b) != ed25519.PublicKeySize {
			return nil, errors.New("LICENSE_PUBLIC_KEY must be 32 bytes")
		}
		return ed25519.PublicKey(b), nil
	}
	b, err := hex.DecodeString(DefaultPublicKeyHex)
	if err != nil {
		return nil, err
	}
	return ed25519.PublicKey(b), nil
}

func privateKey() (ed25519.PrivateKey, error) {
	if raw := strings.TrimSpace(os.Getenv("LICENSE_PRIVATE_KEY")); raw != "" {
		b, err := decodeKey(raw)
		if err != nil {
			return nil, err
		}
		switch len(b) {
		case ed25519.SeedSize:
			return ed25519.NewKeyFromSeed(b), nil
		case ed25519.PrivateKeySize:
			return ed25519.PrivateKey(b), nil
		default:
			return nil, errors.New("LICENSE_PRIVATE_KEY must be 32-byte seed or 64-byte private key")
		}
	}
	if os.Getenv("LICENSE_ALLOW_DEV_SIGNING") != "1" {
		return nil, errors.New("LICENSE_PRIVATE_KEY required (or LICENSE_ALLOW_DEV_SIGNING=1 for local test keys)")
	}
	seed, err := hex.DecodeString(DefaultPrivateSeedHex)
	if err != nil {
		return nil, err
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

func decodeKey(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if b, err := base64.StdEncoding.DecodeString(raw); err == nil {
		return b, nil
	}
	if b, err := base64.RawStdEncoding.DecodeString(raw); err == nil {
		return b, nil
	}
	if b, err := hex.DecodeString(raw); err == nil {
		return b, nil
	}
	return nil, errors.New("key must be base64 or hex")
}

func Sign(p Payload) (string, error) {
	priv, err := privateKey()
	if err != nil {
		return "", err
	}
	if p.Iat == 0 {
		p.Iat = time.Now().Unix()
	}
	if _, ok := MaxUsersForTier(p.Tier); !ok {
		return "", fmt.Errorf("unknown tier %q", p.Tier)
	}
	if max, _ := MaxUsersForTier(p.Tier); p.MaxUsers == 0 && p.Tier != TierUnlimited {
		p.MaxUsers = max
	}
	body, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	sig := ed25519.Sign(priv, body)
	return "TRD1." + base64.RawURLEncoding.EncodeToString(body) + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// SignFromRequest builds a license bound to the customer request's instance id.
func SignFromRequest(req *RequestPayload, tier string, years int, customer, note string) (string, error) {
	if req == nil {
		return "", errors.New("request required")
	}
	if tier == "" {
		tier = req.Tier
	}
	maxUsers, ok := MaxUsersForTier(tier)
	if !ok {
		return "", fmt.Errorf("unknown tier %q", tier)
	}
	p := Payload{
		Tier:       tier,
		MaxUsers:   maxUsers,
		Customer:   customer,
		Note:       note,
		InstanceID: req.InstanceID,
		Iat:        time.Now().Unix(),
	}
	if years > 0 {
		p.Exp = time.Now().AddDate(years, 0, 0).Unix()
	}
	return Sign(p)
}

func Verify(key string) (*Payload, error) {
	key = strings.TrimSpace(key)
	parts := strings.Split(key, ".")
	if len(parts) != 3 || parts[0] != "TRD1" {
		return nil, errors.New("invalid license key format")
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("invalid license payload")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, errors.New("invalid license signature")
	}
	pub, err := publicKey()
	if err != nil {
		return nil, err
	}
	if !ed25519.Verify(pub, body, sig) {
		return nil, errors.New("license signature invalid")
	}
	var p Payload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, errors.New("invalid license payload json")
	}
	if _, ok := MaxUsersForTier(p.Tier); !ok {
		return nil, errors.New("unknown license tier")
	}
	if p.Exp > 0 && time.Now().Unix() > p.Exp {
		return nil, errors.New("license expired")
	}
	return &p, nil
}

func IsUsingDefaultPublicKey() bool {
	// Warn only when the insecure RFC sample verify key is active.
	pub, err := publicKey()
	if err != nil {
		return false
	}
	return strings.EqualFold(hex.EncodeToString(pub), DevRFCPublicKeyHex)
}

func CanSignLocally() bool {
	if strings.TrimSpace(os.Getenv("LICENSE_PRIVATE_KEY")) != "" {
		return true
	}
	return os.Getenv("LICENSE_ALLOW_DEV_SIGNING") == "1"
}
