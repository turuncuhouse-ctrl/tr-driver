package shares

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	"necipdrive/internal/access"
	"necipdrive/internal/config"
	"necipdrive/internal/domain"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	db     *pgxpool.Pool
	cfg    config.Config
	access *access.Service
}

func NewService(db *pgxpool.Pool, cfg config.Config, accessSvc *access.Service) *Service {
	return &Service{db: db, cfg: cfg, access: accessSvc}
}

type CreateOpts struct {
	EntryID      string
	Password     string
	ExpiresAt    *time.Time
	MaxDownloads *int64
	Permission   string // view | download
}

func (s *Service) Create(ctx context.Context, userID, userRole string, opts CreateOpts) (*domain.ShareLink, error) {
	if err := s.access.Require(ctx, userID, userRole, opts.EntryID, access.ActionManage); err != nil {
		return nil, err
	}
	perm := strings.ToLower(strings.TrimSpace(opts.Permission))
	if perm == "" {
		perm = "download"
	}
	if perm != "view" && perm != "download" {
		return nil, errors.New("invalid permission")
	}
	token, err := randomToken(s.cfg.ShareTokenBytes)
	if err != nil {
		return nil, err
	}
	var passwordHash string
	if opts.Password != "" {
		passwordHash = hashSharePassword(opts.Password, s.cfg.SharePasswordSalt)
	}
	var link domain.ShareLink
	err = s.db.QueryRow(ctx, `
		insert into share_links (file_id, entry_id, token, password_hash, expires_at, max_downloads, permission, created_by_user_id)
		select f.id, f.id, $2, $3, $4, $5, $6, $7::uuid
		from file_entries f
		where f.id = $1::uuid and f.deleted_at is null
		returning id::text, coalesce(entry_id::text, file_id::text), coalesce(entry_id::text, file_id::text), token, password_hash,
		          permission, expires_at, download_count, max_downloads, created_by_user_id::text, created_at`,
		opts.EntryID, token, passwordHash, opts.ExpiresAt, opts.MaxDownloads, perm, userID,
	).Scan(&link.ID, &link.EntryID, &link.FileID, &link.Token, &link.PasswordHash, &link.Permission,
		&link.ExpiresAt, &link.DownloadCount, &link.MaxDownloads, &link.CreatedByUserID, &link.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errors.New("file not found")
		}
		return nil, err
	}
	link.HasPassword = link.PasswordHash != ""
	return &link, nil
}

func (s *Service) ListMine(ctx context.Context, userID string) ([]domain.ShareLink, error) {
	rows, err := s.db.Query(ctx, `
		select s.id::text, coalesce(s.entry_id::text, s.file_id::text), coalesce(s.entry_id::text, s.file_id::text),
		       s.token, s.password_hash, s.permission, s.expires_at, s.download_count, s.max_downloads,
		       s.created_by_user_id::text, s.created_at, f.name, f.kind
		from share_links s join file_entries f on f.id = coalesce(s.entry_id, s.file_id)
		where s.created_by_user_id = $1::uuid
		order by s.created_at desc`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.ShareLink, 0)
	for rows.Next() {
		var link domain.ShareLink
		if err := rows.Scan(&link.ID, &link.EntryID, &link.FileID, &link.Token, &link.PasswordHash, &link.Permission,
			&link.ExpiresAt, &link.DownloadCount, &link.MaxDownloads, &link.CreatedByUserID, &link.CreatedAt,
			&link.EntryName, &link.EntryKind); err != nil {
			return nil, err
		}
		link.HasPassword = link.PasswordHash != ""
		link.PasswordHash = ""
		out = append(out, link)
	}
	return out, rows.Err()
}

func (s *Service) Update(ctx context.Context, userID, linkID string, password *string, expiresAt *time.Time, maxDownloads *int64, permission string) error {
	var owner string
	if err := s.db.QueryRow(ctx, `select created_by_user_id::text from share_links where id = $1::uuid`, linkID).Scan(&owner); err != nil {
		return errors.New("share not found")
	}
	if owner != userID {
		return errors.New("forbidden")
	}
	sets := []string{}
	args := []any{linkID}
	idx := 2
	if password != nil {
		hash := ""
		if *password != "" {
			hash = hashSharePassword(*password, s.cfg.SharePasswordSalt)
		}
		sets = append(sets, "password_hash = $"+strconv.Itoa(idx))
		args = append(args, hash)
		idx++
	}
	if expiresAt != nil {
		sets = append(sets, "expires_at = $"+strconv.Itoa(idx))
		args = append(args, *expiresAt)
		idx++
	}
	if maxDownloads != nil {
		sets = append(sets, "max_downloads = $"+strconv.Itoa(idx))
		args = append(args, *maxDownloads)
		idx++
	}
	if permission != "" {
		sets = append(sets, "permission = $"+strconv.Itoa(idx))
		args = append(args, permission)
		idx++
	}
	if len(sets) == 0 {
		return nil
	}
	_, err := s.db.Exec(ctx, `update share_links set `+strings.Join(sets, ", ")+` where id = $1::uuid`, args...)
	return err
}

