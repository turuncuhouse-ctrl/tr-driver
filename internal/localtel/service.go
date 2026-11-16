// Package localtel stores encrypted per-user LocalTel IP-phone/SIP provider
// configuration and provides an authenticated per-user WebSocket/HTTP signaling
// relay for gateway-mode calls.
package localtel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	ProviderSIP     = "sip"
	ProviderGateway = "gateway"

	TransportUDP   = "udp"
	TransportTCP   = "tcp"
	TransportTLS   = "tls"
	TransportWSS   = "wss"
	TransportHTTPS = "https"

	maxConfigBodyBytes = 32 << 10
	maxServerURLLen    = 256
	maxDomainLen       = 253
	maxUsernameLen     = 128
	maxExtensionLen    = 32
	maxDisplayNameLen  = 80
	maxPrefixLen       = 16
	maxSecretLen       = 512
)

var (
	ErrInvalidInput = errors.New("invalid localtel configuration")
	ErrNotFound     = errors.New("localtel configuration not found")
)

type ConfigInput struct {
	Enabled            bool   `json:"enabled"`
	ProviderType       string `json:"providerType"`
	ServerURL          string `json:"serverUrl"`
	SIPDomain          string `json:"sipDomain,omitempty"`
	SIPPort            int    `json:"sipPort,omitempty"`
	Transport          string `json:"transport,omitempty"`
	Username           string `json:"username"`
	Extension          string `json:"extension,omitempty"`
	DisplayName        string `json:"displayName,omitempty"`
	OutboundPrefix     string `json:"outboundPrefix,omitempty"`
	DefaultCountryCode string `json:"defaultCountryCode,omitempty"`
	AutoRegister       bool   `json:"autoRegister"`
	Secret             string `json:"secret,omitempty"`
	ClearSecret        bool   `json:"clearSecret,omitempty"`
}

// ConfigView is safe to return to UI clients: it reports whether a secret is
// stored without ever exposing the raw password or token.
type ConfigView struct {
	Configured         bool       `json:"configured"`
	Enabled            bool       `json:"enabled"`
	ProviderType       string     `json:"providerType"`
	ServerURL          string     `json:"serverUrl"`
	SIPDomain          string     `json:"sipDomain"`
	SIPPort            int        `json:"sipPort"`
	Transport          string     `json:"transport"`
	Username           string     `json:"username"`
	Extension          string     `json:"extension"`
	DisplayName        string     `json:"displayName"`
	OutboundPrefix     string     `json:"outboundPrefix"`
	DefaultCountryCode string     `json:"defaultCountryCode"`
	AutoRegister       bool       `json:"autoRegister"`
	HasSecret          bool       `json:"hasSecret"`
	UpdatedAt          *time.Time `json:"updatedAt,omitempty"`
	// Mode is "auto" (server-provisioned gateway account) or "manual".
	Mode            string       `json:"mode"`
	AutoProvisioned bool         `json:"autoProvisioned"`
	Gateway         *GatewayView `json:"gateway,omitempty"`
}

// ConfigProvision includes the decrypted secret for authenticated device
// provisioning over TLS with Cache-Control: no-store.
type ConfigProvision struct {
	ConfigView
	Secret string `json:"secret"`
}

type storedConfig struct {
	Enabled            bool   `json:"enabled"`
	ProviderType       string `json:"providerType"`
	ServerURL          string `json:"serverUrl"`
	SIPDomain          string `json:"sipDomain"`
	SIPPort            int    `json:"sipPort"`
	Transport          string `json:"transport"`
	Username           string `json:"username"`
	Extension          string `json:"extension"`
	DisplayName        string `json:"displayName"`
	OutboundPrefix     string `json:"outboundPrefix"`
	DefaultCountryCode string `json:"defaultCountryCode"`
	AutoRegister       bool   `json:"autoRegister"`
	Secret             string `json:"secret"`
}

