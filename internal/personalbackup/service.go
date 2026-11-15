// Package personalbackup stores end-to-end protected phone data (contacts and
// SMS) for the Android backup client.
//
// Security properties:
//   - every query is scoped to one authenticated user id,
//   - payloads are AES-256-GCM encrypted at rest (see Vault), bound to their
//     owner with additional authenticated data,
//   - row keys are blind indexes (HMAC-SHA256), never the client item key,
//   - request bodies are size limited and validated before touching the
//     database,
//   - no handler logs request contents, so no PII reaches the log.
package personalbackup

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Kind is the class of personal data backed up by one endpoint pair.
type Kind string

const (
	KindContacts Kind = "contacts"
	KindSMS      Kind = "sms"
)

// Request limits. They keep a single request cheap to validate and to write.
const (
	MaxItemsPerRequest     = 500
	MaxDeletionsPerRequest = 2000
	MaxRequestBytes        = 4 << 20 // 4 MiB
	DefaultPageSize        = 200
	MaxPageSize            = 500
)

const (
	maxContactsItemBytes = 16 << 10 // 16 KiB: names, phones, addresses, notes
	maxSMSItemBytes      = 64 << 10 // 64 KiB: concatenated messages, MMS text
	maxDeviceIDLen       = 100
)

// ErrInvalidInput marks client mistakes; handlers map it to HTTP 400 and never
// echo the offending value back (it could be personal data).
var ErrInvalidInput = errors.New("invalid input")

// ParseKind accepts only the two supported data kinds.
func ParseKind(raw string) (Kind, error) {
	switch Kind(strings.ToLower(strings.TrimSpace(raw))) {
	case KindContacts:
		return KindContacts, nil
	case KindSMS:
		return KindSMS, nil
	default:
		return "", fmt.Errorf("%w: unknown backup kind", ErrInvalidInput)
	}
}

func maxItemBytes(kind Kind) int {
	if kind == KindSMS {
		return maxSMSItemBytes
	}
	return maxContactsItemBytes
}

// Item is one client record pushed by the phone.
type Item struct {
	Key      string          `json:"key"`
	Revision int64           `json:"revision"`
	Hash     string          `json:"hash"`
	Data     json.RawMessage `json:"data"`
}

// SyncRequest is one incremental batch: changed records plus records the phone
// deleted since the previous run.
type SyncRequest struct {
	DeviceID string   `json:"deviceId,omitempty"`
	Items    []Item   `json:"items"`
	Deleted  []string `json:"deleted,omitempty"`
}

// SyncResult reports what the server actually changed, so the client can tell
// an unchanged, an accepted and a stale (older revision) write apart.
type SyncResult struct {
	Inserted  int       `json:"inserted"`
	Updated   int       `json:"updated"`
	Unchanged int       `json:"unchanged"`
	Stale     int       `json:"stale"`
	Deleted   int       `json:"deleted"`
	Total     int       `json:"total"`
	SyncedAt  time.Time `json:"syncedAt"`
}

// ListItem is one decrypted record returned for restore.
type ListItem struct {
	Key       string          `json:"key"`
	Revision  int64           `json:"revision"`
	Hash      string          `json:"hash"`
	Data      json.RawMessage `json:"data"`
	UpdatedAt time.Time       `json:"updatedAt"`
}

// ListPage is a keyset-paginated restore page.
type ListPage struct {
	Kind      Kind       `json:"kind"`
	Items     []ListItem `json:"items"`
	NextAfter string     `json:"nextAfter,omitempty"`
	Total     int        `json:"total"`
}

// ListOptions selects a restore page. Since is exclusive.
type ListOptions struct {
	Since *time.Time
	After string
	Limit int
}

// KindStatus is the per-kind count/last-sync summary.
type KindStatus struct {
	Kind       Kind       `json:"kind"`
	Count      int        `json:"count"`
	LastSyncAt *time.Time `json:"lastSyncAt,omitempty"`
}

// Status is the whole account summary exposed to the client.
type Status struct {
	Kinds []KindStatus `json:"kinds"`
}

// envelope is what actually gets encrypted: the client key travels inside the
// ciphertext, so the row key stays blind.
type envelope struct {
	Key      string          `json:"key"`
	Revision int64           `json:"revision"`
	Hash     string          `json:"hash"`
	Data     json.RawMessage `json:"data"`
}

