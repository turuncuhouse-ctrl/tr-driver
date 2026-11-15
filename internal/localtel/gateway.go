package localtel

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// GatewayView is the complete zero-config answer for one authenticated user:
// the WSS gateway address derived from the TR Driver domain, the auto-created
// calling account, the encrypted-at-rest password (returned only to its owner
// over an authenticated, no-store response) and a short-lived WS token.
type GatewayView struct {
	WSSUrl         string      `json:"wssUrl"`
	Host           string      `json:"host"`
	Domain         string      `json:"domain"`
	Username       string      `json:"username"`
	Extension      string      `json:"extension"`
	Secret         string      `json:"secret"`
	Token          string      `json:"token"`
	TokenExpiresAt *time.Time  `json:"tokenExpiresAt,omitempty"`
	StunUrls       []string    `json:"stunUrls"`
	IceServers     []IceServer `json:"iceServers"`
	Turn           *TurnConfig `json:"turn,omitempty"`
}

// IceServer mirrors the WebRTC-ish description the client may use. The gateway
// relays media through TR Driver itself, so the default list is empty; an
// operator may still configure STUN/TURN through the environment.
type IceServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

// TurnConfig describes an optional relay. Nil means media stays server-relayed.
type TurnConfig struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username"`
	Credential string   `json:"credential"`
}

const (
	ModeAuto   = "auto"
	ModeManual = "manual"
)

// GetGatewayConfig returns everything the app needs to place and receive calls.
//
// Manual SIP/gateway configuration stored through PUT /api/localtel/config
// still wins (advanced users); everyone else gets a server-provisioned account
// on the TR Driver WSS gateway with no setup at all.
func (s *Service) GetGatewayConfig(ctx context.Context, userID, requestHost string) (*ConfigView, error) {
	if userID == "" {
		return nil, fmt.Errorf("%w: user required", ErrInvalidInput)
	}
	manual, manualUpdatedAt, manualErr := s.loadStored(ctx, userID)
	if manualErr != nil && !errors.Is(manualErr, ErrNotFound) {
		return nil, manualErr
	}
	haveManual := manualErr == nil && manual.Enabled

	if s.autoProvisionEnabled() {
		if _, err := s.EnsureAccount(ctx, userID); err != nil {
			return nil, err
		}
	}

	token, expiresAt, err := s.tokens.Issue(userID)
	if err != nil {
		return nil, err
	}
	host := s.publicHost(requestHost)
	// The public transport is always WSS, even when an upstream proxy
	// terminates TLS before the Go process.
	wssURL := "wss://" + host + GatewayPath

	if haveManual {
		view := toView(manual, &manualUpdatedAt)
		view.Mode = ModeManual
		view.AutoProvisioned = s.autoProvisionEnabled()
		if manual.ProviderType == ProviderGateway {
			view.Gateway = &GatewayView{
				WSSUrl:         manualGatewayURL(manual, wssURL),
				Host:           host,
				Domain:         manual.SIPDomain,
				Username:       manual.Username,
				Extension:      manual.Extension,
				Secret:         manual.Secret,
				Token:          token,
				TokenExpiresAt: &expiresAt,
				StunUrls:       []string{},
				IceServers:     []IceServer{},
			}
		}
		return view, nil
	}

	account, err := s.Account(ctx, userID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	view := &ConfigView{
		Configured:         true,
		Enabled:            true,
		ProviderType:       ProviderGateway,
		SIPDomain:          account.Domain,
		SIPPort:            443,
		Transport:          TransportWSS,
		Username:           account.Username,
		Extension:          account.Extension,
		DisplayName:        account.DisplayName,
		DefaultCountryCode: "90",
		AutoRegister:       true,
		HasSecret:          true,
		UpdatedAt:          &now,
		Mode:               ModeAuto,
		AutoProvisioned:    true,
		Gateway: &GatewayView{
			WSSUrl:         wssURL,
			Host:           host,
			Domain:         account.Domain,
			Username:       account.Username,
			Extension:      account.Extension,
			Secret:         account.Password,
			Token:          token,
			TokenExpiresAt: &expiresAt,
			StunUrls:       []string{},
			IceServers:     []IceServer{},
		},
	}
	return view, nil
}

// GatewayPath is the public, token-authenticated WebSocket endpoint. It lives
// on the same host/port as the rest of TR Driver (no extra NPM/DNS/port).
const GatewayPath = "/localtel/ws"

func (s *Service) publicHost(requestHost string) string {
	if domain := strings.TrimSpace(s.domain); domain != "" {
		return domain
	}
	host := strings.TrimSpace(requestHost)
	if host == "" {
		return "localhost"
	}
	return host
}

func manualGatewayURL(cfg storedConfig, fallback string) string {
	raw := strings.TrimSpace(cfg.ServerURL)
	if raw == "" {
		return fallback
	}
	switch {
	case strings.HasPrefix(raw, "wss://"), strings.HasPrefix(raw, "ws://"):
		return raw
	case strings.HasPrefix(raw, "https://"):
		return "wss://" + strings.TrimPrefix(raw, "https://")
	case strings.HasPrefix(raw, "http://"):
		return "ws://" + strings.TrimPrefix(raw, "http://")
	}
	return fallback
}

// ValidateToken checks a short-lived gateway token and confirms the user still
// exists, so a token cannot outlive a deleted account.
func (s *Service) ValidateToken(ctx context.Context, token string) (string, time.Time, error) {
	userID, expiresAt, err := s.tokens.Validate(token)
	if err != nil {
		return "", time.Time{}, err
	}
	var exists bool
	if err := s.db.QueryRow(ctx, `select exists(select 1 from users where id = $1::uuid)`, userID).Scan(&exists); err != nil {
		return "", time.Time{}, err
	}
	if !exists {
		return "", time.Time{}, ErrTokenInvalid
	}
	return userID, expiresAt, nil
}
