package auth

import (
	"context"
	"crypto/hmac"
	"errors"
	"fmt"
	"strings"
	"time"

	"necipdrive/internal/domain"
	"necipdrive/internal/totp"

	"github.com/jackc/pgx/v5"
)

const (
	purposeLogin2FA      = "login_2fa"
	purposePasswordReset = "password_reset"
	purposeQRLogin       = "qr_login"
	otpTTL               = 10 * time.Minute
	qrLoginTTL           = 3 * time.Minute
)

var (
	ErrMailRequired     = errors.New("e-posta (SMTP) yapılandırılmamış; yöneticinizden Mail ayarlarını açmasını isteyin")
	ErrChallengeInvalid = errors.New("geçersiz veya süresi dolmuş doğrulama kodu")
	Err2FARequired      = errors.New("iki adımlı doğrulama gerekli")
)

type MailSender interface {
	Send(ctx context.Context, to, subject, body string) error
	Configured(ctx context.Context) (bool, error)
}

type LoginResult struct {
	User           *domain.User `json:"user,omitempty"`
	Requires2FA    bool         `json:"requires2FA,omitempty"`
	TwoFAMethod    string       `json:"twoFAMethod,omitempty"`
	ChallengeToken string       `json:"challengeToken,omitempty"`
	SessionToken   string       `json:"-"`
}

type SecurityStatus struct {
	Email2FAEnabled bool `json:"email2FAEnabled"`
	TotpEnabled     bool `json:"totpEnabled"`
	MailConfigured  bool `json:"mailConfigured"`
}

func (s *Service) SetMailer(mail MailSender) {
	s.mail = mail
}

func (s *Service) SecurityStatus(ctx context.Context, userID string) (SecurityStatus, error) {
	var st SecurityStatus
	err := s.db.QueryRow(ctx, `select coalesce(email_2fa_enabled,false), coalesce(totp_enabled,false) from users where id = $1::uuid`, userID).Scan(&st.Email2FAEnabled, &st.TotpEnabled)
	if err != nil {
		return st, err
	}
	if s.mail != nil {
		st.MailConfigured, _ = s.mail.Configured(ctx)
	}
	return st, nil
}

func (s *Service) SetEmail2FA(ctx context.Context, userID, password string, enabled bool) error {
	var hash string
	var email string
	err := s.db.QueryRow(ctx, `select email, password_hash from users where id = $1::uuid`, userID).Scan(&email, &hash)
	if err != nil {
		return err
	}
	if !verifyPassword(password, hash) {
		return ErrInvalidCredentials
	}
	if enabled {
		if s.mail == nil {
			return ErrMailRequired
		}
		ok, err := s.mail.Configured(ctx)
		if err != nil {
			return err
		}
		if !ok {
			return ErrMailRequired
		}
	}
	_, err = s.db.Exec(ctx, `update users set email_2fa_enabled = $1 where id = $2::uuid`, enabled, userID)
	return err
}

func (s *Service) ForgotPassword(ctx context.Context, remoteAddr, email string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return errors.New("email required")
	}
	if s.limiter.blocked(remoteAddr + ":reset") {
		return ErrRateLimited
	}
	s.limiter.add(remoteAddr + ":reset")

	// Always succeed outwardly; only send mail when account exists and SMTP works.
	var userID string
	err := s.db.QueryRow(ctx, `select id::text from users where email = $1`, email).Scan(&userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	if s.mail == nil {
		return ErrMailRequired
	}
	ok, err := s.mail.Configured(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return ErrMailRequired
	}
	code, challengeToken, err := s.createChallenge(ctx, userID, purposePasswordReset)
	if err != nil {
		return err
	}
	_ = challengeToken // email-only reset uses email+code
	body := fmt.Sprintf(
		"TR Driver şifre sıfırlama kodunuz: %s\n\nBu kod %d dakika geçerlidir.\nİstemediyseniz bu e-postayı yok sayın.\n",
		code, int(otpTTL.Minutes()),
	)
	return s.mail.Send(ctx, email, "TR Driver şifre sıfırlama", body)
}

