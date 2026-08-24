package files

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"path/filepath"
	"strings"
	"time"

	"necipdrive/internal/access"
	"necipdrive/internal/changelog"
	"necipdrive/internal/config"
	"necipdrive/internal/domain"
	"necipdrive/internal/loadpace"
	"necipdrive/internal/storage"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	db      *pgxpool.Pool
	storage *storage.Local
	cfg     config.Config
	access  *access.Service
	pace    *loadpace.Controller
}

func NewService(db *pgxpool.Pool, fileStorage *storage.Local, cfg config.Config, accessSvc *access.Service, pace *loadpace.Controller) *Service {
	return &Service{db: db, storage: fileStorage, cfg: cfg, access: accessSvc, pace: pace}
}

func (s *Service) UploadPace() loadpace.Snapshot {
	if s.pace == nil {
		return loadpace.Snapshot{Mode: "normal", DelayMs: 350, AcceptUploads: true, MaxConcurrent: 3, RecommendedBatch: 8}
	}
	return s.pace.Snapshot()
}

func (s *Service) List(ctx context.Context, userID, userRole, parentID string) ([]domain.FileEntry, error) {
	if err := s.access.Require(ctx, userID, userRole, parentID, access.ActionView); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `
		select f.id::text, f.user_id::text, coalesce(f.drive_id::text,''), f.parent_id::text, f.name, f.kind, f.storage_key,
		       f.size_bytes, f.mime_type, f.content_version, f.content_hash, f.client_modified_at, f.last_opened_at,
		       f.deleted_at, f.created_at, f.updated_at,
		       exists(select 1 from file_stars s where s.user_id = $1::uuid and s.entry_id = f.id) as starred
		from file_entries f
		where f.deleted_at is null and f.parent_id = $2::uuid
		order by f.kind desc, lower(f.name) asc`,
		userID, parentID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]domain.FileEntry, 0)
	for rows.Next() {
		var item domain.FileEntry
		var parentIDValue *string
		if err := rows.Scan(&item.ID, &item.UserID, &item.DriveID, &parentIDValue, &item.Name, &item.Kind, &item.StorageKey,
			&item.SizeBytes, &item.MimeType, &item.ContentVersion, &item.ContentHash, &item.ClientModifiedAt, &item.LastOpenedAt,
			&item.DeletedAt, &item.CreatedAt, &item.UpdatedAt, &item.Starred); err != nil {
			return nil, err
		}
		item.ParentID = parentIDValue
		items = append(items, item)
	}
	return items, rows.Err()
}

// IsDescendantOrSelf reports whether entryID is shareRootID or a descendant under it.
func (s *Service) IsDescendantOrSelf(ctx context.Context, entryID, shareRootID string) (bool, error) {
	if entryID == "" || shareRootID == "" {
		return false, errors.New("invalid id")
	}
	if entryID == shareRootID {
		return true, nil
	}
	var ok bool
	err := s.db.QueryRow(ctx, `
		with recursive chain as (
			select id, parent_id from file_entries where id = $1::uuid and deleted_at is null
			union all
			select f.id, f.parent_id from file_entries f
			join chain c on f.id = c.parent_id
			where f.deleted_at is null
		)
		select exists(select 1 from chain where id = $2::uuid)`,
		entryID, shareRootID,
	).Scan(&ok)
	return ok, err
}

