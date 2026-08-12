package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func Open(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database config: %w", err)
	}

	cfg.MaxConns = 8
	cfg.MinConns = 1
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.HealthCheckPeriod = 1 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}

func Migrate(ctx context.Context, db *pgxpool.Pool, freeQuotaBytes int64) error {
	ddl := []string{
		`create extension if not exists pgcrypto`,
		`create table if not exists plans (
			code text primary key,
			name text not null,
			quota_bytes bigint not null,
			price_cents bigint not null default 0,
			billing_term text not null default 'monthly',
			active boolean not null default true
		)`,
		`create table if not exists users (
			id uuid primary key default gen_random_uuid(),
			email text unique not null,
			password_hash text not null,
			display_name text not null,
			role text not null default 'user',
			plan_code text not null references plans(code),
			quota_bytes bigint not null,
			used_bytes bigint not null default 0,
			reserved_bytes bigint not null default 0,
			storage_root_id uuid,
			created_at timestamptz not null default now(),
			last_login_at timestamptz not null default now()
		)`,
		`alter table users add column if not exists reserved_bytes bigint not null default 0`,
		`create table if not exists sessions (
			id uuid primary key default gen_random_uuid(),
			user_id uuid not null references users(id) on delete cascade,
			token_hash text not null,
			expires_at timestamptz not null,
			created_at timestamptz not null default now()
		)`,
		`create index if not exists sessions_user_id_idx on sessions(user_id)`,
		`create table if not exists file_entries (
			id uuid primary key default gen_random_uuid(),
			user_id uuid not null references users(id) on delete cascade,
			parent_id uuid references file_entries(id) on delete set null,
			name text not null,
			kind text not null check (kind in ('folder', 'file')),
			storage_key text not null,
			size_bytes bigint not null default 0,
			mime_type text not null default 'application/octet-stream',
			deleted_at timestamptz,
			created_at timestamptz not null default now(),
			updated_at timestamptz not null default now()
		)`,
		`create unique index if not exists file_entries_unique_name_idx
			on file_entries(user_id, coalesce(parent_id, '00000000-0000-0000-0000-000000000000'::uuid), lower(name))
			where deleted_at is null`,
		`create table if not exists share_links (
			id uuid primary key default gen_random_uuid(),
			file_id uuid not null references file_entries(id) on delete cascade,
			token text unique not null,
			password_hash text not null default '',
			expires_at timestamptz,
			download_count bigint not null default 0,
			max_downloads bigint,
			created_by_user_id uuid not null references users(id) on delete cascade,
			created_at timestamptz not null default now()
		)`,
		`create table if not exists app_settings (
			key text primary key,
			value text not null,
			updated_at timestamptz not null default now()
		)`,
		`create table if not exists upload_batches (
			id uuid primary key default gen_random_uuid(),
			user_id uuid not null references users(id) on delete cascade,
			parent_id uuid references file_entries(id) on delete set null,
			total_bytes bigint not null,
			reserved_bytes bigint not null,
			file_count int not null,
			status text not null check (status in ('open', 'done', 'aborted', 'expired')),
			expires_at timestamptz not null,
			created_at timestamptz not null default now(),
			updated_at timestamptz not null default now()
		)`,
		`create index if not exists upload_batches_user_status_idx on upload_batches(user_id, status)`,
		`create index if not exists upload_batches_expires_idx on upload_batches(status, expires_at)`,
		`create table if not exists upload_sessions (
			id uuid primary key default gen_random_uuid(),
			batch_id uuid not null references upload_batches(id) on delete cascade,
			user_id uuid not null references users(id) on delete cascade,
			parent_id uuid references file_entries(id) on delete set null,
			relative_path text not null,
			file_name text not null,
			mime_type text not null default 'application/octet-stream',
			expected_size bigint not null,
			received_bytes bigint not null default 0,
			last_modified_ms bigint not null default 0,
			temp_key text not null,
			status text not null check (status in ('open', 'complete', 'aborted', 'expired')),
			created_at timestamptz not null default now(),
			updated_at timestamptz not null default now()
		)`,
		`create index if not exists upload_sessions_batch_idx on upload_sessions(batch_id)`,
		`create index if not exists upload_sessions_user_status_idx on upload_sessions(user_id, status)`,
		`alter table file_entries add column if not exists content_version bigint not null default 1`,
		`alter table file_entries add column if not exists content_hash text not null default ''`,
		`alter table file_entries add column if not exists client_modified_at timestamptz`,
		`alter table upload_sessions add column if not exists target_entry_id uuid`,
		`alter table upload_sessions add column if not exists expected_version bigint`,
		`alter table upload_sessions add column if not exists content_hash text not null default ''`,
		`alter table upload_sessions add column if not exists client_modified_at timestamptz`,
		`alter table upload_sessions add column if not exists device_id uuid`,
		`create table if not exists devices (
			id uuid primary key default gen_random_uuid(),
			user_id uuid not null references users(id) on delete cascade,
			name text not null,
			token_hash text unique not null,
			created_at timestamptz not null default now(),
			last_seen_at timestamptz not null default now(),
			expires_at timestamptz not null,
			revoked_at timestamptz
		)`,
		`alter table devices add column if not exists expires_at timestamptz`,
		`create index if not exists devices_user_id_idx on devices(user_id)`,
		`create index if not exists devices_token_hash_idx on devices(token_hash)`,
		`create table if not exists file_changes (
			id bigserial primary key,
			user_id uuid not null references users(id) on delete cascade,
			entry_id uuid not null,
			op text not null check (op in ('upsert', 'rename', 'move', 'trash', 'restore', 'purge')),
			name text not null default '',
			parent_id uuid,
			kind text not null default 'file',
			size_bytes bigint not null default 0,
			mime_type text not null default '',
			content_version bigint not null default 1,
			content_hash text not null default '',
			device_id uuid,
			client_modified_at timestamptz,
			created_at timestamptz not null default now()
		)`,
		`create index if not exists file_changes_user_id_id_idx on file_changes(user_id, id)`,

		// Collaboration: drives, ACL, social features
		`create table if not exists drives (
			id uuid primary key default gen_random_uuid(),
			kind text not null check (kind in ('personal', 'shared')),
			name text not null,
			owner_user_id uuid not null references users(id) on delete cascade,
			root_entry_id uuid references file_entries(id) on delete set null,
			created_at timestamptz not null default now()
		)`,
		`create index if not exists drives_owner_idx on drives(owner_user_id)`,
		`create unique index if not exists drives_personal_owner_idx on drives(owner_user_id) where kind = 'personal'`,
		`create table if not exists drive_members (
			drive_id uuid not null references drives(id) on delete cascade,
			user_id uuid not null references users(id) on delete cascade,
			role text not null check (role in ('viewer','commenter','contributor','content_manager','manager')),
			created_at timestamptz not null default now(),
			primary key (drive_id, user_id)
		)`,
		`alter table file_entries add column if not exists drive_id uuid references drives(id) on delete set null`,
		`alter table file_entries add column if not exists last_opened_at timestamptz`,
		`create index if not exists file_entries_drive_id_idx on file_entries(drive_id)`,
		`create table if not exists file_permissions (
			id uuid primary key default gen_random_uuid(),
			entry_id uuid not null references file_entries(id) on delete cascade,
			grantee_user_id uuid not null references users(id) on delete cascade,
			role text not null check (role in ('viewer','commenter','editor')),
			created_at timestamptz not null default now(),
			unique (entry_id, grantee_user_id)
		)`,
		`create index if not exists file_permissions_grantee_idx on file_permissions(grantee_user_id)`,
		`create table if not exists file_stars (
			user_id uuid not null references users(id) on delete cascade,
			entry_id uuid not null references file_entries(id) on delete cascade,
			created_at timestamptz not null default now(),
			primary key (user_id, entry_id)
		)`,
		`create table if not exists file_versions (
			id uuid primary key default gen_random_uuid(),
			entry_id uuid not null references file_entries(id) on delete cascade,
			version bigint not null,
			storage_key text not null,
			size_bytes bigint not null default 0,
			mime_type text not null default 'application/octet-stream',
			content_hash text not null default '',
			created_by_user_id uuid references users(id) on delete set null,
			created_at timestamptz not null default now(),
			unique (entry_id, version)
		)`,
		`create table if not exists file_comments (
			id uuid primary key default gen_random_uuid(),
			entry_id uuid not null references file_entries(id) on delete cascade,
			user_id uuid not null references users(id) on delete cascade,
			body text not null,
			created_at timestamptz not null default now()
		)`,
		`create index if not exists file_comments_entry_idx on file_comments(entry_id, created_at)`,
		`create table if not exists activities (
			id uuid primary key default gen_random_uuid(),
			user_id uuid not null references users(id) on delete cascade,
			actor_id uuid not null references users(id) on delete cascade,
			kind text not null,
			entry_id uuid,
			drive_id uuid,
			message text not null default '',
			created_at timestamptz not null default now()
		)`,
		`create index if not exists activities_user_created_idx on activities(user_id, created_at desc)`,
		`create table if not exists notifications (
			id uuid primary key default gen_random_uuid(),
			user_id uuid not null references users(id) on delete cascade,
			kind text not null,
			title text not null,
			body text not null default '',
			entry_id uuid,
			drive_id uuid,
			read_at timestamptz,
			created_at timestamptz not null default now()
		)`,
		`create index if not exists notifications_user_created_idx on notifications(user_id, created_at desc)`,
		`alter table share_links add column if not exists entry_id uuid references file_entries(id) on delete cascade`,
		`alter table share_links add column if not exists permission text not null default 'download'`,
		`update share_links set entry_id = file_id where entry_id is null and file_id is not null`,
		`alter table file_changes add column if not exists drive_id uuid`,
		`create table if not exists instance_license (
			id int primary key check (id = 1),
			tier text not null,
			max_users int not null default 1,
			license_key text not null default '',
			key_fingerprint text not null default '',
			customer text not null default '',
			instance_id text not null default '',
			activated_at timestamptz not null default now(),
			expires_at timestamptz,
			updated_at timestamptz not null default now()
		)`,
		`alter table instance_license add column if not exists instance_id text not null default ''`,
		`alter table users add column if not exists bonus_quota_bytes bigint not null default 0`,
		`alter table users add column if not exists email_2fa_enabled boolean not null default false`,
		`create table if not exists auth_challenges (
			id uuid primary key default gen_random_uuid(),
			user_id uuid not null references users(id) on delete cascade,
			purpose text not null,
			code_hash text not null,
			token_hash text not null,
			expires_at timestamptz not null,
			consumed_at timestamptz,
			attempts int not null default 0,
			created_at timestamptz not null default now()
		)`,
		`create index if not exists auth_challenges_token_hash_idx on auth_challenges(token_hash)`,
		`create index if not exists auth_challenges_user_purpose_idx on auth_challenges(user_id, purpose)`,
	}

	for _, stmt := range ddl {
		if _, err := db.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}

	if err := backfillPersonalDrives(ctx, db); err != nil {
		return err
	}

	seed := `insert into plans (code, name, quota_bytes, price_cents, billing_term, active)
		values
		('free', 'Free', $1, 0, 'monthly', true),
		('pro', 'Pro', $1, 0, 'monthly', false),
		('team', 'Team', $1, 0, 'monthly', false)
		on conflict (code) do update
		set name = excluded.name,
			price_cents = 0,
			billing_term = excluded.billing_term,
			active = excluded.active`
	if _, err := db.Exec(ctx, seed, freeQuotaBytes); err != nil {
		return fmt.Errorf("seed plans: %w", err)
	}
	if _, err := db.Exec(ctx, `
		update plans set price_cents = 0,
			active = case when code = 'free' then true else false end`); err != nil {
		return fmt.Errorf("free plans migration: %w", err)
	}
	// Default quota for new users: set once from disk/env; admin can override later.
	if _, err := db.Exec(ctx, `
		insert into app_settings (key, value, updated_at)
		values ('default_quota_bytes', $1, now())
		on conflict (key) do nothing`,
		fmt.Sprintf("%d", freeQuotaBytes),
	); err != nil {
		return fmt.Errorf("seed default quota: %w", err)
	}
	if _, err := db.Exec(ctx, `
		update plans p
		set quota_bytes = s.value::bigint
		from app_settings s
		where p.code = 'free' and s.key = 'default_quota_bytes'`); err != nil {
		return fmt.Errorf("sync free plan quota: %w", err)
	}
	// Existing paid-plan users → free plan code only (keep their current quota_bytes).
	if _, err := db.Exec(ctx, `
		update users set plan_code = 'free'
		where plan_code is distinct from 'free'`); err != nil {
		return fmt.Errorf("free user plan codes: %w", err)
	}
	if _, err := db.Exec(ctx, `
		insert into app_settings (key, value)
		values ('max_upload_batch_bytes', $1)
		on conflict (key) do nothing`,
		fmt.Sprintf("%d", int64(10)*1024*1024*1024),
	); err != nil {
		return fmt.Errorf("seed upload settings: %w", err)
	}
	if _, err := db.Exec(ctx, `
		update users
		set role = 'admin'
		where id = (
			select id from users order by created_at asc limit 1
		)
		and not exists (
			select 1 from users where role = 'admin'
		)`); err != nil {
		return fmt.Errorf("bootstrap admin: %w", err)
	}
	return nil
}

func backfillPersonalDrives(ctx context.Context, db *pgxpool.Pool) error {
	if _, err := db.Exec(ctx, `
		insert into drives (kind, name, owner_user_id, root_entry_id)
		select 'personal', 'My Drive', u.id, u.storage_root_id
		from users u
		where u.storage_root_id is not null
		  and not exists (
			select 1 from drives d where d.owner_user_id = u.id and d.kind = 'personal'
		  )`); err != nil {
		return fmt.Errorf("backfill personal drives: %w", err)
	}
	if _, err := db.Exec(ctx, `
		insert into drive_members (drive_id, user_id, role)
		select d.id, d.owner_user_id, 'manager'
		from drives d
		where d.kind = 'personal'
		on conflict do nothing`); err != nil {
		return fmt.Errorf("backfill drive members: %w", err)
	}
	if _, err := db.Exec(ctx, `
		update file_entries f
		set drive_id = d.id
		from drives d
		where d.kind = 'personal'
		  and d.owner_user_id = f.user_id
		  and f.drive_id is null`); err != nil {
		return fmt.Errorf("backfill entry drive_id: %w", err)
	}
	return nil
}