func (s *Service) ResetPassword(ctx context.Context, remoteAddr, email, code, newPassword string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	code = strings.TrimSpace(code)
	if email == "" || code == "" {
		return ErrChallengeInvalid
	}
	if len(newPassword) < 8 || len(newPassword) > 128 {
		return errors.New("password must be between 8 and 128 characters")
	}
	if s.limiter.blocked(remoteAddr + ":reset-complete") {
		return ErrRateLimited
	}

	var userID string
	err := s.db.QueryRow(ctx, `select id::text from users where email = $1`, email).Scan(&userID)
	if err != nil {
		s.limiter.add(remoteAddr + ":reset-complete")
		return ErrChallengeInvalid
	}
	if err := s.consumeChallenge(ctx, userID, purposePasswordReset, "", code); err != nil {
		s.limiter.add(remoteAddr + ":reset-complete")
		return err
	}
	passwordHash, err := hashPassword(newPassword)
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx, `update users set password_hash = $1 where id = $2::uuid`, passwordHash, userID); err != nil {
		return err
	}
	// Invalidate all browser sessions and device tokens after reset.
	if _, err := s.db.Exec(ctx, `delete from sessions where user_id = $1::uuid`, userID); err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx, `update devices set revoked_at = now() where user_id = $1::uuid and revoked_at is null`, userID); err != nil {
		return err
	}
	return nil
}

func (s *Service) CreateQRLogin(ctx context.Context, userID string) (challengeToken string, expiresAt time.Time, err error) {
	challengeToken, tokenHash, err := generateChallengeToken()
	if err != nil {
		return "", time.Time{}, err
	}
	expiresAt = time.Now().Add(qrLoginTTL)
	_, _ = s.db.Exec(ctx, `
		update auth_challenges set consumed_at = now()
		where user_id = $1::uuid and purpose = $2 and consumed_at is null`, userID, purposeQRLogin)
	// code_hash unused for QR; store placeholder hash of token.
	_, err = s.db.Exec(ctx, `
		insert into auth_challenges (user_id, purpose, code_hash, token_hash, expires_at)
		values ($1::uuid, $2, $3, $4, $5)`,
		userID, purposeQRLogin, hashToken(challengeToken), tokenHash, expiresAt,
	)
	if err != nil {
		return "", time.Time{}, err
	}
	return challengeToken, expiresAt, nil
}

func (s *Service) RedeemQRLogin(ctx context.Context, challengeToken, deviceName string) (*domain.User, string, error) {
	challengeToken = strings.TrimSpace(challengeToken)
	if challengeToken == "" {
		return nil, "", ErrChallengeInvalid
	}
	var userID, challengeID string
	err := s.db.QueryRow(ctx, `
		select id::text, user_id::text
		from auth_challenges
		where token_hash = $1 and purpose = $2 and consumed_at is null and expires_at > now()`,
		hashToken(challengeToken), purposeQRLogin,
	).Scan(&challengeID, &userID)
	if err != nil {
		return nil, "", ErrChallengeInvalid
	}
	res, err := s.db.Exec(ctx, `
		update auth_challenges set consumed_at = now()
		where id = $1::uuid and consumed_at is null`, challengeID)
	if err != nil {
		return nil, "", err
	}
	if res.RowsAffected() == 0 {
		return nil, "", ErrChallengeInvalid
	}
	var user domain.User
	err = s.db.QueryRow(ctx, `
		select id::text, email, display_name, role, plan_code, quota_bytes, coalesce(bonus_quota_bytes,0), used_bytes, reserved_bytes,
		       storage_root_id::text, created_at, last_login_at, coalesce(email_2fa_enabled,false), coalesce(totp_enabled,false)
		from users where id = $1::uuid`, userID,
	).Scan(&user.ID, &user.Email, &user.DisplayName, &user.Role, &user.PlanCode, &user.BaseQuotaBytes, &user.BonusQuotaBytes, &user.UsedBytes, &user.ReservedBytes, &user.StorageRootID, &user.CreatedAt, &user.LastLoginAt, &user.Email2FAEnabled, &user.TotpEnabled)
	if err != nil {
		return nil, "", err
	}
	user.QuotaBytes = user.BaseQuotaBytes + user.BonusQuotaBytes
	user.MaxBatchBytes = s.maxBatchBytes(ctx)
	user.UploadChunkBytes = s.cfg.UploadChunkBytes
	_, token, err := s.CreateDevice(ctx, userID, deviceName)
	if err != nil {
		return nil, "", err
	}
	return &user, token, nil
}

func (s *Service) createChallenge(ctx context.Context, userID, purpose string) (code, challengeToken string, err error) {
	code, err = generateOTPCode()
	if err != nil {
		return "", "", err
	}
	challengeToken, tokenHash, err := generateChallengeToken()
	if err != nil {
		return "", "", err
	}
	// Invalidate older open challenges for same purpose.
	_, _ = s.db.Exec(ctx, `
		update auth_challenges set consumed_at = now()
		where user_id = $1::uuid and purpose = $2 and consumed_at is null`, userID, purpose)

	_, err = s.db.Exec(ctx, `
		insert into auth_challenges (user_id, purpose, code_hash, token_hash, expires_at)
		values ($1::uuid, $2, $3, $4, $5)`,
		userID, purpose, s.hashOTP(code), tokenHash, time.Now().Add(otpTTL),
	)
	if err != nil {
		return "", "", err
	}
	return code, challengeToken, nil
}

