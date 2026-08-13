package collab

import (
	"context"
	"errors"
	"strings"
	"time"

	"necipdrive/internal/access"
	"necipdrive/internal/domain"
	"necipdrive/internal/storage"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	db      *pgxpool.Pool
	access  *access.Service
	storage *storage.Local
}

func NewService(db *pgxpool.Pool, accessSvc *access.Service, store *storage.Local) *Service {
	return &Service{db: db, access: accessSvc, storage: store}
}

// --- Permissions ---

func (s *Service) Grant(ctx context.Context, actorID, actorRole, entryID, email, role string) (*domain.FilePermission, error) {
	if err := s.access.Require(ctx, actorID, actorRole, entryID, access.ActionManage); err != nil {
		return nil, err
	}
	role = normalizeACL(role)
	if role == "" {
		return nil, errors.New("invalid role")
	}
	var grantee domain.User
	query := strings.ToLower(strings.TrimSpace(email))
	if query == "" {
		return nil, errors.New("e-posta veya görünen ad gerekli")
	}
	err := s.db.QueryRow(ctx, `
		select id::text, email, display_name from users
		where lower(email) = $1 or lower(display_name) = $1
		order by case when lower(email) = $1 then 0 else 1 end
		limit 1`, query).Scan(&grantee.ID, &grantee.Email, &grantee.DisplayName)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errors.New("kullanıcı bulunamadı (kayıtlı e-posta veya görünen ad girin)")
	}
	if err != nil {
		return nil, err
	}
	if grantee.ID == actorID {
		return nil, errors.New("kendinizle paylaşamazsınız")
	}
	var p domain.FilePermission
	err = s.db.QueryRow(ctx, `
		insert into file_permissions (entry_id, grantee_user_id, role)
		values ($1::uuid, $2::uuid, $3)
		on conflict (entry_id, grantee_user_id) do update set role = excluded.role
		returning id::text, entry_id::text, grantee_user_id::text, role, created_at`,
		entryID, grantee.ID, role,
	).Scan(&p.ID, &p.EntryID, &p.GranteeUserID, &p.Role, &p.CreatedAt)
	if err != nil {
		return nil, err
	}
	p.Email, p.DisplayName = grantee.Email, grantee.DisplayName
	_, _ = s.db.Exec(ctx, `
		insert into notifications (user_id, kind, title, body, entry_id)
		values ($1::uuid, 'share', 'Dosya paylaşıldı', $2, $3::uuid)`,
		grantee.ID, "Sizinle bir dosya/klasör paylaşıldı.", entryID)
	_, _ = s.db.Exec(ctx, `
		insert into activities (user_id, actor_id, kind, entry_id, message)
		values ($1::uuid, $2::uuid, 'share', $3::uuid, $4)`,
		grantee.ID, actorID, entryID, "Sizinle paylaşıldı")
	return &p, nil
}

func (s *Service) Revoke(ctx context.Context, actorID, actorRole, entryID, granteeUserID string) error {
	if err := s.access.Require(ctx, actorID, actorRole, entryID, access.ActionManage); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `delete from file_permissions where entry_id = $1::uuid and grantee_user_id = $2::uuid`, entryID, granteeUserID)
	return err
}

