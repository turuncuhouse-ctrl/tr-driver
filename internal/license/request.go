package license

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// RequestPayload is created on the customer instance and sent to the vendor.
type RequestPayload struct {
	InstanceID string `json:"instanceId"`
	Tier       string `json:"tier"`
	MaxUsers   int    `json:"maxUsers"`
	UserCount  int    `json:"userCount"`
	Product    string `json:"product"`
	Version    string `json:"version,omitempty"`
	CreatedAt  int64  `json:"createdAt"`
}

// EncodeRequest builds TRDR1.<payload>.<checksum> (tamper-evident, not secret).
func EncodeRequest(p RequestPayload) (string, error) {
	if strings.TrimSpace(p.InstanceID) == "" {
		return "", errors.New("instance id required")
	}
	if _, ok := MaxUsersForTier(p.Tier); !ok {
		return "", fmt.Errorf("unknown tier %q", p.Tier)
	}
	if p.CreatedAt == 0 {
		p.CreatedAt = time.Now().Unix()
	}
	if p.Product == "" {
		p.Product = "TR Driver"
	}
	body, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return "TRDR1." + base64.RawURLEncoding.EncodeToString(body) + "." + hex.EncodeToString(sum[:8]), nil
}

func ParseRequest(code string) (*RequestPayload, error) {
	code = strings.TrimSpace(code)
	parts := strings.Split(code, ".")
	if len(parts) != 3 || parts[0] != "TRDR1" {
		return nil, errors.New("invalid license request code (expected TRDR1...)")
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("invalid license request payload")
	}
	sum := sha256.Sum256(body)
	want := hex.EncodeToString(sum[:8])
	if !strings.EqualFold(want, parts[2]) {
		return nil, errors.New("license request checksum mismatch")
	}
	var p RequestPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, errors.New("invalid license request json")
	}
	if p.InstanceID == "" {
		return nil, errors.New("request missing instance id")
	}
	if _, ok := MaxUsersForTier(p.Tier); !ok {
		return nil, errors.New("request has unknown tier")
	}
	// Requests older than 30 days rejected to reduce replay of stale codes.
	if p.CreatedAt > 0 && time.Now().Unix()-p.CreatedAt > 30*24*3600 {
		return nil, errors.New("license request expired; generate a new one")
	}
	return &p, nil
}