// consumeChallenge verifies code. If challengeToken is non-empty it must match; otherwise match latest open challenge for user+purpose.
func (s *Service) consumeChallenge(ctx context.Context, userID, purpose, challengeToken, code string) error {
	code = strings.TrimSpace(code)
	if len(code) != otpDigits {
		return ErrChallengeInvalid
	}
	var (
		id       string
		codeHash string
		attempts int
	)
	var err error
	if challengeToken != "" {
		err = s.db.QueryRow(ctx, `
			select id::text, code_hash, attempts
			from auth_challenges
			where token_hash = $1 and purpose = $2 and consumed_at is null and expires_at > now()`,
			hashToken(challengeToken), purpose,
		).Scan(&id, &codeHash, &attempts)
	} else {
		err = s.db.QueryRow(ctx, `
			select id::text, code_hash, attempts
			from auth_challenges
			where user_id = $1::uuid and purpose = $2 and consumed_at is null and expires_at > now()
			order by created_at desc
			limit 1`, userID, purpose,
		).Scan(&id, &codeHash, &attempts)
	}
	if err != nil {
		return ErrChallengeInvalid
	}
	if attempts >= maxOTPAttempts {
		_, _ = s.db.Exec(ctx, `update auth_challenges set consumed_at = now() where id = $1::uuid`, id)
		return ErrChallengeInvalid
	}
	if !hmac.Equal([]byte(s.hashOTP(code)), []byte(codeHash)) {
		_, _ = s.db.Exec(ctx, `update auth_challenges set attempts = attempts + 1 where id = $1::uuid`, id)
		return ErrChallengeInvalid
	}
	if userID != "" {
		var owner string
		_ = s.db.QueryRow(ctx, `select user_id::text from auth_challenges where id = $1::uuid`, id).Scan(&owner)
		if owner != "" && owner != userID {
			return ErrChallengeInvalid
		}
	}
	res, err := s.db.Exec(ctx, `
		update auth_challenges set consumed_at = now()
		where id = $1::uuid and consumed_at is null`, id)
	if err != nil {
		return err
	}
	if res.RowsAffected() == 0 {
		return ErrChallengeInvalid
	}
	return nil
}

func (s *Service) CompleteLogin2FA(ctx context.Context, remoteAddr, challengeToken, code string) (*domain.User, string, error) {
	if s.limiter.blocked(remoteAddr + ":2fa") {
		return nil, "", ErrRateLimited
	}
	challengeToken = strings.TrimSpace(challengeToken)
	code = strings.TrimSpace(code)
	if challengeToken == "" || code == "" {
		return nil, "", ErrChallengeInvalid
	}
	var userID, purpose string
	err := s.db.QueryRow(ctx, `
		select user_id::text, purpose from auth_challenges
		where token_hash = $1 and purpose in ($2, $3) and consumed_at is null and expires_at > now()`,
		hashToken(challengeToken), purposeLogin2FA, purposeLoginTOTP,
	).Scan(&userID, &purpose)
	if err != nil {
		s.limiter.add(remoteAddr + ":2fa")
		return nil, "", ErrChallengeInvalid
	}
	if purpose == purposeLoginTOTP {
		if err := s.consumeTOTPChallenge(ctx, userID, challengeToken, code); err != nil {
			s.limiter.add(remoteAddr + ":2fa")
			return nil, "", err
		}
	} else if err := s.consumeChallenge(ctx, userID, purposeLogin2FA, challengeToken, code); err != nil {
		s.limiter.add(remoteAddr + ":2fa")
		return nil, "", err
	}
	user, err := s.userByID(ctx, userID)
	if err != nil {
		return nil, "", err
	}
	sessionToken, err := s.issueSession(ctx, user.ID)
	if err != nil {
		return nil, "", err
	}
	return user, sessionToken, nil
}

func (s *Service) issueSession(ctx context.Context, userID string) (string, error) {
	sessionToken, tokenHash, err := newSessionToken()
	if err != nil {
		return "", err
	}
	if _, err := s.db.Exec(ctx, `insert into sessions (user_id, token_hash, expires_at) values ($1::uuid, $2, $3)`, userID, tokenHash, time.Now().Add(s.cfg.SessionTTL)); err != nil {
		return "", err
	}
	if _, err := s.db.Exec(ctx, `update users set last_login_at = now() where id = $1::uuid`, userID); err != nil {
		return "", err
	}
	return sessionToken, nil
}