func (s *Service) Revoke(ctx context.Context, userID, linkID string) error {
	tag, err := s.db.Exec(ctx, `delete from share_links where id = $1::uuid and created_by_user_id = $2::uuid`, linkID, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("share not found")
	}
	return nil
}

func (s *Service) Resolve(ctx context.Context, token, password string, countDownload bool) (*domain.ShareLink, error) {
	var link domain.ShareLink
	err := s.db.QueryRow(ctx, `
		select id::text, coalesce(entry_id::text, file_id::text), coalesce(entry_id::text, file_id::text), token, password_hash,
		       coalesce(nullif(permission,''),'download'), expires_at, download_count, max_downloads, created_by_user_id::text, created_at
		from share_links where token = $1`, token,
	).Scan(&link.ID, &link.EntryID, &link.FileID, &link.Token, &link.PasswordHash, &link.Permission,
		&link.ExpiresAt, &link.DownloadCount, &link.MaxDownloads, &link.CreatedByUserID, &link.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errors.New("share not found")
		}
		return nil, err
	}
	if link.ExpiresAt != nil && time.Now().After(*link.ExpiresAt) {
		return nil, errors.New("share expired")
	}
	link.HasPassword = link.PasswordHash != ""
	if link.PasswordHash != "" {
		got := hashSharePassword(password, s.cfg.SharePasswordSalt)
		if subtle.ConstantTimeCompare([]byte(got), []byte(link.PasswordHash)) != 1 {
			return nil, errors.New("invalid share password")
		}
	}
	if countDownload {
		var newCount int64
		err := s.db.QueryRow(ctx, `
			update share_links
			set download_count = download_count + 1
			where id = $1::uuid
			  and (max_downloads is null or download_count < max_downloads)
			returning download_count`, link.ID).Scan(&newCount)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, errors.New("download limit reached")
			}
			return nil, err
		}
		link.DownloadCount = newCount
	} else if link.MaxDownloads != nil && link.DownloadCount >= *link.MaxDownloads {
		return nil, errors.New("download limit reached")
	}
	return &link, nil
}

type PublicMeta struct {
	HasPassword bool   `json:"hasPassword"`
	Expired     bool   `json:"expired"`
	Permission  string `json:"permission,omitempty"`
	EntryName   string `json:"entryName,omitempty"`
	EntryKind   string `json:"entryKind,omitempty"`
	EntryID     string `json:"entryId,omitempty"`
	Unlocked    bool   `json:"unlocked"`
}

func (s *Service) Meta(ctx context.Context, token, password string) (*PublicMeta, error) {
	var link domain.ShareLink
	var name, kind string
	err := s.db.QueryRow(ctx, `
		select s.id::text, coalesce(s.entry_id::text, s.file_id::text), s.password_hash,
		       coalesce(nullif(s.permission,''),'download'), s.expires_at, f.name, f.kind
		from share_links s join file_entries f on f.id = coalesce(s.entry_id, s.file_id)
		where s.token = $1 and f.deleted_at is null`, token,
	).Scan(&link.ID, &link.EntryID, &link.PasswordHash, &link.Permission, &link.ExpiresAt, &name, &kind)
	if err != nil {
		return nil, errors.New("share not found")
	}
	meta := &PublicMeta{HasPassword: link.PasswordHash != ""}
	if link.ExpiresAt != nil && time.Now().After(*link.ExpiresAt) {
		meta.Expired = true
		return meta, nil
	}
	if link.PasswordHash != "" {
		got := hashSharePassword(password, s.cfg.SharePasswordSalt)
		if subtle.ConstantTimeCompare([]byte(got), []byte(link.PasswordHash)) != 1 {
			return meta, nil
		}
	}
	meta.Unlocked = true
	meta.Permission = link.Permission
	meta.EntryName = name
	meta.EntryKind = kind
	meta.EntryID = link.EntryID
	return meta, nil
}

func randomToken(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func hashSharePassword(password, salt string) string {
	sum := sha256.Sum256([]byte(salt + ":" + password))
	return hex.EncodeToString(sum[:])
}
