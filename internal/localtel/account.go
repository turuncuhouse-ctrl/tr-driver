package localtel

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
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
	// Prefer the member's real phone so the callee's contact matcher can show
	// the saved contact name; the extension remains the fallback identity.
	if phone := normalizeDial(s.userPhone(ctx, userID)).digits; phone != "" {
		return "+" + phone, acct.DisplayName, true
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

// ErrInvalidPhone is returned when a phone number cannot be normalized.
var ErrInvalidPhone = fmt.Errorf("%w: invalid phone number", ErrInvalidInput)

// NormalizePhone returns the digits-only international form (Turkish national
// numbers become 90...). The empty string clears the number.
func NormalizePhone(raw string) (string, error) {
	value := normalizeDial(raw).digits
	if value == "" {
		return "", nil
	}
	value = strings.TrimPrefix(value, "00")
	if len(value) == 11 && strings.HasPrefix(value, "0") {
		value = "90" + value[1:]
	} else if len(value) == 10 && strings.HasPrefix(value, "5") {
		value = "90" + value
	}
	if len(value) < 10 || len(value) > 15 {
		return "", ErrInvalidPhone
	}
	return value, nil
}

// PhoneView is what the app shows in "Kendi numaram".
type PhoneView struct {
	Phone     string `json:"phone"`
	Extension string `json:"extension"`
}

func (s *Service) GetPhone(ctx context.Context, userID string) (*PhoneView, error) {
	acct, err := s.EnsureAccount(ctx, userID)
	if err != nil {
		return nil, err
	}
	return &PhoneView{Phone: normalizeDial(s.userPhone(ctx, userID)).digits, Extension: acct.Extension}, nil
}

// SetPhone stores the member's own number so other members can call it (from
// their contacts) and so the callee sees the right contact name.
func (s *Service) SetPhone(ctx context.Context, userID, raw string) (*PhoneView, error) {
	if s.db == nil {
		return nil, errors.New("localtel database is unavailable")
	}
	phone, err := NormalizePhone(raw)
	if err != nil {
		return nil, err
	}
	if _, err := s.db.Exec(ctx, `update users set phone = $2 where id = $1::uuid`, userID, phone); err != nil {
		return nil, err
	}
	return s.GetPhone(ctx, userID)
}

// DirectoryEntry is one callable member shown in the app's "TR Driver
// kullanıcıları" list.
type DirectoryEntry struct {
	Name      string `json:"name"`
	Extension string `json:"extension"`
	Online    bool   `json:"online"`
	userID    string
}

// Directory lists the other members that have a calling account.
func (s *Service) Directory(ctx context.Context, userID string) ([]DirectoryEntry, error) {
	if s.db == nil {
		return nil, errors.New("localtel database is unavailable")
	}
	rows, err := s.db.Query(ctx, `
		select coalesce(nullif(a.display_name, ''), nullif(u.display_name, ''), split_part(u.email, '@', 1)), a.extension, a.user_id::text
		from localtel_accounts a join users u on u.id = a.user_id
		where a.user_id <> $1::uuid
		order by 1 limit 200`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DirectoryEntry{}
	for rows.Next() {
		var e DirectoryEntry
		if err := rows.Scan(&e.Name, &e.Extension, &e.userID); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ContactMatch is a member found through a hashed contact number.
type ContactMatch struct {
	Hash      string `json:"hash"`
	Name      string `json:"name"`
	Extension string `json:"extension"`
	Online    bool   `json:"online"`
	userID    string
}

// ContactHash is sha256 (hex) of the last 10 digits of a phone number; the app
// sends these instead of raw contact numbers.
func ContactHash(phone string) string {
	d := normalizeDial(phone).digits
	if len(d) < 10 {
		return ""
	}
	sum := sha256.Sum256([]byte(d[len(d)-10:]))
	return hex.EncodeToString(sum[:])
}

// MatchContacts returns the members (other than the caller) whose registered
// phone hashes to one of the given values.
func (s *Service) MatchContacts(ctx context.Context, userID string, hashes []string) ([]ContactMatch, error) {
	if s.db == nil {
		return nil, errors.New("localtel database is unavailable")
	}
	want := make(map[string]struct{}, len(hashes))
	for _, h := range hashes {
		want[strings.ToLower(strings.TrimSpace(h))] = struct{}{}
	}
	rows, err := s.db.Query(ctx, `
		select u.phone, coalesce(nullif(a.display_name, ''), nullif(u.display_name, ''), split_part(u.email, '@', 1)), a.extension, a.user_id::text
		from localtel_accounts a join users u on u.id = a.user_id
		where a.user_id <> $1::uuid and length(u.phone) >= 10`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ContactMatch{}
	for rows.Next() {
		var phone string
		var m ContactMatch
		if err := rows.Scan(&phone, &m.Name, &m.Extension, &m.userID); err != nil {
			return nil, err
		}
		m.Hash = ContactHash(phone)
		if _, ok := want[m.Hash]; ok {
			out = append(out, m)
		}
	}
	return out, rows.Err()
}