func (s *Service) userByID(ctx context.Context, userID string) (*domain.User, error) {
	var user domain.User
	err := s.db.QueryRow(ctx, `
		select id::text, email, display_name, role, plan_code, quota_bytes, coalesce(bonus_quota_bytes,0), used_bytes, reserved_bytes,
		       storage_root_id::text, created_at, last_login_at, coalesce(email_2fa_enabled,false), coalesce(totp_enabled,false)
		from users where id = $1::uuid`, userID,
	).Scan(&user.ID, &user.Email, &user.DisplayName, &user.Role, &user.PlanCode, &user.BaseQuotaBytes, &user.BonusQuotaBytes, &user.UsedBytes, &user.ReservedBytes, &user.StorageRootID, &user.CreatedAt, &user.LastLoginAt, &user.Email2FAEnabled, &user.TotpEnabled)
	if err != nil {
		return nil, err
	}
	user.QuotaBytes = user.BaseQuotaBytes + user.BonusQuotaBytes
	user.MaxBatchBytes = s.maxBatchBytes(ctx)
	user.UploadChunkBytes = s.cfg.UploadChunkBytes
	return &user, nil
}

func (s *Service) startLogin2FA(ctx context.Context, user *domain.User) (string, error) {
	if s.mail == nil {
		return "", ErrMailRequired
	}
	ok, err := s.mail.Configured(ctx)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", ErrMailRequired
	}
	code, challengeToken, err := s.createChallenge(ctx, user.ID, purposeLogin2FA)
	if err != nil {
		return "", err
	}
	body := fmt.Sprintf(
		"TR Driver giriş doğrulama kodunuz: %s\n\nBu kod %d dakika geçerlidir.\nSiz değilseniz şifrenizi değiştirin.\n",
		code, int(otpTTL.Minutes()),
	)
	if err := s.mail.Send(ctx, user.Email, "TR Driver giriş kodu", body); err != nil {
		return "", err
	}
	return challengeToken, nil
}

func (s *Service) BeginDeviceLogin(ctx context.Context, remoteAddr, email, password string) (*LoginResult, error) {
	user, err := s.authenticatePassword(ctx, remoteAddr, email, password)
	if err != nil {
		return nil, err
	}
	if user.TotpEnabled {
		challengeToken, err := s.startTOTPLogin(ctx, user.ID)
		if err != nil {
			return nil, err
		}
		return &LoginResult{Requires2FA: true, TwoFAMethod: "totp", ChallengeToken: challengeToken}, nil
	}
	if user.Email2FAEnabled {
		challengeToken, err := s.startLogin2FA(ctx, user)
		if err != nil {
			return nil, err
		}
		return &LoginResult{Requires2FA: true, TwoFAMethod: "email", ChallengeToken: challengeToken}, nil
	}
	return &LoginResult{User: user}, nil
}

const purposeLoginTOTP = "login_totp"

// BeginTOTPSetup stores a pending (not yet enabled) TOTP secret for the user.
func (s *Service) BeginTOTPSetup(ctx context.Context, userID, password string) (secret string, provisioningURI string, err error) {
	var hash, email string
	if err := s.db.QueryRow(ctx, `select email, password_hash from users where id = $1::uuid`, userID).Scan(&email, &hash); err != nil {
		return "", "", err
	}
	if !verifyPassword(password, hash) {
		return "", "", ErrInvalidCredentials
	}
	var enabled bool
	if err := s.db.QueryRow(ctx, `select totp_enabled from users where id = $1::uuid`, userID).Scan(&enabled); err != nil {
		return "", "", err
	}
	if enabled {
		return "", "", errors.New("TOTP zaten açık; önce mevcut authenticator kodu ile kapatın")
	}
	secret, err = totp.GenerateSecret()
	if err != nil {
		return "", "", err
	}
	sealed, err := totp.Seal(secret, s.cfg.SessionSecret)
	if err != nil {
		return "", "", err
	}
	if _, err := s.db.Exec(ctx, `update users set totp_secret_encrypted = $1, totp_enabled = false, totp_last_counter = -1 where id = $2::uuid`, sealed, userID); err != nil {
		return "", "", err
	}
	return secret, totp.ProvisioningURI("TR Driver", email, secret), nil
}

