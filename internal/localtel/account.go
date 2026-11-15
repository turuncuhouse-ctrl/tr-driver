package localtel

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	extensionLowerBound   = 10000
	extensionUpperBound   = 99999
	accountCreateRetries  = 20
	maxAccountDisplayName = 80
	maxAccountDomain      = 253
)

// Account is the auto-provisioned LocalTel calling identity of one TR Driver
// user. Its password is only ever stored sealed with LOCAL_TEL_KEY.
type Account struct {
	UserID      string
	Extension   string
	Username    string
	Domain      string
	Password    string
	DisplayName string
	CreatedAt   time.Time
}

// EnsureAccount returns the user's calling account, creating it on first call.
func (s *Service) EnsureAccount(ctx context.Context, userID string) (*Account, error) {
	if s.db == nil {
		return nil, errors.New("localtel database is unavailable")
	}
	if userID == "" {
		return nil, fmt.Errorf("%w: user required", ErrInvalidInput)
	}
	acct, err := s.loadAccount(ctx, userID)
	if err == nil {
		return acct, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	return s.createAccount(ctx, userID)
}

// Account returns the user's calling account without creating one.
func (s *Service) Account(ctx context.Context, userID string) (*Account, error) {
	if s.db == nil {
		return nil, errors.New("localtel database is unavailable")
	}
	if userID == "" {
		return nil, fmt.Errorf("%w: user required", ErrInvalidInput)
	}
	return s.loadAccount(ctx, userID)
}

func (s *Service) loadAccount(ctx context.Context, userID string) (*Account, error) {
	var (
		extension string
		username  string
		domain    string
		display   string
		created   time.Time
		sealed    []byte
	)
	err := s.db.QueryRow(ctx, `
		select extension, sip_username, sip_domain, display_name, created_at, password_enc
		from localtel_accounts where user_id = $1::uuid`, userID,
	).Scan(&extension, &username, &domain, &display, &created, &sealed)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	password, err := s.vault.Open(sealed, aadFor(userID))
	if err != nil {
		return nil, err
	}
	return &Account{
		UserID:      userID,
		Extension:   extension,
		Username:    username,
		Domain:      domain,
		Password:    string(password),
		DisplayName: display,
		CreatedAt:   created.UTC(),
	}, nil
}

func (s *Service) createAccount(ctx context.Context, userID string) (*Account, error) {
	domain := strings.TrimSpace(s.domain)
	if domain == "" {
		domain = "localhost"
	}
	if len(domain) > maxAccountDomain || strings.ContainsAny(domain, " \t@/?#") {
		return nil, fmt.Errorf("%w: invalid localtel domain", ErrInvalidInput)
	}
	display := s.userDisplayName(ctx, userID)
	phone := normalizeDial(s.userPhone(ctx, userID)).digits
	usePhone := phone != ""

	for attempt := 0; attempt < accountCreateRetries; attempt++ {
		extension := ""
		if usePhone {
			extension = phone
		} else {
			var err error
			extension, err = randomExtension()
			if err != nil {
				return nil, err
			}
		}
		username, err := randomHex(12)
		if err != nil {
			return nil, err
		}
		username = "u" + username
		password, err := randomHex(16)
		if err != nil {
			return nil, err
		}
		sealed, err := s.vault.Seal([]byte(password), aadFor(userID))
		if err != nil {
			return nil, err
		}
		var created time.Time
		err = s.db.QueryRow(ctx, `
			insert into localtel_accounts (user_id, extension, sip_username, sip_domain, password_enc, display_name)
			values ($1::uuid, $2, $3, $4, $5, $6)
			returning created_at`,
			userID, extension, username, domain, sealed, display,
		).Scan(&created)
		if err == nil {
			return &Account{
				UserID:      userID,
				Extension:   extension,
				Username:    username,
				Domain:      domain,
				Password:    password,
				DisplayName: display,
				CreatedAt:   created.UTC(),
			}, nil
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			// A same-user request may have provisioned concurrently; return its
			// record rather than treating that harmless race as allocation failure.
			if existing, loadErr := s.loadAccount(ctx, userID); loadErr == nil {
				return existing, nil
			}
			// A shared phone may already belong to another account. Preserve
			// reachability by falling back to a generated unique extension.
			if usePhone {
				usePhone = false
			}
			// Extension/username raced with another account; try fresh values.
			continue
		}
		return nil, err
	}
	return nil, fmt.Errorf("%w: could not allocate a unique localtel extension", ErrInvalidInput)
}

func (s *Service) userDisplayName(ctx context.Context, userID string) string {
	var display string
	err := s.db.QueryRow(ctx, `
		select coalesce(nullif(display_name, ''), split_part(email, '@', 1))
		from users where id = $1::uuid`, userID).Scan(&display)
	if err != nil {
		return ""
	}
	display = strings.TrimSpace(display)
	if runes := []rune(display); len(runes) > maxAccountDisplayName {
		display = string(runes[:maxAccountDisplayName])
	}
	return display
}

func (s *Service) userPhone(ctx context.Context, userID string) string {
	var phone string
	if err := s.db.QueryRow(ctx, `select coalesce(phone, '') from users where id = $1::uuid`, userID).Scan(&phone); err != nil {
		return ""
	}
	return phone
}

func randomExtension() (string, error) {
	span := int64(extensionUpperBound - extensionLowerBound + 1)
	value, err := rand.Int(rand.Reader, big.NewInt(span))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%05d", extensionLowerBound+value.Int64()), nil
}

func randomHex(bytes int) (string, error) {
	raw := make([]byte, bytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

// ResolveCallTarget maps a dialed address to a provisioned TR Driver account.
// Accepted forms include the account phone, 5-digit extension, sip username,
// or a sip: URI. Numbers are normalized to digits before the DB comparison.
func (s *Service) ResolveCallTarget(ctx context.Context, dial string) (string, bool) {
	if s.db == nil {
		return "", false
	}
	key := normalizeDial(dial)
	if key.raw == "" && key.digits == "" {
		return "", false
	}
	var userID string
	err := s.db.QueryRow(ctx, `
		select user_id::text from localtel_accounts
		where extension = $1 or lower(sip_username) = lower($2)
		limit 1`, key.digits, key.raw).Scan(&userID)
	if err == nil && userID != "" {
		return userID, true
	}
	// Fall back to the member's registered phone (digits-only international
	// form), compared on the last 10 digits so 0532..., +90532... and 532...
	// all reach the same account.
	if len(key.digits) >= 10 {
		tail := key.digits[len(key.digits)-10:]
		err = s.db.QueryRow(ctx, `
			select a.user_id::text from localtel_accounts a
			join users u on u.id = a.user_id
			where length(u.phone) >= 10 and right(u.phone, 10) = $1
			order by a.created_at
			limit 1`, tail).Scan(&userID)
		if err == nil && userID != "" {
			return userID, true
		}
	}
	return "", false
}

// CallerIdentity returns the caller's stable address and display name so the
// receiving side can show a local contact name when its address is in contacts.
func (s *Service) CallerIdentity(ctx context.Context, userID string) (string, string, bool) {
	if s.db == nil {
		return "", "", false
	}
	acct, err := s.loadAccount(ctx, userID)
	if err != nil {
		return "", "", false
	}
	return acct.Extension, acct.DisplayName, acct.Extension != ""
}

type dialKey struct {
	raw    string
	digits string
}

func normalizeDial(dial string) dialKey {
	raw := strings.TrimSpace(dial)
	raw = strings.TrimPrefix(raw, "sip:")
	raw = strings.TrimPrefix(raw, "sips:")
	if at := strings.Index(raw, "@"); at >= 0 {
		raw = raw[:at]
	}
	raw = strings.TrimSpace(raw)
	var builder strings.Builder
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			builder.WriteRune(r)
		}
	}
	return dialKey{raw: raw, digits: builder.String()}
}
