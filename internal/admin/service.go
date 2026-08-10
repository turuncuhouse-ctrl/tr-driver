package admin

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Summary struct {
	UserCount       int64 `json:"userCount"`
	AdminCount      int64 `json:"adminCount"`
	FileCount       int64 `json:"fileCount"`
	ShareCount      int64 `json:"shareCount"`
	UsedBytes       int64 `json:"usedBytes"`
	AssignedBytes   int64 `json:"assignedBytes"`
}

type User struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	DisplayName  string    `json:"displayName"`
	Role         string    `json:"role"`
	PlanCode     string    `json:"planCode"`
	QuotaBytes   int64     `json:"quotaBytes"`
	UsedBytes    int64     `json:"usedBytes"`
	CreatedAt    time.Time `json:"createdAt"`
	LastLoginAt  time.Time `json:"lastLoginAt"`
}

type Service struct {
	db *pgxpool.Pool
}

func NewService(db *pgxpool.Pool) *Service {
	return &Service{db: db}
}

type Settings struct {
	MaxUploadBatchBytes int64 `json:"maxUploadBatchBytes"`
	UploadChunkBytes    int64 `json:"uploadChunkBytes"`
}

func (s *Service) Settings(ctx context.Context, defaultBatch, chunkBytes int64) (Settings, error) {
	settings := Settings{MaxUploadBatchBytes: defaultBatch, UploadChunkBytes: chunkBytes}
	var raw string
	err := s.db.QueryRow(ctx, `select value from app_settings where key = 'max_upload_batch_bytes'`).Scan(&raw)
	if err == nil {
		if parsed, parseErr := strconv.ParseInt(raw, 10, 64); parseErr == nil && parsed > 0 {
			settings.MaxUploadBatchBytes = parsed
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return Settings{}, err
	}
	return settings, nil
}

func (s *Service) SetMaxUploadBatchBytes(ctx context.Context, bytes int64) error {
	const minBytes = 64 * 1024 * 1024
	const maxBytes = 50 * 1024 * 1024 * 1024
	if bytes < minBytes || bytes > maxBytes {
		return fmt.Errorf("batch limit must be between %d and %d bytes", minBytes, maxBytes)
	}
	_, err := s.db.Exec(ctx, `
		insert into app_settings (key, value, updated_at)
		values ('max_upload_batch_bytes', $1, now())
		on conflict (key) do update set value = excluded.value, updated_at = now()`,
		strconv.FormatInt(bytes, 10),
	)
	return err
}

func (s *Service) Summary(ctx context.Context) (Summary, error) {
	var result Summary
	err := s.db.QueryRow(ctx, `
		select
			(select count(*) from users),
			(select count(*) from users where role = 'admin'),
			(select count(*) from file_entries where kind = 'file' and deleted_at is null),
			(select count(*) from share_links),
			coalesce((select sum(used_bytes) from users), 0),
			coalesce((select sum(quota_bytes) from users), 0)`,
	).Scan(
		&result.UserCount,
		&result.AdminCount,
		&result.FileCount,
		&result.ShareCount,
		&result.UsedBytes,
		&result.AssignedBytes,
	)
	return result, err
}

func (s *Service) Users(ctx context.Context) ([]User, error) {
	rows, err := s.db.Query(ctx, `
		select id::text, email, display_name, role, plan_code, quota_bytes,
		       used_bytes, created_at, last_login_at
		from users
		order by created_at desc
		limit 500`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	users := make([]User, 0)
	for rows.Next() {
		var user User
		if err := rows.Scan(
			&user.ID,
			&user.Email,
			&user.DisplayName,
			&user.Role,
			&user.PlanCode,
			&user.QuotaBytes,
			&user.UsedBytes,
			&user.CreatedAt,
			&user.LastLoginAt,
		); err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	return users, rows.Err()
}

func (s *Service) SetPlan(ctx context.Context, userID, planCode string) error {
	result, err := s.db.Exec(ctx, `
		update users u
		set plan_code = p.code, quota_bytes = greatest(p.quota_bytes, u.used_bytes)
		from plans p
		where u.id = $1::uuid and p.code = $2 and p.active = true`,
		userID, planCode,
	)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return errors.New("user or plan not found")
	}
	return nil
}

func (s *Service) SetQuota(ctx context.Context, userID string, quotaBytes int64) error {
	if quotaBytes < 0 || quotaBytes > 10*1024*1024*1024*1024*1024 {
		return errors.New("invalid quota")
	}
	result, err := s.db.Exec(ctx, `
		update users
		set quota_bytes = $1
		where id = $2::uuid and used_bytes <= $1`,
		quotaBytes, userID,
	)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return errors.New("user not found or quota is below current usage")
	}
	return nil
}

func (s *Service) SetRole(ctx context.Context, actorID, userID, role string) error {
	if role != "user" && role != "admin" {
		return errors.New("role must be user or admin")
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var currentRole string
	if err := tx.QueryRow(ctx, `select role from users where id = $1::uuid for update`, userID).Scan(&currentRole); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errors.New("user not found")
		}
		return err
	}
	if currentRole == "admin" && role == "user" {
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(741852963)`); err != nil {
			return err
		}
		var adminCount int
		if err := tx.QueryRow(ctx, `select count(*) from users where role = 'admin'`).Scan(&adminCount); err != nil {
			return err
		}
		if adminCount <= 1 {
			return errors.New("the last admin cannot be demoted")
		}
		if actorID == userID {
			return errors.New("you cannot remove your own admin role")
		}
	}
	if _, err := tx.Exec(ctx, `update users set role = $1 where id = $2::uuid`, role, userID); err != nil {
		return fmt.Errorf("update role: %w", err)
	}
	return tx.Commit(ctx)
}
