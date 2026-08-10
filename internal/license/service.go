package license

import (
	"context"
	"errors"
	"time"

	"necipdrive/internal/version"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Status struct {
	Tier              string     `json:"tier"`
	MaxUsers          int        `json:"maxUsers"`
	UserCount         int        `json:"userCount"`
	SeatsRemaining    int        `json:"seatsRemaining"` // -1 if unlimited
	ActivatedAt       *time.Time `json:"activatedAt,omitempty"`
	ExpiresAt         *time.Time `json:"expiresAt,omitempty"`
	Customer          string     `json:"customer,omitempty"`
	InstanceID        string     `json:"instanceId"`
	UsingDefaultKey   bool       `json:"usingDefaultKey"`
	CanRegisterPublic bool       `json:"canRegisterPublic"`
	VendorMode        bool       `json:"vendorMode"`
	CanIssueLicenses  bool       `json:"canIssueLicenses"`
	Catalog           []TierInfo `json:"catalog"`
}

type Service struct {
	db                *pgxpool.Pool
	allowRegistration bool
	vendorMode        bool
}

func NewService(db *pgxpool.Pool, allowRegistration, vendorMode bool) *Service {
	return &Service{db: db, allowRegistration: allowRegistration, vendorMode: vendorMode}
}

func (s *Service) EnsureInstanceID(ctx context.Context) (string, error) {
	var id string
	err := s.db.QueryRow(ctx, `select value from app_settings where key = 'instance_id'`).Scan(&id)
	if err == nil && id != "" {
		return id, nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	id = uuid.NewString()
	_, err = s.db.Exec(ctx, `
		insert into app_settings (key, value, updated_at) values ('instance_id', $1, now())
		on conflict (key) do update set value = excluded.value, updated_at = now()`, id)
	if err != nil {
		return "", err
	}
	return id, nil
}

func (s *Service) Status(ctx context.Context) (*Status, error) {
	instanceID, err := s.EnsureInstanceID(ctx)
	if err != nil {
		return nil, err
	}
	var userCount int
	if err := s.db.QueryRow(ctx, `select count(*) from users`).Scan(&userCount); err != nil {
		return nil, err
	}
	st := &Status{
		Tier:             TierUnlicensed,
		MaxUsers:         UnlicensedMaxUsers,
		UserCount:        userCount,
		InstanceID:       instanceID,
		UsingDefaultKey:  IsUsingDefaultPublicKey(),
		VendorMode:       s.vendorMode,
		CanIssueLicenses: s.vendorMode && CanSignLocally(),
		Catalog:          Catalog(),
	}
	var tier, customer string
	var maxUsers int
	var activatedAt time.Time
	var expiresAt *time.Time
	err = s.db.QueryRow(ctx, `
		select tier, max_users, activated_at, expires_at, coalesce(customer,'')
		from instance_license where id = 1`).Scan(&tier, &maxUsers, &activatedAt, &expiresAt, &customer)
	if err == nil {
		if expiresAt != nil && time.Now().After(*expiresAt) {
			// treat as unlicensed
		} else {
			st.Tier = tier
			st.MaxUsers = maxUsers
			st.ActivatedAt = &activatedAt
			st.ExpiresAt = expiresAt
			st.Customer = customer
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	if st.MaxUsers == 0 {
		st.SeatsRemaining = -1
	} else {
		left := st.MaxUsers - userCount
		if left < 0 {
			left = 0
		}
		st.SeatsRemaining = left
	}
	st.CanRegisterPublic = s.allowRegistration && s.seatsAvailable(st)
	return st, nil
}

func (s *Service) seatsAvailable(st *Status) bool {
	if st.MaxUsers == 0 {
		return true
	}
	return st.UserCount < st.MaxUsers
}

func (s *Service) EnsureCanAddUser(ctx context.Context) error {
	st, err := s.Status(ctx)
	if err != nil {
		return err
	}
	if !s.seatsAvailable(st) {
		return errors.New("user seat limit reached; activate a higher TR Driver license")
	}
	return nil
}

func (s *Service) RegistrationAllowed(ctx context.Context) error {
	if !s.allowRegistration {
		var n int
		_ = s.db.QueryRow(ctx, `select count(*) from users`).Scan(&n)
		if n > 0 {
			return errors.New("public registration is disabled")
		}
	}
	return s.EnsureCanAddUser(ctx)
}

func (s *Service) CreateRequest(ctx context.Context, tier string) (string, *RequestPayload, error) {
	if _, ok := MaxUsersForTier(tier); !ok {
		return "", nil, errors.New("unknown tier")
	}
	st, err := s.Status(ctx)
	if err != nil {
		return "", nil, err
	}
	max, _ := MaxUsersForTier(tier)
	req := RequestPayload{
		InstanceID: st.InstanceID,
		Tier:       tier,
		MaxUsers:   max,
		UserCount:  st.UserCount,
		Product:    version.ProductName,
		Version:    version.Version,
		CreatedAt:  time.Now().Unix(),
	}
	code, err := EncodeRequest(req)
	if err != nil {
		return "", nil, err
	}
	_, _ = s.db.Exec(ctx, `
		insert into app_settings (key, value, updated_at) values ('last_license_request', $1, now())
		on conflict (key) do update set value = excluded.value, updated_at = now()`, code)
	return code, &req, nil
}

func (s *Service) Activate(ctx context.Context, key string) (*Status, error) {
	payload, err := Verify(key)
	if err != nil {
		return nil, err
	}
	instanceID, err := s.EnsureInstanceID(ctx)
	if err != nil {
		return nil, err
	}
	if payload.InstanceID == "" {
		return nil, errors.New("license is not bound to an instance; request a new key from vendor using your demand code")
	}
	if payload.InstanceID != instanceID {
		return nil, errors.New("license issued for a different TR Driver instance")
	}
	maxUsers := payload.MaxUsers
	if max, ok := MaxUsersForTier(payload.Tier); ok {
		if payload.Tier == TierUnlimited {
			maxUsers = 0
		} else if maxUsers == 0 {
			maxUsers = max
		}
	}
	var expires *time.Time
	if payload.Exp > 0 {
		t := time.Unix(payload.Exp, 0).UTC()
		expires = &t
	}
	fp := keyFingerprint(key)
	_, err = s.db.Exec(ctx, `
		insert into instance_license (id, tier, max_users, license_key, key_fingerprint, customer, instance_id, activated_at, expires_at, updated_at)
		values (1, $1, $2, $3, $4, $5, $6, now(), $7, now())
		on conflict (id) do update set
			tier = excluded.tier,
			max_users = excluded.max_users,
			license_key = excluded.license_key,
			key_fingerprint = excluded.key_fingerprint,
			customer = excluded.customer,
			instance_id = excluded.instance_id,
			activated_at = now(),
			expires_at = excluded.expires_at,
			updated_at = now()`,
		payload.Tier, maxUsers, key, fp, payload.Customer, payload.InstanceID, expires)
	if err != nil {
		return nil, err
	}
	return s.Status(ctx)
}

func (s *Service) IssueFromRequest(ctx context.Context, requestCode, tier string, years int, customer, note string) (string, *RequestPayload, error) {
	if !s.vendorMode {
		return "", nil, errors.New("vendor mode disabled")
	}
	if !CanSignLocally() {
		return "", nil, errors.New("LICENSE_PRIVATE_KEY not configured on vendor server")
	}
	req, err := ParseRequest(requestCode)
	if err != nil {
		return "", nil, err
	}
	key, err := SignFromRequest(req, tier, years, customer, note)
	if err != nil {
		return "", nil, err
	}
	return key, req, nil
}

func keyFingerprint(key string) string {
	parts := splitKey(key)
	if len(parts) < 2 {
		return ""
	}
	if len(parts[1]) <= 12 {
		return parts[1]
	}
	return parts[1][:12]
}

func splitKey(key string) []string {
	out := make([]string, 0, 3)
	start := 0
	for i := 0; i < len(key); i++ {
		if key[i] == '.' {
			out = append(out, key[start:i])
			start = i + 1
		}
	}
	out = append(out, key[start:])
	return out
}