type Service struct {
	db    *pgxpool.Pool
	vault *Vault
}

func NewService(db *pgxpool.Pool, vault *Vault) (*Service, error) {
	if vault == nil {
		return nil, errors.New("personal backup encryption vault is required")
	}
	return &Service{db: db, vault: vault}, nil
}

func normalizeKey(raw string) (string, error) {
	key := strings.TrimSpace(raw)
	if len(key) < 8 || len(key) > 128 {
		return "", fmt.Errorf("%w: invalid item key length", ErrInvalidInput)
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '-' || c == '_' || c == '=' || c == '.'
		if !ok {
			return "", fmt.Errorf("%w: invalid item key", ErrInvalidInput)
		}
	}
	return key, nil
}

// normalizeHash accepts a lowercase/uppercase SHA-256 hex digest or "" when the
// client did not compute one.
func normalizeHash(raw string) (string, error) {
	hash := strings.ToLower(strings.TrimSpace(raw))
	if hash == "" {
		return "", nil
	}
	if len(hash) != 64 {
		return "", fmt.Errorf("%w: invalid content hash", ErrInvalidInput)
	}
	if _, err := hex.DecodeString(hash); err != nil {
		return "", fmt.Errorf("%w: invalid content hash", ErrInvalidInput)
	}
	return hash, nil
}

// normalizeData validates size and JSON shape without interpreting the fields.
// Personal fields stay opaque to the server on purpose.
func normalizeData(kind Kind, raw json.RawMessage) (json.RawMessage, error) {
	data := bytes.TrimSpace(raw)
	if len(data) == 0 {
		return nil, fmt.Errorf("%w: empty record", ErrInvalidInput)
	}
	if len(data) > maxItemBytes(kind) {
		return nil, fmt.Errorf("%w: record too large", ErrInvalidInput)
	}
	if data[0] != '{' || !json.Valid(data) {
		return nil, fmt.Errorf("%w: record must be a JSON object", ErrInvalidInput)
	}
	return json.RawMessage(data), nil
}

