package localtel

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Optional FCM high-priority pushes (Stage 2). Everything here is disabled
// unless the operator configures FIREBASE_SERVICE_ACCOUNT; tokens and push
// payloads never appear in logs.

const (
	fcmTokenURL    = "https://oauth2.googleapis.com/token"
	fcmMessageURL  = "https://fcm.googleapis.com/v1"
	fcmScopes      = "https://www.googleapis.com/auth/firebase.messaging"
	fcmHTTPTimeout = 6 * time.Second
)

type fcmSender struct {
	projectID   string
	clientEmail string
	key         *rsa.PrivateKey

	httpClient *http.Client
	tokenURL   string
	messageURL string

	mu          sync.Mutex
	accessToken string
	tokenExpiry time.Time
}

// serviceAccountKey is the subset of a Firebase service-account JSON file the
// sender needs.
type serviceAccountKey struct {
	ProjectID   string `json:"project_id"`
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
}

// newFCMSender parses a Firebase service-account JSON blob. Any problem
// returns nil: the feature is optional and must never break calling.
func newFCMSender(serviceAccountJSON string) *fcmSender {
	trimmed := strings.TrimSpace(serviceAccountJSON)
	if trimmed == "" {
		return nil
	}
	var key serviceAccountKey
	if err := json.Unmarshal([]byte(trimmed), &key); err != nil {
		log.Printf("localtel fcm: service account JSON is invalid, push disabled")
		return nil
	}
	if key.ProjectID == "" || key.ClientEmail == "" || key.PrivateKey == "" {
		log.Printf("localtel fcm: service account is missing fields, push disabled")
		return nil
	}
	privateKey, err := parseRSAPrivateKey(key.PrivateKey)
	if err != nil {
		log.Printf("localtel fcm: private key unusable, push disabled")
		return nil
	}
	return &fcmSender{
		projectID:   key.ProjectID,
		clientEmail: key.ClientEmail,
		key:         privateKey,
		httpClient:  &http.Client{Timeout: fcmHTTPTimeout},
		tokenURL:    fcmTokenURL,
		messageURL:  fcmMessageURL,
	}
}

func parseRSAPrivateKey(raw string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(strings.ReplaceAll(raw, `\n`, "\n")))
	if block == nil {
		return nil, errors.New("no PEM block")
	}
	if parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if key, ok := parsed.(*rsa.PrivateKey); ok {
			return key, nil
		}
		return nil, errors.New("not an RSA key")
	}
	return x509.ParsePKCS1PrivateKey(block.Bytes)
}

// accessToken exchanges the service-account JWT for an OAuth access token,
// caching it until shortly before expiry.
func (f *fcmSender) fetchAccessToken(ctx context.Context) (string, error) {
	f.mu.Lock()
	if f.accessToken != "" && time.Now().Before(f.tokenExpiry.Add(-time.Minute)) {
		token := f.accessToken
		f.mu.Unlock()
		return token, nil
	}
	f.mu.Unlock()

	now := time.Now()
	claims := map[string]any{
		"iss":   f.clientEmail,
		"scope": fcmScopes,
		"aud":   f.tokenURL,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	}
	assertion, err := signJWT(claims, f.key)
	if err != nil {
		return "", err
	}
	form := strings.NewReader("grant_type=urn:ietf:params:oauth:grant-type:jwt-bearer&assertion=" + assertion)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.tokenURL, form)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := f.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// The body can echo request details; only the status is safe to log.
		return "", fmt.Errorf("token endpoint status %d", resp.StatusCode)
	}
	var payload struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	if payload.AccessToken == "" {
		return "", errors.New("empty access token")
	}
	if payload.ExpiresIn <= 0 {
		payload.ExpiresIn = 3600
	}
	f.mu.Lock()
	f.accessToken = payload.AccessToken
	f.tokenExpiry = now.Add(time.Duration(payload.ExpiresIn) * time.Second)
	f.mu.Unlock()
	return payload.AccessToken, nil
}

