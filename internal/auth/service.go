package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"necipdrive/internal/config"
	"necipdrive/internal/domain"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/argon2"
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrRateLimited        = errors.New("too many login attempts")
)

type Service struct {
	db       *pgxpool.Pool
	cfg      config.Config
	limiter  *loginLimiter
	license  LicenseGate
}

type LicenseGate interface {
	RegistrationAllowed(ctx context.Context) error
	EnsureCanAddUser(ctx context.Context) error
}

type loginLimiter struct {
	mu      sync.Mutex
	attempt map[string][]time.Time
}

func NewService(db *pgxpool.Pool, cfg config.Config, license LicenseGate) *Service {
	return &Service{
		db:      db,
		cfg:     cfg,
		limiter: &loginLimiter{attempt: map[string][]time.Time{}},
		license: license,
	}
}

func (s *Service) Register(ctx context.Context, email, password, displayName string) (*domain.User, string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	displayName = strings.TrimSpace(displayName)
	if email == "" || password == "" || displayName == "" {
		return nil, "", errors.New("email, password and display name are required")
	}
	parsedEmail, err := mail.ParseAddress(email)
	if err != nil || strings.ToLower(parsedEmail.Address) != email || len(email) > 254 {
		return nil, "", errors.New("invalid email address")
	}
	if len(password) < 8 || len(password) > 128 {
		return nil, "", errors.New("password must be between 8 and 128 characters")
	}
	if utf8.RuneCountInString(displayName) > 100 {
		return nil, "", errors.New("display name is too long")
	}
	if s.license != nil {
		if err := s.license.RegistrationAllowed(ctx); err != nil {
			return nil, "", err
		}
	}

	passwordHash, err := hashPassword(password)
	if err != nil {
		return nil, "", err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback(ctx)

	// Serialize bootstrap admin assignment.
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(872014201)`); err != nil {
		return nil, "", err
	}
	if s.license != nil {
		if err := s.license.EnsureCanAddUser(ctx); err != nil {
			return nil, "", err
		}
	}

	var user domain.User
	err = tx.QueryRow(ctx, `
		insert into users (email, password_hash, display_name, role, plan_code, quota_bytes)
		values (
			$1, $2, $3,
			case when exists(select 1 from users where role = 'admin') then 'user' else 'admin' end,
			'free', $4
		)
		returning id::text, email, display_name, role, plan_code, quota_bytes, used_bytes, reserved_bytes, created_at, last_login_at`,
		email, passwordHash, displayName, s.cfg.FreeQuotaBytes,
	).Scan(&user.ID, &user.Email, &user.DisplayName, &user.Role, &user.PlanCode, &user.QuotaBytes, &user.UsedBytes, &user.ReservedBytes, &user.CreatedAt, &user.LastLoginAt)
	if err != nil {
		return nil, "", err
	}
	user.BaseQuotaBytes = user.QuotaBytes
	user.BonusQuotaBytes = 0

	rootID := uuid.NewString()
	if _, err := tx.Exec(ctx, `
		insert into file_entries (id, user_id, parent_id, name, kind, storage_key, size_bytes, mime_type)
		values ($1::uuid, $2::uuid, null, '/', 'folder', $3, 0, 'inode/directory')`,
		rootID, user.ID, "root/"+user.ID,
	); err != nil {
		return nil, "", err
	}
	driveID := uuid.NewString()
	if _, err := tx.Exec(ctx, `
		insert into drives (id, kind, name, owner_user_id, root_entry_id)
		values ($1::uuid, 'personal', 'My Drive', $2::uuid, $3::uuid)`, driveID, user.ID, rootID); err != nil {
		return nil, "", err
	}
	if _, err := tx.Exec(ctx, `update file_entries set drive_id = $1::uuid where id = $2::uuid`, driveID, rootID); err != nil {
		return nil, "", err
	}
	if _, err := tx.Exec(ctx, `insert into drive_members (drive_id, user_id, role) values ($1::uuid, $2::uuid, 'manager')`, driveID, user.ID); err != nil {
		return nil, "", err
	}
	if _, err := tx.Exec(ctx, `update users set storage_root_id = $1::uuid where id = $2::uuid`, rootID, user.ID); err != nil {
		return nil, "", err
	}
	user.StorageRootID = rootID
	user.PersonalDriveID = driveID

	sessionToken, tokenHash, err := newSessionToken()
	if err != nil {
		return nil, "", err
	}
	if _, err := tx.Exec(ctx, `
		insert into sessions (user_id, token_hash, expires_at)
		values ($1::uuid, $2, $3)`,
		user.ID, tokenHash, time.Now().Add(s.cfg.SessionTTL),
	); err != nil {
		return nil, "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, "", err
	}

	return &user, sessionToken, nil
}

func (s *Service) Login(ctx context.Context, remoteAddr, email, password string) (*domain.User, string, error) {
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		remoteAddr = host
	}
	if s.limiter.blocked(remoteAddr) {
		return nil, "", ErrRateLimited
	}

	var user domain.User
	err := s.db.QueryRow(ctx, `
		select id::text, email, password_hash, display_name, role, plan_code, quota_bytes, coalesce(bonus_quota_bytes,0), used_bytes, reserved_bytes,
		       storage_root_id::text, created_at, last_login_at
		from users where email = $1`, strings.ToLower(strings.TrimSpace(email)),
	).Scan(&user.ID, &user.Email, &user.PasswordHash, &user.DisplayName, &user.Role, &user.PlanCode, &user.BaseQuotaBytes, &user.BonusQuotaBytes, &user.UsedBytes, &user.ReservedBytes, &user.StorageRootID, &user.CreatedAt, &user.LastLoginAt)
	if err != nil {
		s.limiter.add(remoteAddr)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, "", ErrInvalidCredentials
		}
		return nil, "", err
	}
	user.QuotaBytes = user.BaseQuotaBytes + user.BonusQuotaBytes
	if !verifyPassword(password, user.PasswordHash) {
		s.limiter.add(remoteAddr)
		return nil, "", ErrInvalidCredentials
	}

	sessionToken, tokenHash, err := newSessionToken()
	if err != nil {
		return nil, "", err
	}
	if _, err := s.db.Exec(ctx, `insert into sessions (user_id, token_hash, expires_at) values ($1::uuid, $2, $3)`, user.ID, tokenHash, time.Now().Add(s.cfg.SessionTTL)); err != nil {
		return nil, "", err
	}
	if _, err := s.db.Exec(ctx, `update users set last_login_at = now() where id = $1::uuid`, user.ID); err != nil {
		return nil, "", err
	}
	return &user, sessionToken, nil
}

func (s *Service) Logout(ctx context.Context, token string) error {
	_, err := s.db.Exec(ctx, `delete from sessions where token_hash = $1`, hashToken(token))
	return err
}

func (s *Service) UserBySession(ctx context.Context, token string) (*domain.User, error) {
	var user domain.User
	err := s.db.QueryRow(ctx, `
		select u.id::text, u.email, u.display_name, u.role, u.plan_code, u.quota_bytes, coalesce(u.bonus_quota_bytes,0), u.used_bytes, u.reserved_bytes,
		       u.storage_root_id::text, u.created_at, u.last_login_at
		from sessions s
		join users u on u.id = s.user_id
		where s.token_hash = $1 and s.expires_at > now()`,
		hashToken(token),
	).Scan(&user.ID, &user.Email, &user.DisplayName, &user.Role, &user.PlanCode, &user.BaseQuotaBytes, &user.BonusQuotaBytes, &user.UsedBytes, &user.ReservedBytes, &user.StorageRootID, &user.CreatedAt, &user.LastLoginAt)
	if err != nil {
		return nil, err
	}
	user.QuotaBytes = user.BaseQuotaBytes + user.BonusQuotaBytes
	user.MaxBatchBytes = s.maxBatchBytes(ctx)
	user.UploadChunkBytes = s.cfg.UploadChunkBytes
	return &user, nil
}

func (s *Service) CreateDevice(ctx context.Context, userID, name string) (*domain.Device, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 100 {
		return nil, "", errors.New("device name is required and must be at most 100 characters")
	}
	token, tokenHash, err := newSessionToken()
	if err != nil {
		return nil, "", err
	}
	var device domain.Device
	err = s.db.QueryRow(ctx, `
		insert into devices (user_id, name, token_hash, expires_at)
		values ($1::uuid, $2, $3, $4)
		returning id::text, name, created_at, last_seen_at`,
		userID, name, tokenHash, time.Now().Add(s.cfg.DeviceTokenTTL),
	).Scan(&device.ID, &device.Name, &device.CreatedAt, &device.LastSeenAt)
	if err != nil {
		return nil, "", err
	}
	return &device, token, nil
}

func (s *Service) ListDevices(ctx context.Context, userID string) ([]domain.Device, error) {
	rows, err := s.db.Query(ctx, `
		select id::text, name, created_at, last_seen_at
		from devices where user_id = $1::uuid and revoked_at is null
		order by created_at desc`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.Device, 0)
	for rows.Next() {
		var item domain.Device
		if err := rows.Scan(&item.ID, &item.Name, &item.CreatedAt, &item.LastSeenAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Service) RevokeDevice(ctx context.Context, userID, deviceID string) error {
	result, err := s.db.Exec(ctx, `
		update devices set revoked_at = now()
		where id = $1::uuid and user_id = $2::uuid and revoked_at is null`, deviceID, userID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return errors.New("device not found")
	}
	return nil
}

func (s *Service) UserByDeviceToken(ctx context.Context, token string) (*domain.User, string, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback(ctx)
	var user domain.User
	var deviceID string
	err = tx.QueryRow(ctx, `
		select u.id::text, u.email, u.display_name, u.role, u.plan_code, u.quota_bytes, coalesce(u.bonus_quota_bytes,0), u.used_bytes, u.reserved_bytes,
		       u.storage_root_id::text, u.created_at, u.last_login_at, d.id::text
		from devices d join users u on u.id = d.user_id
		where d.token_hash = $1 and d.revoked_at is null and d.expires_at > now()
		for update of d`, hashToken(token),
	).Scan(&user.ID, &user.Email, &user.DisplayName, &user.Role, &user.PlanCode, &user.BaseQuotaBytes, &user.BonusQuotaBytes, &user.UsedBytes, &user.ReservedBytes, &user.StorageRootID, &user.CreatedAt, &user.LastLoginAt, &deviceID)
	if err != nil {
		return nil, "", err
	}
	user.QuotaBytes = user.BaseQuotaBytes + user.BonusQuotaBytes
	if _, err := tx.Exec(ctx, `update devices set last_seen_at = now() where id = $1::uuid`, deviceID); err != nil {
		return nil, "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, "", err
	}
	user.MaxBatchBytes = s.maxBatchBytes(ctx)
	user.UploadChunkBytes = s.cfg.UploadChunkBytes
	user.DeviceID = &deviceID
	return &user, deviceID, nil
}

func (s *Service) maxBatchBytes(ctx context.Context) int64 {
	var raw string
	if err := s.db.QueryRow(ctx, `select value from app_settings where key = 'max_upload_batch_bytes'`).Scan(&raw); err != nil {
		return s.cfg.MaxUploadBatchBytes
	}
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || parsed <= 0 {
		return s.cfg.MaxUploadBatchBytes
	}
	return parsed
}

func (s *Service) SessionCookie(token string, secure bool) *http.Cookie {
	return &http.Cookie{
		Name:     "session_token",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
		MaxAge:   int(s.cfg.SessionTTL.Seconds()),
	}
}

func (s *Service) ClearCookie(secure bool) *http.Cookie {
	c := s.SessionCookie("", secure)
	c.MaxAge = -1
	c.Expires = time.Unix(0, 0)
	return c
}

func newSessionToken() (string, string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	return token, hashToken(token), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func hashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	derived := argon2.IDKey([]byte(password), salt, 1, 64*1024, 2, 32)
	return fmt.Sprintf("%x:%x", salt, derived), nil
}

func verifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, ":")
	if len(parts) != 2 {
		return false
	}
	salt, err := hex.DecodeString(parts[0])
	if err != nil {
		return false
	}
	expected, err := hex.DecodeString(parts[1])
	if err != nil {
		return false
	}
	derived := argon2.IDKey([]byte(password), salt, 1, 64*1024, 2, 32)
	return subtleCompare(expected, derived)
}

func subtleCompare(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

func (l *loginLimiter) blocked(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	window := now.Add(-10 * time.Minute)
	entries := pruneAfter(l.attempt[key], window)
	l.attempt[key] = entries
	return len(entries) >= 10
}

func (l *loginLimiter) add(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	window := now.Add(-10 * time.Minute)
	entries := append(pruneAfter(l.attempt[key], window), now)
	l.attempt[key] = entries
}

func pruneAfter(items []time.Time, after time.Time) []time.Time {
	out := items[:0]
	for _, item := range items {
		if item.After(after) {
			out = append(out, item)
		}
	}
	return out
}