func (s *Service) ListPublicChildren(ctx context.Context, parentID string) ([]domain.FileEntry, error) {
	rows, err := s.db.Query(ctx, `
		select id::text, user_id::text, coalesce(drive_id::text,''), parent_id::text, name, kind, storage_key,
		       size_bytes, mime_type, content_version, content_hash, client_modified_at, last_opened_at,
		       deleted_at, created_at, updated_at
		from file_entries
		where deleted_at is null and parent_id = $1::uuid
		order by kind desc, lower(name)`, parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.FileEntry, 0)
	for rows.Next() {
		var item domain.FileEntry
		if err := rows.Scan(&item.ID, &item.UserID, &item.DriveID, &item.ParentID, &item.Name, &item.Kind, &item.StorageKey,
			&item.SizeBytes, &item.MimeType, &item.ContentVersion, &item.ContentHash, &item.ClientModifiedAt, &item.LastOpenedAt,
			&item.DeletedAt, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		item.StorageKey = ""
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Service) CreateFolder(ctx context.Context, userID, userRole, parentID, name, deviceID string) (*domain.FileEntry, error) {
	name = sanitizeName(name)
	if name == "" {
		return nil, errors.New("folder name is required")
	}
	if err := s.access.Require(ctx, userID, userRole, parentID, access.ActionEdit); err != nil {
		return nil, err
	}
	driveID, billUser, err := s.parentContext(ctx, parentID)
	if err != nil {
		return nil, err
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var entry domain.FileEntry
	err = tx.QueryRow(ctx, `
		insert into file_entries (user_id, drive_id, parent_id, name, kind, storage_key, size_bytes, mime_type)
		values ($1::uuid, nullif($2,'')::uuid, $3::uuid, $4, 'folder', $5, 0, 'inode/directory')
		returning id::text, user_id::text, coalesce(drive_id::text,''), parent_id::text, name, kind, storage_key, size_bytes, mime_type, content_version, content_hash, client_modified_at, last_opened_at, deleted_at, created_at, updated_at`,
		userID, driveID, parentID, name, "folder/"+uuid.NewString(),
	).Scan(&entry.ID, &entry.UserID, &entry.DriveID, &entry.ParentID, &entry.Name, &entry.Kind, &entry.StorageKey, &entry.SizeBytes, &entry.MimeType, &entry.ContentVersion, &entry.ContentHash, &entry.ClientModifiedAt, &entry.LastOpenedAt, &entry.DeletedAt, &entry.CreatedAt, &entry.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if err := changelog.Append(ctx, tx, billUser, entry.ID, "upsert", entry.Name, entry.ParentID, entry.Kind, entry.SizeBytes, entry.MimeType, entry.ContentVersion, entry.ContentHash, stringPtr(deviceID), entry.ClientModifiedAt); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &entry, nil
}

func (s *Service) Upload(ctx context.Context, user domain.User, parentID, deviceID string, file multipart.File, header *multipart.FileHeader) (*domain.FileEntry, error) {
	defer file.Close()
	if s.pace != nil {
		release, err := s.pace.Acquire(ctx)
		if err != nil {
			return nil, err
		}
		defer release()
	}
	if err := s.access.Require(ctx, user.ID, user.Role, parentID, access.ActionEdit); err != nil {
		return nil, err
	}
	driveID, billUser, err := s.parentContext(ctx, parentID)
	if err != nil {
		return nil, err
	}
	if header.Size > s.cfg.MaxUploadBytes {
		return nil, fmt.Errorf("file exceeds max upload limit")
	}
	var billQuota, billUsed int64
	if err := s.db.QueryRow(ctx, `select quota_bytes + coalesce(bonus_quota_bytes,0), used_bytes from users where id = $1::uuid`, billUser).Scan(&billQuota, &billUsed); err != nil {
		return nil, err
	}
	if billUsed+header.Size > billQuota {
		return nil, fmt.Errorf("quota exceeded")
	}
	fileName := sanitizeName(header.Filename)
	if fileName == "" {
		return nil, errors.New("file name is required")
	}
	mimeType := header.Header.Get("Content-Type")
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	storageKey := filepath.Join("user", billUser, time.Now().UTC().Format("2006/01/02"), uuid.NewString())
	limited := io.LimitReader(file, s.cfg.MaxUploadBytes+1)
	written, err := s.storage.Save(ctx, storageKey, limited)
	if err != nil {
		return nil, err
	}
	if written > s.cfg.MaxUploadBytes {
		_ = s.storage.Delete(storageKey)
		return nil, fmt.Errorf("quota or size limit exceeded during upload")
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		_ = s.storage.Delete(storageKey)
		return nil, err
	}
	defer tx.Rollback(ctx)

	var freshUsed, quota int64
	if err := tx.QueryRow(ctx, `select used_bytes, quota_bytes + coalesce(bonus_quota_bytes,0) from users where id = $1::uuid for update`, billUser).Scan(&freshUsed, &quota); err != nil {
		_ = s.storage.Delete(storageKey)
		return nil, err
	}
	if freshUsed+written > quota {
		_ = s.storage.Delete(storageKey)
		return nil, fmt.Errorf("quota exceeded")
	}

	var entry domain.FileEntry
	err = tx.QueryRow(ctx, `
		insert into file_entries (user_id, drive_id, parent_id, name, kind, storage_key, size_bytes, mime_type)
		values ($1::uuid, nullif($2,'')::uuid, $3::uuid, $4, 'file', $5, $6, $7)
		returning id::text, user_id::text, coalesce(drive_id::text,''), parent_id::text, name, kind, storage_key, size_bytes, mime_type, content_version, content_hash, client_modified_at, last_opened_at, deleted_at, created_at, updated_at`,
		user.ID, driveID, parentID, fileName, storageKey, written, mimeType,
	).Scan(&entry.ID, &entry.UserID, &entry.DriveID, &entry.ParentID, &entry.Name, &entry.Kind, &entry.StorageKey, &entry.SizeBytes, &entry.MimeType, &entry.ContentVersion, &entry.ContentHash, &entry.ClientModifiedAt, &entry.LastOpenedAt, &entry.DeletedAt, &entry.CreatedAt, &entry.UpdatedAt)
	if err != nil {
		_ = s.storage.Delete(storageKey)
		return nil, err
	}
	if _, err := tx.Exec(ctx, `update users set used_bytes = used_bytes + $1 where id = $2::uuid`, written, billUser); err != nil {
		_ = s.storage.Delete(storageKey)
		return nil, err
	}
	if err := changelog.Append(ctx, tx, billUser, entry.ID, "upsert", entry.Name, entry.ParentID, entry.Kind, entry.SizeBytes, entry.MimeType, entry.ContentVersion, entry.ContentHash, stringPtr(deviceID), entry.ClientModifiedAt); err != nil {
		_ = s.storage.Delete(storageKey)
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		_ = s.storage.Delete(storageKey)
		return nil, err
	}
	return &entry, nil
}

// UploadBytes stores raw bytes (WebDAV / internal writers).
func (s *Service) UploadBytes(ctx context.Context, userID, userRole, parentID, fileName, mimeType string, data []byte, deviceID string) error {
	if err := s.access.Require(ctx, userID, userRole, parentID, access.ActionEdit); err != nil {
		return err
	}
	driveID, billUser, err := s.parentContext(ctx, parentID)
	if err != nil {
		return err
	}
	size := int64(len(data))
	if size > s.cfg.MaxUploadBytes {
		return fmt.Errorf("file exceeds max upload limit")
	}
	fileName = sanitizeName(fileName)
	if fileName == "" {
		return errors.New("file name is required")
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	var billQuota, billUsed int64
	if err := s.db.QueryRow(ctx, `select quota_bytes + coalesce(bonus_quota_bytes,0), used_bytes from users where id = $1::uuid`, billUser).Scan(&billQuota, &billUsed); err != nil {
		return err
	}
	if billUsed+size > billQuota {
		return fmt.Errorf("quota exceeded")
	}
	return s.uploadBytesInner(ctx, userID, billUser, driveID, parentID, fileName, mimeType, data, deviceID)
}

func (s *Service) uploadBytesInner(ctx context.Context, userID, billUser, driveID, parentID, fileName, mimeType string, data []byte, deviceID string) error {
	storageKey := filepath.Join("user", billUser, time.Now().UTC().Format("2006/01/02"), uuid.NewString())
	written, err := s.storage.Save(ctx, storageKey, bytes.NewReader(data))
	if err != nil {
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		_ = s.storage.Delete(storageKey)
		return err
	}
	defer tx.Rollback(ctx)
	var freshUsed, quota int64
	if err := tx.QueryRow(ctx, `select used_bytes, quota_bytes + coalesce(bonus_quota_bytes,0) from users where id = $1::uuid for update`, billUser).Scan(&freshUsed, &quota); err != nil {
		_ = s.storage.Delete(storageKey)
		return err
	}
	if freshUsed+written > quota {
		_ = s.storage.Delete(storageKey)
		return fmt.Errorf("quota exceeded")
	}
	// replace existing same name if any
	var existingID, oldKey string
	var oldSize int64
	err = tx.QueryRow(ctx, `
		select id::text, storage_key, size_bytes from file_entries
		where parent_id = $1::uuid and lower(name) = lower($2) and deleted_at is null and kind = 'file'`,
		parentID, fileName,
	).Scan(&existingID, &oldKey, &oldSize)
	if err == nil {
		if _, err := tx.Exec(ctx, `
			update file_entries set storage_key = $1, size_bytes = $2, mime_type = $3, content_version = content_version + 1, updated_at = now()
			where id = $4::uuid`, storageKey, written, mimeType, existingID); err != nil {
			_ = s.storage.Delete(storageKey)
			return err
		}
		if _, err := tx.Exec(ctx, `update users set used_bytes = used_bytes - $1 + $2 where id = $3::uuid`, oldSize, written, billUser); err != nil {
			_ = s.storage.Delete(storageKey)
			return err
		}
		_ = s.storage.Delete(oldKey)
		var ver int64 = 1
		_ = tx.QueryRow(ctx, `select content_version from file_entries where id = $1::uuid`, existingID).Scan(&ver)
		if err := changelog.Append(ctx, tx, billUser, existingID, "upsert", fileName, &parentID, "file", written, mimeType, ver, "", stringPtr(deviceID), nil); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	var entryID string
	if err := tx.QueryRow(ctx, `
		insert into file_entries (user_id, drive_id, parent_id, name, kind, storage_key, size_bytes, mime_type)
		values ($1::uuid, nullif($2,'')::uuid, $3::uuid, $4, 'file', $5, $6, $7)
		returning id::text`,
		userID, driveID, parentID, fileName, storageKey, written, mimeType,
	).Scan(&entryID); err != nil {
		_ = s.storage.Delete(storageKey)
		return err
	}
	if _, err := tx.Exec(ctx, `update users set used_bytes = used_bytes + $1 where id = $2::uuid`, written, billUser); err != nil {
		_ = s.storage.Delete(storageKey)
		return err
	}
	_ = changelog.Append(ctx, tx, billUser, entryID, "upsert", fileName, &parentID, "file", written, mimeType, 1, "", stringPtr(deviceID), nil)
	return tx.Commit(ctx)
}

// FindChildFile returns an active file with the same name under parent (case-insensitive).
func (s *Service) FindChildFile(ctx context.Context, parentID, name string) (*domain.FileEntry, error) {
	name = sanitizeName(name)
	var entry domain.FileEntry
	err := s.db.QueryRow(ctx, `
		select id::text, user_id::text, coalesce(drive_id::text,''), parent_id::text, name, kind, storage_key,
		       size_bytes, mime_type, content_version, content_hash, client_modified_at, last_opened_at, deleted_at, created_at, updated_at
		from file_entries
		where parent_id = $1::uuid and lower(name) = lower($2) and deleted_at is null and kind = 'file'`,
		parentID, name,
	).Scan(&entry.ID, &entry.UserID, &entry.DriveID, &entry.ParentID, &entry.Name, &entry.Kind, &entry.StorageKey,
		&entry.SizeBytes, &entry.MimeType, &entry.ContentVersion, &entry.ContentHash, &entry.ClientModifiedAt, &entry.LastOpenedAt,
		&entry.DeletedAt, &entry.CreatedAt, &entry.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	entry.StorageKey = ""
	return &entry, nil
}

var ErrIsFolder = errors.New("entry is a folder")

func (s *Service) Download(ctx context.Context, userID, userRole, fileID string) (*domain.FileEntry, io.ReadCloser, error) {
	if err := s.access.Require(ctx, userID, userRole, fileID, access.ActionView); err != nil {
		return nil, nil, err
	}
	entry, err := s.entryByID(ctx, fileID)
	if err != nil {
		return nil, nil, err
	}
	if entry.Kind == "folder" || strings.HasPrefix(entry.StorageKey, "folder/") {
		return entry, nil, ErrIsFolder
	}
	_, _ = s.db.Exec(ctx, `update file_entries set last_opened_at = now() where id = $1::uuid`, fileID)
	reader, err := s.storage.Open(entry.StorageKey)
	if err != nil {
		return nil, nil, err
	}
	return entry, reader, nil
}

func (s *Service) Move(ctx context.Context, userID, userRole, fileID, parentID, deviceID string) error {
	if err := s.access.Require(ctx, userID, userRole, fileID, access.ActionEdit); err != nil {
		return err
	}
	if err := s.access.Require(ctx, userID, userRole, parentID, access.ActionEdit); err != nil {
		return err
	}
	var invalid bool
	err := s.db.QueryRow(ctx, `
		with recursive descendants as (
			select id from file_entries where id = $1::uuid and deleted_at is null
			union all
			select f.id from file_entries f join descendants d on f.parent_id = d.id
			where f.deleted_at is null
		)
		select exists(select 1 from descendants where id = $2::uuid)`,
		fileID, parentID,
	).Scan(&invalid)
	if err != nil {
		return err
	}
	if invalid {
		return errors.New("a folder cannot be moved into itself")
	}
	driveID, billUser, err := s.parentContext(ctx, parentID)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var entry domain.FileEntry
	err = tx.QueryRow(ctx, `
		update file_entries set parent_id = $1::uuid, drive_id = nullif($2,'')::uuid, content_version = content_version + 1, updated_at = now()
		where id = $3::uuid and deleted_at is null
		  and id not in (select root_entry_id from drives where root_entry_id is not null)
		returning id::text, user_id::text, coalesce(drive_id::text,''), parent_id::text, name, kind, storage_key, size_bytes, mime_type, content_version, content_hash, client_modified_at, last_opened_at, deleted_at, created_at, updated_at`,
		parentID, driveID, fileID,
	).Scan(&entry.ID, &entry.UserID, &entry.DriveID, &entry.ParentID, &entry.Name, &entry.Kind, &entry.StorageKey, &entry.SizeBytes, &entry.MimeType, &entry.ContentVersion, &entry.ContentHash, &entry.ClientModifiedAt, &entry.LastOpenedAt, &entry.DeletedAt, &entry.CreatedAt, &entry.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.New("file not found or root folder cannot be moved")
	}
	if err != nil {
		return err
	}
	if err := changelog.Append(ctx, tx, billUser, entry.ID, "move", entry.Name, entry.ParentID, entry.Kind, entry.SizeBytes, entry.MimeType, entry.ContentVersion, entry.ContentHash, stringPtr(deviceID), entry.ClientModifiedAt); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) Rename(ctx context.Context, userID, userRole, fileID, name, deviceID string) error {
	name = sanitizeName(name)
	if name == "" {
		return errors.New("name is required")
	}
	if err := s.access.Require(ctx, userID, userRole, fileID, access.ActionEdit); err != nil {
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var entry domain.FileEntry
	err = tx.QueryRow(ctx, `
		update file_entries set name = $1, content_version = content_version + 1, updated_at = now()
		where id = $2::uuid and deleted_at is null
		  and id not in (select root_entry_id from drives where root_entry_id is not null)
		returning id::text, user_id::text, coalesce(drive_id::text,''), parent_id::text, name, kind, storage_key, size_bytes, mime_type, content_version, content_hash, client_modified_at, last_opened_at, deleted_at, created_at, updated_at`,
		name, fileID,
	).Scan(&entry.ID, &entry.UserID, &entry.DriveID, &entry.ParentID, &entry.Name, &entry.Kind, &entry.StorageKey, &entry.SizeBytes, &entry.MimeType, &entry.ContentVersion, &entry.ContentHash, &entry.ClientModifiedAt, &entry.LastOpenedAt, &entry.DeletedAt, &entry.CreatedAt, &entry.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.New("file not found or root folder cannot be renamed")
	}
	if err != nil {
		return err
	}
	if err := changelog.Append(ctx, tx, entry.UserID, entry.ID, "rename", entry.Name, entry.ParentID, entry.Kind, entry.SizeBytes, entry.MimeType, entry.ContentVersion, entry.ContentHash, stringPtr(deviceID), entry.ClientModifiedAt); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) Delete(ctx context.Context, userID, userRole, fileID, deviceID string) error {
	if err := s.access.Require(ctx, userID, userRole, fileID, access.ActionManage); err != nil {
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, `
		with recursive targets as (
			select id, parent_id, name, kind, storage_key, size_bytes, mime_type, content_version, content_hash, client_modified_at, user_id
			from file_entries
			where id = $1::uuid and deleted_at is null
			  and id not in (select root_entry_id from drives where root_entry_id is not null)
			union all
			select f.id, f.parent_id, f.name, f.kind, f.storage_key, f.size_bytes, f.mime_type, f.content_version, f.content_hash, f.client_modified_at, f.user_id
			from file_entries f join targets t on f.parent_id = t.id
			where f.deleted_at is null
		)
		select id::text, parent_id::text, name, kind, storage_key, size_bytes, mime_type, content_version, content_hash, client_modified_at, user_id::text from targets`,
		fileID,
	)
	if err != nil {
		return err
	}
	type deletion struct {
		id, name, kind, key, mime, hash, owner string
		parentID                             *string
		clientModifiedAt                     *time.Time
		size, version                        int64
	}
	var targets []deletion
	for rows.Next() {
		var target deletion
		if err := rows.Scan(&target.id, &target.parentID, &target.name, &target.kind, &target.key, &target.size, &target.mime, &target.version, &target.hash, &target.clientModifiedAt, &target.owner); err != nil {
			rows.Close()
			return err
		}
		targets = append(targets, target)
	}
	rows.Close()
	if len(targets) == 0 {
		return errors.New("file not found or root folder cannot be deleted")
	}
	if _, err := tx.Exec(ctx, `
		with recursive targets as (
			select id from file_entries where id = $1::uuid and deleted_at is null
			union all
			select f.id from file_entries f join targets t on f.parent_id = t.id
			where f.deleted_at is null
		)
		update file_entries set deleted_at = now(), content_version = content_version + 1, updated_at = now()
		where id in (select id from targets)`,
		fileID,
	); err != nil {
		return err
	}
	for _, target := range targets {
		if err := changelog.Append(ctx, tx, target.owner, target.id, "trash", target.name, target.parentID, target.kind, target.size, target.mime, target.version+1, target.hash, stringPtr(deviceID), nil); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Service) EntryForShare(ctx context.Context, fileID string) (*domain.FileEntry, error) {
	return s.entryByID(ctx, fileID)
}

func (s *Service) OpenStorage(key string) (io.ReadCloser, error) {
	return s.storage.Open(key)
}

func (s *Service) ListTrash(ctx context.Context, userID string) ([]domain.FileEntry, error) {
	rows, err := s.db.Query(ctx, `
		select id::text, user_id::text, coalesce(drive_id::text,''), parent_id::text, name, kind, storage_key, size_bytes, mime_type, content_version, content_hash, client_modified_at, last_opened_at, deleted_at, created_at, updated_at
		from file_entries where user_id = $1::uuid and deleted_at is not null
		order by deleted_at desc`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.FileEntry, 0)
	for rows.Next() {
		var entry domain.FileEntry
		if err := rows.Scan(&entry.ID, &entry.UserID, &entry.DriveID, &entry.ParentID, &entry.Name, &entry.Kind, &entry.StorageKey, &entry.SizeBytes, &entry.MimeType, &entry.ContentVersion, &entry.ContentHash, &entry.ClientModifiedAt, &entry.LastOpenedAt, &entry.DeletedAt, &entry.CreatedAt, &entry.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, entry)
	}
	return items, rows.Err()
}

func (s *Service) Restore(ctx context.Context, userID, userRole, fileID, deviceID string) error {
	if err := s.access.Require(ctx, userID, userRole, fileID, access.ActionManage); err != nil {
		// trashed entries still need ownership check fallback
		var owner string
		_ = s.db.QueryRow(ctx, `select user_id::text from file_entries where id = $1::uuid`, fileID).Scan(&owner)
		if owner != userID && userRole != "admin" {
			return err
		}
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var entry domain.FileEntry
	err = tx.QueryRow(ctx, `
		update file_entries set deleted_at = null, content_version = content_version + 1, updated_at = now()
		where id = $1::uuid and deleted_at is not null
		  and (parent_id is null or exists (select 1 from file_entries p where p.id = file_entries.parent_id and p.deleted_at is null))
		returning id::text, user_id::text, coalesce(drive_id::text,''), parent_id::text, name, kind, storage_key, size_bytes, mime_type, content_version, content_hash, client_modified_at, last_opened_at, deleted_at, created_at, updated_at`,
		fileID,
	).Scan(&entry.ID, &entry.UserID, &entry.DriveID, &entry.ParentID, &entry.Name, &entry.Kind, &entry.StorageKey, &entry.SizeBytes, &entry.MimeType, &entry.ContentVersion, &entry.ContentHash, &entry.ClientModifiedAt, &entry.LastOpenedAt, &entry.DeletedAt, &entry.CreatedAt, &entry.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.New("file not found or parent is still trashed")
	}
	if err != nil {
		return err
	}
	if entry.UserID != userID && userRole != "admin" {
		return errors.New("forbidden")
	}
	if err := changelog.Append(ctx, tx, entry.UserID, entry.ID, "restore", entry.Name, entry.ParentID, entry.Kind, entry.SizeBytes, entry.MimeType, entry.ContentVersion, entry.ContentHash, stringPtr(deviceID), entry.ClientModifiedAt); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) Purge(ctx context.Context, userID, fileID, deviceID string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var entry domain.FileEntry
	err = tx.QueryRow(ctx, `
		delete from file_entries where id = $1::uuid and user_id = $2::uuid and deleted_at is not null
		returning id::text, user_id::text, coalesce(drive_id::text,''), parent_id::text, name, kind, storage_key, size_bytes, mime_type, content_version, content_hash, client_modified_at, last_opened_at, deleted_at, created_at, updated_at`,
		fileID, userID,
	).Scan(&entry.ID, &entry.UserID, &entry.DriveID, &entry.ParentID, &entry.Name, &entry.Kind, &entry.StorageKey, &entry.SizeBytes, &entry.MimeType, &entry.ContentVersion, &entry.ContentHash, &entry.ClientModifiedAt, &entry.LastOpenedAt, &entry.DeletedAt, &entry.CreatedAt, &entry.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.New("trashed file not found")
	}
	if err != nil {
		return err
	}
	if entry.Kind == "file" {
		if _, err := tx.Exec(ctx, `update users set used_bytes = greatest(0, used_bytes - $1) where id = $2::uuid`, entry.SizeBytes, userID); err != nil {
			return err
		}
	}
	if err := changelog.Append(ctx, tx, userID, entry.ID, "purge", entry.Name, entry.ParentID, entry.Kind, entry.SizeBytes, entry.MimeType, entry.ContentVersion, entry.ContentHash, stringPtr(deviceID), entry.ClientModifiedAt); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if entry.Kind == "file" {
		_ = s.storage.Delete(entry.StorageKey)
	}
	return nil
}

func (s *Service) CleanupTrash(ctx context.Context, retention time.Duration) error {
	rows, err := s.db.Query(ctx, `select id::text, user_id::text from file_entries where deleted_at <= now() - $1::interval`, retention.String())
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, userID string
		if err := rows.Scan(&id, &userID); err != nil {
			return err
		}
		if err := s.Purge(ctx, userID, id, ""); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (s *Service) entryByID(ctx context.Context, fileID string) (*domain.FileEntry, error) {
	var entry domain.FileEntry
	err := s.db.QueryRow(ctx, `
		select id::text, user_id::text, coalesce(drive_id::text,''), parent_id::text, name, kind, storage_key, size_bytes, mime_type, content_version, content_hash, client_modified_at, last_opened_at, deleted_at, created_at, updated_at
		from file_entries where id = $1::uuid and deleted_at is null`,
		fileID,
	).Scan(&entry.ID, &entry.UserID, &entry.DriveID, &entry.ParentID, &entry.Name, &entry.Kind, &entry.StorageKey, &entry.SizeBytes, &entry.MimeType, &entry.ContentVersion, &entry.ContentHash, &entry.ClientModifiedAt, &entry.LastOpenedAt, &entry.DeletedAt, &entry.CreatedAt, &entry.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errors.New("file not found")
		}
		return nil, err
	}
	return &entry, nil
}

func (s *Service) parentContext(ctx context.Context, parentID string) (driveID, billUserID string, err error) {
	err = s.db.QueryRow(ctx, `
		select coalesce(f.drive_id::text,''), coalesce(d.owner_user_id::text, f.user_id::text)
		from file_entries f
		left join drives d on d.id = f.drive_id
		where f.id = $1::uuid and f.kind = 'folder' and f.deleted_at is null`, parentID,
	).Scan(&driveID, &billUserID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", errors.New("parent folder not found")
	}
	return driveID, billUserID, err
}

func sanitizeName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\\", "_")
	name = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 || r == '"' {
			return -1
		}
		return r
	}, name)
	if len(name) > 255 {
		name = name[:255]
	}
	return strings.TrimSpace(name)
}

func stringPtr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