func (s *Service) ListPermissions(ctx context.Context, actorID, actorRole, entryID string) ([]domain.FilePermission, error) {
	if err := s.access.Require(ctx, actorID, actorRole, entryID, access.ActionView); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `
		select p.id::text, p.entry_id::text, p.grantee_user_id::text, u.email, u.display_name, p.role, p.created_at
		from file_permissions p join users u on u.id = p.grantee_user_id
		where p.entry_id = $1::uuid order by lower(u.email)`, entryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.FilePermission, 0)
	for rows.Next() {
		var p domain.FilePermission
		if err := rows.Scan(&p.ID, &p.EntryID, &p.GranteeUserID, &p.Email, &p.DisplayName, &p.Role, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Service) SharedWithMe(ctx context.Context, userID string) ([]domain.FileEntry, error) {
	rows, err := s.db.Query(ctx, `
		select distinct f.id::text, f.user_id::text, coalesce(f.drive_id::text,''), f.parent_id::text, f.name, f.kind,
		       f.storage_key, f.size_bytes, f.mime_type, f.content_version, f.content_hash, f.client_modified_at,
		       f.last_opened_at, f.deleted_at, f.created_at, f.updated_at
		from file_permissions p
		join file_entries f on f.id = p.entry_id
		where p.grantee_user_id = $1::uuid and f.deleted_at is null
		order by f.updated_at desc`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEntries(rows)
}

// --- Stars / Recent / Search ---

func (s *Service) SetStar(ctx context.Context, userID, userRole, entryID string, starred bool) error {
	if err := s.access.Require(ctx, userID, userRole, entryID, access.ActionView); err != nil {
		return err
	}
	if starred {
		_, err := s.db.Exec(ctx, `insert into file_stars (user_id, entry_id) values ($1::uuid, $2::uuid) on conflict do nothing`, userID, entryID)
		return err
	}
	_, err := s.db.Exec(ctx, `delete from file_stars where user_id = $1::uuid and entry_id = $2::uuid`, userID, entryID)
	return err
}

func (s *Service) ListStarred(ctx context.Context, userID string) ([]domain.FileEntry, error) {
	rows, err := s.db.Query(ctx, `
		select f.id::text, f.user_id::text, coalesce(f.drive_id::text,''), f.parent_id::text, f.name, f.kind,
		       f.storage_key, f.size_bytes, f.mime_type, f.content_version, f.content_hash, f.client_modified_at,
		       f.last_opened_at, f.deleted_at, f.created_at, f.updated_at
		from file_stars s join file_entries f on f.id = s.entry_id
		where s.user_id = $1::uuid and f.deleted_at is null
		order by s.created_at desc`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := scanEntries(rows)
	for i := range items {
		items[i].Starred = true
	}
	return items, err
}

func (s *Service) TouchOpened(ctx context.Context, entryID string) {
	_, _ = s.db.Exec(ctx, `update file_entries set last_opened_at = now() where id = $1::uuid`, entryID)
}

func (s *Service) ListRecent(ctx context.Context, userID string) ([]domain.FileEntry, error) {
	rows, err := s.db.Query(ctx, `
		select f.id::text, f.user_id::text, coalesce(f.drive_id::text,''), f.parent_id::text, f.name, f.kind,
		       f.storage_key, f.size_bytes, f.mime_type, f.content_version, f.content_hash, f.client_modified_at,
		       f.last_opened_at, f.deleted_at, f.created_at, f.updated_at
		from file_entries f
		where f.deleted_at is null and f.kind = 'file' and f.last_opened_at is not null
		  and (
			f.user_id = $1::uuid
			or exists (select 1 from drive_members dm where dm.drive_id = f.drive_id and dm.user_id = $1::uuid)
			or exists (
				with recursive chain as (
					select id, parent_id from file_entries where id = f.id
					union all
					select p.id, p.parent_id from file_entries p join chain c on p.id = c.parent_id
				)
				select 1 from chain c join file_permissions fp on fp.entry_id = c.id and fp.grantee_user_id = $1::uuid
			)
		  )
		order by f.last_opened_at desc
		limit 50`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEntries(rows)
}

func (s *Service) Search(ctx context.Context, userID, userRole, q string) ([]domain.FileEntry, error) {
	q = strings.TrimSpace(q)
	if len(q) < 1 {
		return nil, errors.New("query required")
	}
	like := "%" + strings.ToLower(q) + "%"
	rows, err := s.db.Query(ctx, `
		select f.id::text, f.user_id::text, coalesce(f.drive_id::text,''), f.parent_id::text, f.name, f.kind,
		       f.storage_key, f.size_bytes, f.mime_type, f.content_version, f.content_hash, f.client_modified_at,
		       f.last_opened_at, f.deleted_at, f.created_at, f.updated_at
		from file_entries f
		where f.deleted_at is null and lower(f.name) like $2
		  and (
			f.user_id = $1::uuid
			or exists (select 1 from drive_members dm where dm.drive_id = f.drive_id and dm.user_id = $1::uuid)
			or exists (
				with recursive chain as (
					select id, parent_id from file_entries where id = f.id
					union all
					select p.id, p.parent_id from file_entries p join chain c on p.id = c.parent_id
				)
				select 1 from chain c join file_permissions fp on fp.entry_id = c.id and fp.grantee_user_id = $1::uuid
			)
			or $3 = 'admin'
		  )
		order by lower(f.name)
		limit 100`, userID, like, userRole)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEntries(rows)
}

// --- Comments ---

func (s *Service) AddComment(ctx context.Context, user domain.User, entryID, body string) (*domain.FileComment, error) {
	if err := s.access.Require(ctx, user.ID, user.Role, entryID, access.ActionComment); err != nil {
		return nil, err
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return nil, errors.New("comment required")
	}
	var c domain.FileComment
	err := s.db.QueryRow(ctx, `
		insert into file_comments (entry_id, user_id, body) values ($1::uuid, $2::uuid, $3)
		returning id::text, entry_id::text, user_id::text, body, created_at`,
		entryID, user.ID, body,
	).Scan(&c.ID, &c.EntryID, &c.UserID, &c.Body, &c.CreatedAt)
	if err != nil {
		return nil, err
	}
	c.Email, c.DisplayName = user.Email, user.DisplayName
	var ownerID string
	_ = s.db.QueryRow(ctx, `select user_id::text from file_entries where id = $1::uuid`, entryID).Scan(&ownerID)
	if ownerID != "" && ownerID != user.ID {
		_, _ = s.db.Exec(ctx, `
			insert into notifications (user_id, kind, title, body, entry_id)
			values ($1::uuid, 'comment', 'Yeni yorum', $2, $3::uuid)`, ownerID, user.DisplayName+": "+body, entryID)
	}
	return &c, nil
}

func (s *Service) ListComments(ctx context.Context, userID, userRole, entryID string) ([]domain.FileComment, error) {
	if err := s.access.Require(ctx, userID, userRole, entryID, access.ActionView); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `
		select c.id::text, c.entry_id::text, c.user_id::text, u.email, u.display_name, c.body, c.created_at
		from file_comments c join users u on u.id = c.user_id
		where c.entry_id = $1::uuid order by c.created_at asc`, entryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.FileComment, 0)
	for rows.Next() {
		var c domain.FileComment
		if err := rows.Scan(&c.ID, &c.EntryID, &c.UserID, &c.Email, &c.DisplayName, &c.Body, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// --- Versions ---

func (s *Service) SnapshotVersion(ctx context.Context, entry domain.FileEntry, actorID string) error {
	if entry.Kind != "file" || entry.StorageKey == "" {
		return nil
	}
	_, err := s.db.Exec(ctx, `
		insert into file_versions (entry_id, version, storage_key, size_bytes, mime_type, content_hash, created_by_user_id)
		values ($1::uuid, $2, $3, $4, $5, $6, nullif($7,'')::uuid)
		on conflict (entry_id, version) do nothing`,
		entry.ID, entry.ContentVersion, entry.StorageKey, entry.SizeBytes, entry.MimeType, entry.ContentHash, actorID)
	if err != nil {
		return err
	}
	// Keep last 20
	_, _ = s.db.Exec(ctx, `
		delete from file_versions where entry_id = $1::uuid and id in (
			select id from file_versions where entry_id = $1::uuid order by version desc offset 20
		)`, entry.ID)
	return nil
}

func (s *Service) ListVersions(ctx context.Context, userID, userRole, entryID string) ([]domain.FileVersion, error) {
	if err := s.access.Require(ctx, userID, userRole, entryID, access.ActionView); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `
		select id::text, entry_id::text, version, size_bytes, mime_type, content_hash,
		       coalesce(created_by_user_id::text,''), created_at
		from file_versions where entry_id = $1::uuid order by version desc`, entryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.FileVersion, 0)
	for rows.Next() {
		var v domain.FileVersion
		if err := rows.Scan(&v.ID, &v.EntryID, &v.Version, &v.SizeBytes, &v.MimeType, &v.ContentHash, &v.CreatedByUserID, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Service) RestoreVersion(ctx context.Context, user domain.User, entryID, versionID string) error {
	if err := s.access.Require(ctx, user.ID, user.Role, entryID, access.ActionEdit); err != nil {
		return err
	}
	var vStorageKey, mime, hash string
	var vSize, ver int64
	err := s.db.QueryRow(ctx, `
		select storage_key, size_bytes, mime_type, content_hash, version
		from file_versions where id = $1::uuid and entry_id = $2::uuid`, versionID, entryID,
	).Scan(&vStorageKey, &vSize, &mime, &hash, &ver)
	if err != nil {
		return err
	}
	var cur domain.FileEntry
	err = s.db.QueryRow(ctx, `
		select id::text, user_id::text, coalesce(drive_id::text,''), parent_id::text, name, kind, storage_key,
		       size_bytes, mime_type, content_version, content_hash, client_modified_at, last_opened_at, deleted_at, created_at, updated_at
		from file_entries where id = $1::uuid`, entryID,
	).Scan(&cur.ID, &cur.UserID, &cur.DriveID, &cur.ParentID, &cur.Name, &cur.Kind, &cur.StorageKey,
		&cur.SizeBytes, &cur.MimeType, &cur.ContentVersion, &cur.ContentHash, &cur.ClientModifiedAt, &cur.LastOpenedAt, &cur.DeletedAt, &cur.CreatedAt, &cur.UpdatedAt)
	if err != nil {
		return err
	}
	_ = s.SnapshotVersion(ctx, cur, user.ID)
	_, err = s.db.Exec(ctx, `
		update file_entries
		set storage_key = $1, size_bytes = $2, mime_type = $3, content_hash = $4,
		    content_version = content_version + 1, updated_at = now()
		where id = $5::uuid`, vStorageKey, vSize, mime, hash, entryID)
	return err
}

// --- Activity / Notifications ---

func (s *Service) ListActivities(ctx context.Context, userID string, limit int) ([]domain.Activity, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(ctx, `
		select a.id::text, a.user_id::text, a.actor_id::text, coalesce(u.display_name,''), a.kind,
		       coalesce(a.entry_id::text,''), coalesce(a.drive_id::text,''), a.message, a.created_at
		from activities a left join users u on u.id = a.actor_id
		where a.user_id = $1::uuid
		order by a.created_at desc limit $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.Activity, 0)
	for rows.Next() {
		var a domain.Activity
		if err := rows.Scan(&a.ID, &a.UserID, &a.ActorID, &a.ActorName, &a.Kind, &a.EntryID, &a.DriveID, &a.Message, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Service) ListNotifications(ctx context.Context, userID string, unreadOnly bool) ([]domain.Notification, error) {
	q := `
		select id::text, user_id::text, kind, title, body, coalesce(entry_id::text,''), coalesce(drive_id::text,''), read_at, created_at
		from notifications where user_id = $1::uuid`
	if unreadOnly {
		q += ` and read_at is null`
	}
	q += ` order by created_at desc limit 100`
	rows, err := s.db.Query(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.Notification, 0)
	for rows.Next() {
		var n domain.Notification
		if err := rows.Scan(&n.ID, &n.UserID, &n.Kind, &n.Title, &n.Body, &n.EntryID, &n.DriveID, &n.ReadAt, &n.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Service) MarkNotificationsRead(ctx context.Context, userID string, ids []string) error {
	if len(ids) == 0 {
		_, err := s.db.Exec(ctx, `update notifications set read_at = now() where user_id = $1::uuid and read_at is null`, userID)
		return err
	}
	for _, id := range ids {
		_, _ = s.db.Exec(ctx, `update notifications set read_at = now() where id = $1::uuid and user_id = $2::uuid`, id, userID)
	}
	return nil
}

func normalizeACL(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "viewer", "commenter", "editor":
		return strings.ToLower(strings.TrimSpace(role))
	default:
		return ""
	}
}

type entryScanner interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}

func scanEntries(rows entryScanner) ([]domain.FileEntry, error) {
	items := make([]domain.FileEntry, 0)
	for rows.Next() {
		var e domain.FileEntry
		var parent *string
		var lastOpened *time.Time
		if err := rows.Scan(&e.ID, &e.UserID, &e.DriveID, &parent, &e.Name, &e.Kind, &e.StorageKey, &e.SizeBytes, &e.MimeType,
			&e.ContentVersion, &e.ContentHash, &e.ClientModifiedAt, &lastOpened, &e.DeletedAt, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		e.ParentID = parent
		e.LastOpenedAt = lastOpened
		items = append(items, e)
	}
	return items, rows.Err()
}