func (s *Service) EnableTOTP(ctx context.Context, userID, password, code string) error {
	var hash, sealed string
	var enabled bool
	if err := s.db.QueryRow(ctx, `select password_hash, totp_secret_encrypted, totp_enabled from users where id = $1::uuid`, userID).Scan(&hash, &sealed, &enabled); err != nil {
		return err
	}
	if !verifyPassword(password, hash) {
		return ErrInvalidCredentials
	}
	if enabled {
		return nil
	}
	if sealed == "" {
		return ErrChallengeInvalid
	}
	secret, err := totp.Open(sealed, s.cfg.SessionSecret)
	if err != nil {
		return ErrChallengeInvalid
	}
	counter, ok := totp.Match(secret, code, time.Now(), -1)
	if !ok {
		return ErrChallengeInvalid
	}
	_, err = s.db.Exec(ctx, `update users set totp_enabled = true, totp_last_counter = $1 where id = $2::uuid`, counter, userID)
	return err
}

func (s *Service) DisableTOTP(ctx context.Context, userID, password, code string) error {
	var hash, sealed string
	var enabled bool
	var lastCounter int64
	if err := s.db.QueryRow(ctx, `select password_hash, totp_secret_encrypted, totp_enabled, totp_last_counter from users where id = $1::uuid`, userID).Scan(&hash, &sealed, &enabled, &lastCounter); err != nil {
		return err
	}
	if !verifyPassword(password, hash) {
		return ErrInvalidCredentials
	}
	if enabled {
		secret, err := totp.Open(sealed, s.cfg.SessionSecret)
		if err != nil {
			return ErrChallengeInvalid
		}
		if _, ok := totp.Match(secret, code, time.Now(), lastCounter); !ok {
			return ErrChallengeInvalid
		}
	}
	_, err := s.db.Exec(ctx, `update users set totp_enabled = false, totp_secret_encrypted = '', totp_last_counter = -1 where id = $1::uuid`, userID)
	return err
}

func (s *Service) startTOTPLogin(ctx context.Context, userID string) (string, error) {
	challengeToken, tokenHash, err := generateChallengeToken()
	if err != nil {
		return "", err
	}
	_, _ = s.db.Exec(ctx, `update auth_challenges set consumed_at = now() where user_id = $1::uuid and purpose = $2 and consumed_at is null`, userID, purposeLoginTOTP)
	if _, err := s.db.Exec(ctx, `insert into auth_challenges (user_id, purpose, code_hash, token_hash, expires_at) values ($1::uuid, $2, '', $3, $4)`, userID, purposeLoginTOTP, tokenHash, time.Now().Add(otpTTL)); err != nil {
		return "", err
	}
	return challengeToken, nil
}

func (s *Service) consumeTOTPChallenge(ctx context.Context, userID, challengeToken, code string) error {
	var id string
	var attempts int
	err := s.db.QueryRow(ctx, `select id::text, attempts from auth_challenges where token_hash = $1 and purpose = $2 and consumed_at is null and expires_at > now()`, hashToken(challengeToken), purposeLoginTOTP).Scan(&id, &attempts)
	if err != nil {
		return ErrChallengeInvalid
	}
	if attempts >= maxOTPAttempts {
		_, _ = s.db.Exec(ctx, `update auth_challenges set consumed_at = now() where id = $1::uuid`, id)
		return ErrChallengeInvalid
	}
	var sealed string
	var lastCounter int64
	if err := s.db.QueryRow(ctx, `select totp_secret_encrypted, totp_last_counter from users where id = $1::uuid`, userID).Scan(&sealed, &lastCounter); err != nil {
		return ErrChallengeInvalid
	}
	secret, err := totp.Open(sealed, s.cfg.SessionSecret)
	if err != nil {
		return ErrChallengeInvalid
	}
	counter, ok := totp.Match(secret, code, time.Now(), lastCounter)
	if !ok {
		_, _ = s.db.Exec(ctx, `update auth_challenges set attempts = attempts + 1 where id = $1::uuid`, id)
		return ErrChallengeInvalid
	}
	res, err := s.db.Exec(ctx, `update users set totp_last_counter = $1 where id = $2::uuid and totp_last_counter < $1`, counter, userID)
	if err != nil || res.RowsAffected() == 0 {
		return ErrChallengeInvalid
	}
	res, err = s.db.Exec(ctx, `update auth_challenges set consumed_at = now() where id = $1::uuid and consumed_at is null`, id)
	if err != nil || res.RowsAffected() == 0 {
		return ErrChallengeInvalid
	}
	return nil
}