type Service struct {
	db            *pgxpool.Pool
	vault         *Vault
	domain        string
	tokens        *TokenService
	autoProvision bool
	// fcm sends optional high-priority wake pushes when the server has
	// Firebase credentials configured; nil disables the feature.
	fcm *fcmSender
	// Test-only overrides used by the signaling e2e tests in this package.
	resolveForTest  func(ctx context.Context, dial string) (string, bool)
	identityForTest func(ctx context.Context, userID string) (string, string, bool)
}

func NewService(db *pgxpool.Pool, vault *Vault) (*Service, error) {
	if vault == nil {
		return nil, errors.New("localtel encryption vault is required")
	}
	return &Service{
		db:            db,
		vault:         vault,
		tokens:        NewTokenService(vault, 15*time.Minute),
		autoProvision: true,
	}, nil
}

// SetFCM enables optional high-priority wake pushes when a Firebase
// service-account JSON is configured; an empty or invalid value keeps the
// feature off (calls work exactly as before).
func (s *Service) SetFCM(serviceAccountJSON string) {
	s.fcm = newFCMSender(serviceAccountJSON)
}

// Configure sets the public gateway domain, short-lived token TTL and whether
// calling accounts are auto-provisioned. A zero token TTL keeps the default.
func (s *Service) Configure(domain string, tokenTTL time.Duration, autoProvision bool) {
	s.domain = strings.TrimSpace(domain)
	if tokenTTL > 0 {
		s.tokens = NewTokenService(s.vault, tokenTTL)
	}
	s.autoProvision = autoProvision
}

// autoProvisionEnabled reports whether new users get a server-side account.
func (s *Service) autoProvisionEnabled() bool {
	return s.autoProvision
}

func defaultView() *ConfigView {
	return &ConfigView{
		Configured:         false,
		Enabled:            false,
		ProviderType:       ProviderSIP,
		SIPPort:            5060,
		Transport:          TransportUDP,
		DefaultCountryCode: "90",
	}
}

func toView(cfg storedConfig, updatedAt *time.Time) *ConfigView {
	return &ConfigView{
		Configured:         true,
		Enabled:            cfg.Enabled,
		ProviderType:       cfg.ProviderType,
		ServerURL:          cfg.ServerURL,
		SIPDomain:          cfg.SIPDomain,
		SIPPort:            cfg.SIPPort,
		Transport:          cfg.Transport,
		Username:           cfg.Username,
		Extension:          cfg.Extension,
		DisplayName:        cfg.DisplayName,
		OutboundPrefix:     cfg.OutboundPrefix,
		DefaultCountryCode: cfg.DefaultCountryCode,
		AutoRegister:       cfg.AutoRegister,
		HasSecret:          cfg.Secret != "",
		UpdatedAt:          updatedAt,
	}
}