// signJWT produces a compact RS256 JWT with the stdlib only.
func signJWT(claims map[string]any, key *rsa.PrivateKey) (string, error) {
	header, err := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	unsigned := enc.EncodeToString(header) + "." + enc.EncodeToString(body)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return unsigned + "." + enc.EncodeToString(signature), nil
}

// send delivers one high-priority data message. The payload carries only what
// the ringing screen needs: call id and caller identity (same fields the
// WebSocket invite already carries).
func (f *fcmSender) send(ctx context.Context, token string, data map[string]string) error {
	accessToken, err := f.fetchAccessToken(ctx)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]any{
		"message": map[string]any{
			"token": token,
			"data":  data,
			"android": map[string]any{
				"priority": "HIGH",
				"ttl":      "45s",
			},
		},
	})
	if err != nil {
		return err
	}
	url := f.messageURL + "/projects/" + f.projectID + "/messages:send"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fcm send status %d", resp.StatusCode)
	}
	return nil
}

// PushInvite best-effort wakes the callee's devices with a high-priority data
// push. Returns how many tokens accepted the message (0 = feature off or no
// tokens). It never blocks the caller for long and logs no personal data.
func (s *Service) PushInvite(ctx context.Context, calleeUserID string, msg SignalMessage) int {
	if s.fcm == nil || s.db == nil || calleeUserID == "" {
		return 0
	}
	rows, err := s.db.Query(ctx, `
		select token from localtel_fcm_tokens
		where user_id = $1::uuid
		order by updated_at desc
		limit 5`, calleeUserID)
	if err != nil {
		log.Printf("localtel fcm: token lookup failed")
		return 0
	}
	var tokens []string
	for rows.Next() {
		var token string
		if err := rows.Scan(&token); err == nil && token != "" {
			tokens = append(tokens, token)
		}
	}
	rows.Close()
	if len(tokens) == 0 {
		return 0
	}
	data := map[string]string{
		"type":        "localtel_invite",
		"callId":      msg.CallID,
		"number":      msg.Number,
		"displayName": msg.DisplayName,
	}
	// Fire and forget: the signal path must not wait on Google. Returning the
	// token count tells routeSignal that a wake may still arrive, so an
	// offline WebSocket no longer means an instant callee_offline reject.
	for _, token := range tokens {
		go func(token string) {
			sendCtx, cancel := context.WithTimeout(context.Background(), fcmHTTPTimeout)
			defer cancel()
			if err := s.fcm.send(sendCtx, token, data); err != nil {
				// The error never contains the token or payload.
				log.Printf("localtel fcm: push failed: %v", err)
				if strings.Contains(err.Error(), "status 404") || strings.Contains(err.Error(), "status 410") {
					// Unregistered token: forget it.
					_, _ = s.db.Exec(context.Background(),
						`delete from localtel_fcm_tokens where token = $1`, token)
				}
			}
		}(token)
	}
	return len(tokens)
}

// SaveFCMToken stores (or refreshes) a device's push token, keeping only the
// most recent few per user.
func (s *Service) SaveFCMToken(ctx context.Context, userID, deviceID, token string) error {
	if s.db == nil {
		return errors.New("fcm storage unavailable")
	}
	if userID == "" || token == "" || len(token) > 4096 {
		return errors.New("invalid fcm token")
	}
	for _, r := range token {
		if r < 0x20 || r == 0x7f {
			return errors.New("invalid fcm token")
		}
	}
	_, err := s.db.Exec(ctx, `
		insert into localtel_fcm_tokens (token, user_id, device_id, updated_at)
		values ($1, $2::uuid, $3, now())
		on conflict (token) do update set user_id = excluded.user_id,
			device_id = excluded.device_id, updated_at = now()`,
		token, userID, deviceID)
	if err != nil {
		return err
	}
	// Keep the newest five tokens per user.
	_, err = s.db.Exec(ctx, `
		delete from localtel_fcm_tokens where user_id = $1::uuid and token not in (
			select token from localtel_fcm_tokens where user_id = $1::uuid
			order by updated_at desc limit 5)`, userID)
	return err
}
