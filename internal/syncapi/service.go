package syncapi

import (
	"context"
	"errors"

	"necipdrive/internal/domain"
	"necipdrive/internal/files"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	db    *pgxpool.Pool
	files *files.Service
}

func NewService(db *pgxpool.Pool, fileService *files.Service) *Service {
	return &Service{db: db, files: fileService}
}

func (s *Service) Snapshot(ctx context.Context, userID string) (int64, []domain.FileEntry, error) {
	rows, err := s.db.Query(ctx, `
		select f.id::text, f.user_id::text, coalesce(f.drive_id::text,''), f.parent_id::text, f.name, f.kind, f.storage_key,
		       f.size_bytes, f.mime_type, f.content_version, f.content_hash, f.client_modified_at, f.last_opened_at,
		       f.deleted_at, f.created_at, f.updated_at
		from file_entries f
		where f.deleted_at is null
		  and (
			f.user_id = $1::uuid
			or exists (select 1 from drive_members dm where dm.drive_id = f.drive_id and dm.user_id = $1::uuid)
		  )
		order by f.created_at asc`, userID)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	entries := make([]domain.FileEntry, 0)
	for rows.Next() {
		var entry domain.FileEntry
		if err := rows.Scan(&entry.ID, &entry.UserID, &entry.DriveID, &entry.ParentID, &entry.Name, &entry.Kind, &entry.StorageKey,
			&entry.SizeBytes, &entry.MimeType, &entry.ContentVersion, &entry.ContentHash, &entry.ClientModifiedAt, &entry.LastOpenedAt,
			&entry.DeletedAt, &entry.CreatedAt, &entry.UpdatedAt); err != nil {
			return 0, nil, err
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return 0, nil, err
	}
	var cursor int64
	if err := s.db.QueryRow(ctx, `
		select coalesce(max(id), 0) from file_changes
		where user_id = $1::uuid
		   or drive_id in (select drive_id from drive_members where user_id = $1::uuid)`, userID).Scan(&cursor); err != nil {
		return 0, nil, err
	}
	return cursor, entries, nil
}

func (s *Service) Changes(ctx context.Context, userID string, cursor int64, limit int) ([]domain.FileChange, int64, error) {
	limit = normalizeLimit(limit)
	rows, err := s.db.Query(ctx, `
		select id, entry_id::text, op, name, parent_id::text, kind, size_bytes, mime_type, content_version, content_hash, device_id::text, client_modified_at, created_at
		from file_changes
		where id > $2 and (
			user_id = $1::uuid
			or drive_id in (select drive_id from drive_members where user_id = $1::uuid)
		)
		order by id asc limit $3`, userID, cursor, limit)
	if err != nil {
		return nil, cursor, err
	}
	defer rows.Close()
	changes := make([]domain.FileChange, 0)
	next := cursor
	for rows.Next() {
		var change domain.FileChange
		if err := rows.Scan(&change.ID, &change.EntryID, &change.Op, &change.Name, &change.ParentID, &change.Kind, &change.SizeBytes, &change.MimeType, &change.ContentVersion, &change.ContentHash, &change.DeviceID, &change.ClientModifiedAt, &change.CreatedAt); err != nil {
			return nil, cursor, err
		}
		next = change.ID
		changes = append(changes, change)
	}
	return changes, next, rows.Err()
}

func normalizeLimit(limit int) int {
	if limit <= 0 {
		return 500
	}
	if limit > 2000 {
		return 2000
	}
	return limit
}

func (s *Service) checkVersion(ctx context.Context, fileID string, expected *int64) error {
	if expected == nil {
		return nil
	}
	var version int64
	err := s.db.QueryRow(ctx, `select content_version from file_entries where id = $1::uuid and deleted_at is null`, fileID).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.New("file not found")
	}
	if err != nil {
		return err
	}
	if version != *expected {
		return errors.New("version conflict")
	}
	return nil
}

func (s *Service) CreateFolder(ctx context.Context, userID, userRole, parentID, name, deviceID string) (*domain.FileEntry, error) {
	return s.files.CreateFolder(ctx, userID, userRole, parentID, name, deviceID)
}

func (s *Service) Rename(ctx context.Context, userID, userRole, fileID, name, deviceID string, expected *int64) error {
	if err := s.checkVersion(ctx, fileID, expected); err != nil {
		return err
	}
	return s.files.Rename(ctx, userID, userRole, fileID, name, deviceID)
}

func (s *Service) Move(ctx context.Context, userID, userRole, fileID, parentID, deviceID string, expected *int64) error {
	if err := s.checkVersion(ctx, fileID, expected); err != nil {
		return err
	}
	return s.files.Move(ctx, userID, userRole, fileID, parentID, deviceID)
}

func (s *Service) Trash(ctx context.Context, userID, userRole, fileID, deviceID string) error {
	return s.files.Delete(ctx, userID, userRole, fileID, deviceID)
}

func (s *Service) Restore(ctx context.Context, userID, userRole, fileID, deviceID string) error {
	return s.files.Restore(ctx, userID, userRole, fileID, deviceID)
}

func (s *Service) Purge(ctx context.Context, userID, fileID, deviceID string) error {
	return s.files.Purge(ctx, userID, fileID, deviceID)
}

func (s *Service) ListTrash(ctx context.Context, userID string) ([]domain.FileEntry, error) {
	return s.files.ListTrash(ctx, userID)
}