func hasControlOrNewline(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

func normalizeInput(in ConfigInput) (storedConfig, error) {
	fields := []string{
		in.ProviderType, in.ServerURL, in.SIPDomain, in.Transport,
		in.Username, in.Extension, in.DisplayName, in.OutboundPrefix,
		in.DefaultCountryCode, in.Secret,
	}
	for _, f := range fields {
		if hasControlOrNewline(f) {
			return storedConfig{}, fmt.Errorf("%w: control characters not allowed", ErrInvalidInput)
		}
	}

	provider := strings.ToLower(strings.TrimSpace(in.ProviderType))
	if provider == "" {
		provider = ProviderSIP
	}
	if provider != ProviderSIP && provider != ProviderGateway {
		return storedConfig{}, fmt.Errorf("%w: unsupported providerType", ErrInvalidInput)
	}

	transport := strings.ToLower(strings.TrimSpace(in.Transport))
	if transport == "" {
		if provider == ProviderGateway {
			transport = TransportWSS
		} else {
			transport = TransportUDP
		}
	}
	switch transport {
	case TransportUDP, TransportTCP, TransportTLS, TransportWSS, TransportHTTPS:
	default:
		return storedConfig{}, fmt.Errorf("%w: unsupported transport", ErrInvalidInput)
	}
	if provider == ProviderSIP && (transport == TransportWSS || transport == TransportHTTPS) {
		return storedConfig{}, fmt.Errorf("%w: invalid transport for sip", ErrInvalidInput)
	}

	port := in.SIPPort
	if port == 0 {
		switch transport {
		case TransportTLS:
			port = 5061
		case TransportWSS, TransportHTTPS:
			port = 443
		default:
			port = 5060
		}
	}
	if port < 1 || port > 65535 {
		return storedConfig{}, fmt.Errorf("%w: invalid port", ErrInvalidInput)
	}

	serverURL := strings.TrimSpace(in.ServerURL)
	if len(serverURL) > maxServerURLLen || strings.ContainsAny(serverURL, " \t") {
		return storedConfig{}, fmt.Errorf("%w: invalid serverUrl", ErrInvalidInput)
	}
	if strings.Contains(serverURL, "://") {
		u, err := url.Parse(serverURL)
		if err != nil || u.Host == "" || u.User != nil {
			return storedConfig{}, fmt.Errorf("%w: invalid serverUrl", ErrInvalidInput)
		}
		scheme := strings.ToLower(u.Scheme)
		switch scheme {
		case "https", "http", "wss", "ws", "sip", "sips":
		default:
			return storedConfig{}, fmt.Errorf("%w: unsupported url scheme", ErrInvalidInput)
		}
	}

	sipDomain := strings.TrimSpace(in.SIPDomain)
	if len(sipDomain) > maxDomainLen || strings.ContainsAny(sipDomain, " \t@") {
		return storedConfig{}, fmt.Errorf("%w: invalid sipDomain", ErrInvalidInput)
	}

	username := strings.TrimSpace(in.Username)
	if len(username) > maxUsernameLen {
		return storedConfig{}, fmt.Errorf("%w: username too long", ErrInvalidInput)
	}

	extension := strings.TrimSpace(in.Extension)
	if len(extension) > maxExtensionLen {
		return storedConfig{}, fmt.Errorf("%w: extension too long", ErrInvalidInput)
	}

	displayName := strings.TrimSpace(in.DisplayName)
	if len(displayName) > maxDisplayNameLen {
		return storedConfig{}, fmt.Errorf("%w: displayName too long", ErrInvalidInput)
	}

	prefix := strings.TrimSpace(in.OutboundPrefix)
	if len(prefix) > maxPrefixLen {
		return storedConfig{}, fmt.Errorf("%w: outboundPrefix too long", ErrInvalidInput)
	}
	for _, r := range prefix {
		if !((r >= '0' && r <= '9') || r == '+' || r == '*' || r == '#') {
			return storedConfig{}, fmt.Errorf("%w: invalid outboundPrefix", ErrInvalidInput)
		}
	}

	countryCode := strings.TrimSpace(in.DefaultCountryCode)
	if countryCode == "" {
		countryCode = "90"
	}
	if len(countryCode) > 4 {
		return storedConfig{}, fmt.Errorf("%w: invalid defaultCountryCode", ErrInvalidInput)
	}
	for _, r := range countryCode {
		if r < '0' || r > '9' {
			return storedConfig{}, fmt.Errorf("%w: invalid defaultCountryCode", ErrInvalidInput)
		}
	}

	if len(in.Secret) > maxSecretLen {
		return storedConfig{}, fmt.Errorf("%w: secret too long", ErrInvalidInput)
	}

	if in.Enabled && provider == ProviderSIP && serverURL == "" && sipDomain == "" {
		return storedConfig{}, fmt.Errorf("%w: sip server or domain required when enabled", ErrInvalidInput)
	}

	return storedConfig{
		Enabled:            in.Enabled,
		ProviderType:       provider,
		ServerURL:          serverURL,
		SIPDomain:          sipDomain,
		SIPPort:            port,
		Transport:          transport,
		Username:           username,
		Extension:          extension,
		DisplayName:        displayName,
		OutboundPrefix:     prefix,
		DefaultCountryCode: countryCode,
		AutoRegister:       in.AutoRegister,
		Secret:             in.Secret,
	}, nil
}

func (s *Service) GetConfig(ctx context.Context, userID string) (*ConfigView, error) {
	if userID == "" {
		return nil, fmt.Errorf("%w: user required", ErrInvalidInput)
	}
	cfg, updatedAt, err := s.loadStored(ctx, userID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return defaultView(), nil
		}
		return nil, err
	}
	return toView(cfg, &updatedAt), nil
}