func normalizeDeviceID(raw string) string {
	device := strings.TrimSpace(raw)
	if device == "" {
		return "unknown"
	}
	// Keep only safe printable characters: the id is stored and later echoed.
	var b strings.Builder
	for _, r := range device {
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
		if b.Len() >= maxDeviceIDLen {
			break
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "unknown"
	}
	return out
}

type preparedItem struct {
	blind    string
	revision int64
	hash     string
	payload  []byte
	size     int
}

// Sync applies one incremental batch. The whole batch commits atomically, so a
// dropped connection never leaves the client and the server half-agreeing.
func (s *Service) Sync(ctx context.Context, userID string, kind Kind, req *SyncRequest) (*SyncResult, error) {
	if userID == "" {
		return nil, fmt.Errorf("%w: user required", ErrInvalidInput)
	}
	if req == nil {
		return nil, fmt.Errorf("%w: empty request", ErrInvalidInput)
	}
	if len(req.Items) > MaxItemsPerRequest {
		return nil, fmt.Errorf("%w: too many items in one request", ErrInvalidInput)
	}
	if len(req.Deleted) > MaxDeletionsPerRequest {
		return nil, fmt.Errorf("%w: too many deletions in one request", ErrInvalidInput)
	}

	deviceID := normalizeDeviceID(req.DeviceID)
	prepared := make([]preparedItem, 0, len(req.Items))
	seen := make(map[string]struct{}, len(req.Items))
	for i := range req.Items {
		item := req.Items[i]
		key, err := normalizeKey(item.Key)
		if err != nil {
			return nil, err
		}
		hash, err := normalizeHash(item.Hash)
		if err != nil {
			return nil, err
		}
		if item.Revision < 0 {
			return nil, fmt.Errorf("%w: negative revision", ErrInvalidInput)
		}
		data, err := normalizeData(kind, item.Data)
		if err != nil {
			return nil, err
		}
		blind := s.vault.BlindKey(string(kind), key)
		if _, dup := seen[blind]; dup {
			return nil, fmt.Errorf("%w: duplicate item key", ErrInvalidInput)
		}
		seen[blind] = struct{}{}
		plain, err := json.Marshal(envelope{Key: key, Revision: item.Revision, Hash: hash, Data: data})
		if err != nil {
			return nil, err
		}
		sealed, err := s.vault.Seal(plain, aadFor(userID, kind))
		if err != nil {
			return nil, err
		}
		prepared = append(prepared, preparedItem{
			blind:    blind,
			revision: item.Revision,
			hash:     hash,
			payload:  sealed,
			size:     len(plain),
		})
	}

	deletedBlind := make([]string, 0, len(req.Deleted))
	deletedSeen := make(map[string]struct{}, len(req.Deleted))
	for _, raw := range req.Deleted {
		key, err := normalizeKey(raw)
		if err != nil {
			return nil, err
		}
		blind := s.vault.BlindKey(string(kind), key)
		if _, dup := deletedSeen[blind]; dup {
			continue
		}
		if _, conflict := seen[blind]; conflict {
			return nil, fmt.Errorf("%w: item both written and deleted", ErrInvalidInput)
		}
		deletedSeen[blind] = struct{}{}
		deletedBlind = append(deletedBlind, blind)
	}

	result := &SyncResult{SyncedAt: time.Now().UTC()}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	stored := map[string]struct {
		revision int64
		hash     string
	}{}
	if len(prepared) > 0 {
		blinds := make([]string, 0, len(prepared))
		for _, item := range prepared {
			blinds = append(blinds, item.blind)
		}
		rows, err := tx.Query(ctx, `
			select blind_key, revision, content_hash
			from personal_backup_items
			where user_id = $1::uuid and kind = $2 and blind_key = any($3::text[])`,
			userID, string(kind), blinds)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var blind string
			var revision int64
			var hash string
			if err := rows.Scan(&blind, &revision, &hash); err != nil {
				rows.Close()
				return nil, err
			}
			stored[blind] = struct {
				revision int64
				hash     string
			}{revision: revision, hash: hash}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}

	for _, item := range prepared {
		existing, found := stored[item.blind]
		if found {
			// Identical content: no write, no ciphertext churn.
			if item.hash != "" && item.hash == existing.hash {
				result.Unchanged++
				continue
			}
			// The phone sent an older revision than the one already stored
			// (e.g. a queued retry replayed after a restore). Never regress.
			if item.revision > 0 && existing.revision > item.revision {
				result.Stale++
				continue
			}
			// No revision supplied: last writer wins on content change.
			if _, err := tx.Exec(ctx, `
				update personal_backup_items
				set revision = $4, content_hash = $5, payload = $6, payload_bytes = $7, updated_at = now()
				where user_id = $1::uuid and kind = $2 and blind_key = $3`,
				userID, string(kind), item.blind, item.revision, item.hash, item.payload, item.size); err != nil {
				return nil, err
			}
			result.Updated++
			continue
		}
		if _, err := tx.Exec(ctx, `
			insert into personal_backup_items
				(user_id, kind, blind_key, revision, content_hash, payload, payload_bytes)
			values ($1::uuid, $2, $3, $4, $5, $6, $7)
			on conflict (user_id, kind, blind_key) do update
			set revision = excluded.revision,
			    content_hash = excluded.content_hash,
			    payload = excluded.payload,
			    payload_bytes = excluded.payload_bytes,
			    updated_at = now()`,
			userID, string(kind), item.blind, item.revision, item.hash, item.payload, item.size); err != nil {
			return nil, err
		}
		result.Inserted++
	}

	if len(deletedBlind) > 0 {
		tag, err := tx.Exec(ctx, `
			delete from personal_backup_items
			where user_id = $1::uuid and kind = $2 and blind_key = any($3::text[])`,
			userID, string(kind), deletedBlind)
		if err != nil {
			return nil, err
		}
		result.Deleted = int(tag.RowsAffected())
	}

	if err := tx.QueryRow(ctx, `
		select count(*) from personal_backup_items where user_id = $1::uuid and kind = $2`,
		userID, string(kind)).Scan(&result.Total); err != nil {
		return nil, err
	}

	if _, err := tx.Exec(ctx, `
		insert into personal_backup_syncs
			(user_id, kind, device_id, last_sync_at, last_items, last_deleted, total_items)
		values ($1::uuid, $2, $3, now(), $4, $5, $6)
		on conflict (user_id, kind, device_id) do update
		set last_sync_at = now(),
		    last_items = excluded.last_items,
		    last_deleted = excluded.last_deleted,
		    total_items = excluded.total_items`,
		userID, string(kind), deviceID, len(prepared), result.Deleted, result.Total); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

// List returns one restore page. Only the owning user's rows are considered.
func (s *Service) List(ctx context.Context, userID string, kind Kind, opts ListOptions) (*ListPage, error) {
	if userID == "" {
		return nil, fmt.Errorf("%w: user required", ErrInvalidInput)
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultPageSize
	}
	if limit > MaxPageSize {
		limit = MaxPageSize
	}
	after := strings.TrimSpace(opts.After)
	if after != "" && !isHexDigest(after) {
		return nil, fmt.Errorf("%w: invalid page cursor", ErrInvalidInput)
	}

	page := &ListPage{Kind: kind, Items: make([]ListItem, 0, limit)}
	var since any
	if opts.Since != nil {
		since = opts.Since.UTC()
	}
	rows, err := s.db.Query(ctx, `
		select blind_key, payload, updated_at
		from personal_backup_items
		where user_id = $1::uuid
		  and kind = $2
		  and ($3::timestamptz is null or updated_at > $3::timestamptz)
		  and ($4 = '' or blind_key > $4)
		order by blind_key asc
		limit $5`,
		userID, string(kind), since, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	aad := aadFor(userID, kind)
	for rows.Next() {
		var blind string
		var payload []byte
		var updatedAt time.Time
		if err := rows.Scan(&blind, &payload, &updatedAt); err != nil {
			return nil, err
		}
		plain, err := s.vault.Open(payload, aad)
		if err != nil {
			return nil, err
		}
		var env envelope
		if err := json.Unmarshal(plain, &env); err != nil {
			return nil, errVaultCiphertext
		}
		page.Items = append(page.Items, ListItem{
			Key:       env.Key,
			Revision:  env.Revision,
			Hash:      env.Hash,
			Data:      env.Data,
			UpdatedAt: updatedAt.UTC(),
		})
		page.NextAfter = blind
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(page.Items) < limit {
		page.NextAfter = ""
	}
	if err := s.db.QueryRow(ctx, `
		select count(*) from personal_backup_items where user_id = $1::uuid and kind = $2`,
		userID, string(kind)).Scan(&page.Total); err != nil {
		return nil, err
	}
	return page, nil
}

func isHexDigest(raw string) bool {
	if len(raw) != 64 {
		return false
	}
	_, err := hex.DecodeString(raw)
	return err == nil
}

// Status reports counts and the most recent sync per kind.
func (s *Service) Status(ctx context.Context, userID string) (*Status, error) {
	if userID == "" {
		return nil, fmt.Errorf("%w: user required", ErrInvalidInput)
	}
	out := &Status{Kinds: []KindStatus{
		{Kind: KindContacts},
		{Kind: KindSMS},
	}}
	rows, err := s.db.Query(ctx, `
		select kind, count(*) from personal_backup_items
		where user_id = $1::uuid group by kind`, userID)
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for rows.Next() {
		var kind string
		var count int
		if err := rows.Scan(&kind, &count); err != nil {
			rows.Close()
			return nil, err
		}
		counts[kind] = count
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	lastRows, err := s.db.Query(ctx, `
		select kind, max(last_sync_at) from personal_backup_syncs
		where user_id = $1::uuid group by kind`, userID)
	if err != nil {
		return nil, err
	}
	last := map[string]*time.Time{}
	for lastRows.Next() {
		var kind string
		var at *time.Time
		if err := lastRows.Scan(&kind, &at); err != nil {
			lastRows.Close()
			return nil, err
		}
		last[kind] = at
	}
	lastRows.Close()
	if err := lastRows.Err(); err != nil {
		return nil, err
	}

	for i := range out.Kinds {
		key := string(out.Kinds[i].Kind)
		out.Kinds[i].Count = counts[key]
		if at, ok := last[key]; ok && at != nil {
			utc := at.UTC()
			out.Kinds[i].LastSyncAt = &utc
		}
	}
	return out, nil
}

// Purge removes every stored record of one kind for the user (privacy/GDPR
// erase) and reports how many rows were deleted.
func (s *Service) Purge(ctx context.Context, userID string, kind Kind) (int64, error) {
	if userID == "" {
		return 0, fmt.Errorf("%w: user required", ErrInvalidInput)
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `delete from personal_backup_items where user_id = $1::uuid and kind = $2`,
		userID, string(kind))
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `delete from personal_backup_syncs where user_id = $1::uuid and kind = $2`,
		userID, string(kind)); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