func (s *Service) GetProvision(ctx context.Context, userID string) (*ConfigProvision, error) {
	if userID == "" {
		return nil, fmt.Errorf("%w: user required", ErrInvalidInput)
	}
	cfg, updatedAt, err := s.loadStored(ctx, userID)
	if err != nil {
		return nil, err
	}
	return &ConfigProvision{
		ConfigView: *toView(cfg, &updatedAt),
		Secret:     cfg.Secret,
	}, nil
}

func (s *Service) PutConfig(ctx context.Context, userID string, in ConfigInput) (*ConfigView, error) {
	if userID == "" {
		return nil, fmt.Errorf("%w: user required", ErrInvalidInput)
	}
	normalized, err := normalizeInput(in)
	if err != nil {
		return nil, err
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if !in.ClearSecret && normalized.Secret == "" {
		var existingBlob []byte
		err := tx.QueryRow(ctx, `select payload from localtel_configs where user_id = $1::uuid`, userID).Scan(&existingBlob)
		if err == nil {
			if plain, decErr := s.vault.Open(existingBlob, aadFor(userID)); decErr == nil {
				var prev storedConfig
				if json.Unmarshal(plain, &prev) == nil {
					normalized.Secret = prev.Secret
				}
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	}
	if in.ClearSecret {
		normalized.Secret = ""
	}

	plain, err := json.Marshal(normalized)
	if err != nil {
		return nil, err
	}
	sealed, err := s.vault.Seal(plain, aadFor(userID))
	if err != nil {
		return nil, err
	}

	var updatedAt time.Time
	if err := tx.QueryRow(ctx, `
		insert into localtel_configs (user_id, payload, updated_at)
		values ($1::uuid, $2, now())
		on conflict (user_id) do update
		set payload = excluded.payload,
		    updated_at = now()
		returning updated_at`, userID, sealed).Scan(&updatedAt); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	utc := updatedAt.UTC()
	return toView(normalized, &utc), nil
}

func (s *Service) DeleteConfig(ctx context.Context, userID string) error {
	if userID == "" {
		return fmt.Errorf("%w: user required", ErrInvalidInput)
	}
	_, err := s.db.Exec(ctx, `delete from localtel_configs where user_id = $1::uuid`, userID)
	return err
}

func (s *Service) loadStored(ctx context.Context, userID string) (storedConfig, time.Time, error) {
	var blob []byte
	var updatedAt time.Time
	err := s.db.QueryRow(ctx, `
		select payload, updated_at from localtel_configs where user_id = $1::uuid`, userID).Scan(&blob, &updatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storedConfig{}, time.Time{}, ErrNotFound
		}
		return storedConfig{}, time.Time{}, err
	}
	plain, err := s.vault.Open(blob, aadFor(userID))
	if err != nil {
		return storedConfig{}, time.Time{}, err
	}
	var cfg storedConfig
	if err := json.Unmarshal(plain, &cfg); err != nil {
		return storedConfig{}, time.Time{}, errVaultCiphertext
	}
	return cfg, updatedAt.UTC(), nil
}
