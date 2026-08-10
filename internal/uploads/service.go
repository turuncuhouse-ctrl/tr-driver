package uploads

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"necipdrive/internal/changelog"
	"necipdrive/internal/config"
	"necipdrive/internal/domain"
	"necipdrive/internal/storage"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ManifestFile struct {
	RelativePath   string `json:"relativePath"`
	FileName       string `json:"fileName"`
	MimeType       string `json:"mimeType"`
	ExpectedSize   int64  `json:"expectedSize"`
	LastModifiedMs int64  `json:"lastModifiedMs"`
	TargetEntryID  *string `json:"targetEntryId,omitempty"`
	ExpectedVersion *int64 `json:"expectedVersion,omitempty"`
	ContentHash    string `json:"contentHash"`
	ClientModifiedAt *time.Time `json:"clientModifiedAt,omitempty"`
	DeviceID       *string `json:"deviceId,omitempty"`
}

type Service struct {
	db      *pgxpool.Pool
	storage *storage.Local
	cfg     config.Config

	globalSem chan struct{}
	userLocks sync.Map
}

func NewService(db *pgxpool.Pool, fileStorage *storage.Local, cfg config.Config) *Service {
	slots := cfg.MaxConcurrentChunks
	if slots < 1 {
		slots = 2
	}
	return &Service{
		db:        db,
		storage:   fileStorage,
		cfg:       cfg,
		globalSem: make(chan struct{}, slots),
	}
}

func (s *Service) MaxBatchBytes(ctx context.Context) int64 {
	var raw string
	if err := s.db.QueryRow(ctx, `select value from app_settings where key = 'max_upload_batch_bytes'`).Scan(&raw); err != nil {
		return s.cfg.MaxUploadBatchBytes
	}
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || parsed <= 0 {
		return s.cfg.MaxUploadBatchBytes
	}
	return parsed
}

func (s *Service) CreateBatch(ctx context.Context, user domain.User, parentID string, files []ManifestFile) (*domain.UploadBatch, error) {
	if len(files) == 0 {
		return nil, errors.New("at least one file is required")
	}
	if len(files) > s.cfg.MaxBatchFiles {
		return nil, fmt.Errorf("batch cannot exceed %d files", s.cfg.MaxBatchFiles)
	}
	if parentID == "" {
		parentID = user.StorageRootID
	}

	var total int64
	normalized := make([]ManifestFile, 0, len(files))
	seen := map[string]struct{}{}
	for _, file := range files {
		item, err := normalizeManifest(file)
		if err != nil {
			return nil, err
		}
		if item.ExpectedSize > s.cfg.MaxUploadBytes {
			return nil, fmt.Errorf("%s exceeds max file size", item.RelativePath)
		}
		key := strings.ToLower(item.RelativePath)
		if _, ok := seen[key]; ok {
			return nil, fmt.Errorf("duplicate path: %s", item.RelativePath)
		}
		seen[key] = struct{}{}
		total += item.ExpectedSize
		normalized = append(normalized, item)
	}

	maxBatch := s.MaxBatchBytes(ctx)
	if total > maxBatch {
		return nil, fmt.Errorf("batch exceeds maximum upload size of %d bytes", maxBatch)
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if err := validateOwnedFolder(ctx, tx, user.ID, parentID); err != nil {
		return nil, err
	}

	var used, reserved, quota int64
	if err := tx.QueryRow(ctx, `
		select used_bytes, reserved_bytes, quota_bytes
		from users where id = $1::uuid for update`,
		user.ID,
	).Scan(&used, &reserved, &quota); err != nil {
		return nil, err
	}
	if used+reserved+total > quota {
		return nil, errors.New("quota exceeded")
	}

	folderCache := map[string]string{"": parentID}
	for _, file := range normalized {
		dir := path.Dir(file.RelativePath)
		if dir == "." {
			dir = ""
		}
		if _, err := ensureFolderPath(ctx, tx, user.ID, parentID, dir, folderCache); err != nil {
			return nil, err
		}
	}

	expiresAt := time.Now().Add(s.cfg.UploadSessionTTL)
	var batch domain.UploadBatch
	err = tx.QueryRow(ctx, `
		insert into upload_batches (user_id, parent_id, total_bytes, reserved_bytes, file_count, status, expires_at)
		values ($1::uuid, $2::uuid, $3, $3, $4, 'open', $5)
		returning id::text, parent_id::text, total_bytes, reserved_bytes, file_count, status, expires_at, created_at`,
		user.ID, parentID, total, len(normalized), expiresAt,
	).Scan(&batch.ID, &batch.ParentID, &batch.TotalBytes, &batch.ReservedBytes, &batch.FileCount, &batch.Status, &batch.ExpiresAt, &batch.CreatedAt)
	if err != nil {
		return nil, err
	}

	sessions := make([]domain.UploadSession, 0, len(normalized))
	for _, file := range normalized {
		dir := path.Dir(file.RelativePath)
		if dir == "." {
			dir = ""
		}
		folderID := folderCache[dir]
		tempKey := filepath.ToSlash(filepath.Join("tmp", "uploads", user.ID, batch.ID, uuid.NewString()+".part"))
		if err := s.storage.EnsureEmpty(tempKey); err != nil {
			return nil, err
		}
		var session domain.UploadSession
		err = tx.QueryRow(ctx, `
			insert into upload_sessions (
				batch_id, user_id, parent_id, relative_path, file_name, mime_type,
				expected_size, received_bytes, last_modified_ms, temp_key, target_entry_id, expected_version,
				content_hash, client_modified_at, device_id, status
			) values (
				$1::uuid, $2::uuid, $3::uuid, $4, $5, $6,
				$7, 0, $8, $9, nullif($10, '')::uuid, $11, $12, $13, nullif($14, '')::uuid, 'open'
			)
			returning id::text, batch_id::text, parent_id::text, relative_path, file_name, mime_type,
			          expected_size, received_bytes, last_modified_ms, target_entry_id::text, expected_version,
			          content_hash, client_modified_at, device_id::text, status, created_at, updated_at`,
			batch.ID, user.ID, folderID, file.RelativePath, file.FileName, file.MimeType,
			file.ExpectedSize, file.LastModifiedMs, tempKey, file.TargetEntryID, file.ExpectedVersion, file.ContentHash, file.ClientModifiedAt, file.DeviceID,
		).Scan(
			&session.ID, &session.BatchID, &session.ParentID, &session.RelativePath, &session.FileName, &session.MimeType,
			&session.ExpectedSize, &session.ReceivedBytes, &session.LastModifiedMs, &session.TargetEntryID, &session.ExpectedVersion,
			&session.ContentHash, &session.ClientModifiedAt, &session.DeviceID, &session.Status, &session.CreatedAt, &session.UpdatedAt,
		)
		if err != nil {
			_ = s.storage.Delete(tempKey)
			return nil, err
		}
		sessions = append(sessions, session)
	}

	if _, err := tx.Exec(ctx, `update users set reserved_bytes = reserved_bytes + $1 where id = $2::uuid`, total, user.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	batch.Files = sessions
	return &batch, nil
}

func (s *Service) ListOpenBatches(ctx context.Context, userID string) ([]domain.UploadBatch, error) {
	rows, err := s.db.Query(ctx, `
		select id::text, parent_id::text, total_bytes, reserved_bytes, file_count, status, expires_at, created_at
		from upload_batches
		where user_id = $1::uuid and status = 'open' and expires_at > now()
		order by created_at desc`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	batches := make([]domain.UploadBatch, 0)
	for rows.Next() {
		var batch domain.UploadBatch
		if err := rows.Scan(&batch.ID, &batch.ParentID, &batch.TotalBytes, &batch.ReservedBytes, &batch.FileCount, &batch.Status, &batch.ExpiresAt, &batch.CreatedAt); err != nil {
			return nil, err
		}
		files, err := s.sessionsForBatch(ctx, userID, batch.ID)
		if err != nil {
			return nil, err
		}
		batch.Files = files
		batches = append(batches, batch)
	}
	return batches, rows.Err()
}

func (s *Service) AppendChunk(ctx context.Context, userID, sessionID string, offset int64, body io.Reader, contentLength int64) (int64, error) {
	if offset < 0 {
		return 0, errors.New("invalid offset")
	}
	if contentLength < 0 || contentLength > s.cfg.UploadChunkBytes {
		return 0, fmt.Errorf("chunk must be between 1 and %d bytes", s.cfg.UploadChunkBytes)
	}

	unlock := s.lockUser(userID)
	defer unlock()

	select {
	case s.globalSem <- struct{}{}:
		defer func() { <-s.globalSem }()
	case <-ctx.Done():
		return 0, ctx.Err()
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	var session domain.UploadSession
	var tempKey string
	var expiresAt time.Time
	err = tx.QueryRow(ctx, `
		select us.id::text, us.batch_id::text, us.parent_id::text, us.relative_path, us.file_name, us.mime_type,
		       us.expected_size, us.received_bytes, us.last_modified_ms, us.temp_key, us.target_entry_id::text,
		       us.expected_version, us.content_hash, us.client_modified_at, us.device_id::text, us.status,
		       us.created_at, us.updated_at, ub.expires_at
		from upload_sessions us
		join upload_batches ub on ub.id = us.batch_id
		where us.id = $1::uuid and us.user_id = $2::uuid
		for update of us`,
		sessionID, userID,
	).Scan(
		&session.ID, &session.BatchID, &session.ParentID, &session.RelativePath, &session.FileName, &session.MimeType,
		&session.ExpectedSize, &session.ReceivedBytes, &session.LastModifiedMs, &tempKey, &session.TargetEntryID,
		&session.ExpectedVersion, &session.ContentHash, &session.ClientModifiedAt, &session.DeviceID, &session.Status,
		&session.CreatedAt, &session.UpdatedAt, &expiresAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, errors.New("upload session not found")
		}
		return 0, err
	}
	if session.Status != "open" || time.Now().After(expiresAt) {
		return 0, errors.New("upload session is not open")
	}

	diskSize, err := s.storage.Size(tempKey)
	if err != nil {
		return 0, err
	}
	if diskSize != session.ReceivedBytes {
		if err := s.storage.Truncate(tempKey, session.ReceivedBytes); err != nil {
			return 0, err
		}
		diskSize = session.ReceivedBytes
	}
	if offset != diskSize {
		return diskSize, fmt.Errorf("unexpected offset: expected %d", diskSize)
	}
	if offset+contentLength > session.ExpectedSize {
		return diskSize, errors.New("chunk exceeds expected file size")
	}

	limited := io.LimitReader(body, contentLength+1)
	written, err := s.storage.AppendAt(ctx, tempKey, offset, limited)
	if err != nil {
		_ = s.storage.Truncate(tempKey, session.ReceivedBytes)
		return session.ReceivedBytes, err
	}
	if written != contentLength {
		_ = s.storage.Truncate(tempKey, session.ReceivedBytes)
		return session.ReceivedBytes, errors.New("incomplete chunk")
	}

	newOffset := offset + written
	if _, err := tx.Exec(ctx, `
		update upload_sessions
		set received_bytes = $1, updated_at = now()
		where id = $2::uuid`,
		newOffset, sessionID,
	); err != nil {
		_ = s.storage.Truncate(tempKey, session.ReceivedBytes)
		return session.ReceivedBytes, err
	}
	if err := tx.Commit(ctx); err != nil {
		_ = s.storage.Truncate(tempKey, session.ReceivedBytes)
		return session.ReceivedBytes, err
	}
	return newOffset, nil
}

func (s *Service) CompleteFile(ctx context.Context, userID, sessionID string) (*domain.FileEntry, error) {
	unlock := s.lockUser(userID)
	defer unlock()

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var session domain.UploadSession
	var tempKey string
	err = tx.QueryRow(ctx, `
		select us.id::text, us.batch_id::text, us.parent_id::text, us.relative_path, us.file_name, us.mime_type,
		       us.expected_size, us.received_bytes, us.last_modified_ms, us.temp_key, us.target_entry_id::text,
		       us.expected_version, us.content_hash, us.client_modified_at, us.device_id::text, us.status,
		       us.created_at, us.updated_at
		from upload_sessions us
		join upload_batches ub on ub.id = us.batch_id
		where us.id = $1::uuid and us.user_id = $2::uuid and us.status = 'open' and ub.status = 'open'
		for update of us`,
		sessionID, userID,
	).Scan(
		&session.ID, &session.BatchID, &session.ParentID, &session.RelativePath, &session.FileName, &session.MimeType,
		&session.ExpectedSize, &session.ReceivedBytes, &session.LastModifiedMs, &tempKey, &session.TargetEntryID,
		&session.ExpectedVersion, &session.ContentHash, &session.ClientModifiedAt, &session.DeviceID, &session.Status,
		&session.CreatedAt, &session.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errors.New("upload session not found")
		}
		return nil, err
	}
	if session.ReceivedBytes != session.ExpectedSize {
		return nil, fmt.Errorf("file incomplete: %d/%d", session.ReceivedBytes, session.ExpectedSize)
	}
	diskSize, err := s.storage.Size(tempKey)
	if err != nil {
		return nil, err
	}
	if diskSize != session.ExpectedSize {
		return nil, errors.New("temporary file size mismatch")
	}

	parentID := ""
	if session.ParentID != nil {
		parentID = *session.ParentID
	}
	var oldKey string
	var oldSize int64
	replacing := session.TargetEntryID != nil
	if replacing {
		var version int64
		var oldHash, oldMime string
		err := tx.QueryRow(ctx, `
			select storage_key, size_bytes, content_version, content_hash, mime_type from file_entries
			where id = $1::uuid and user_id = $2::uuid and deleted_at is null for update`,
			*session.TargetEntryID, userID,
		).Scan(&oldKey, &oldSize, &version, &oldHash, &oldMime)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errors.New("target file not found")
		}
		if err != nil {
			return nil, err
		}
		if session.ExpectedVersion == nil || version != *session.ExpectedVersion {
			return nil, errors.New("version conflict")
		}
		if _, err := tx.Exec(ctx, `
			insert into file_versions (entry_id, version, storage_key, size_bytes, mime_type, content_hash, created_by_user_id)
			values ($1::uuid, $2, $3, $4, $5, $6, $7::uuid)
			on conflict (entry_id, version) do nothing`,
			*session.TargetEntryID, version, oldKey, oldSize, oldMime, oldHash, userID); err != nil {
			return nil, err
		}
	}
	finalKey := filepath.ToSlash(filepath.Join("user", userID, time.Now().UTC().Format("2006/01/02"), uuid.NewString()))
	if err := s.storage.Finalize(tempKey, finalKey); err != nil {
		return nil, err
	}

	var entry domain.FileEntry
	if replacing {
		err = tx.QueryRow(ctx, `
			update file_entries
			set storage_key = $1, size_bytes = $2, mime_type = $3, content_hash = $4,
			    client_modified_at = $5, content_version = content_version + 1, updated_at = now()
			where id = $6::uuid
			returning id::text, user_id::text, parent_id::text, name, kind, storage_key, size_bytes, mime_type, content_version, content_hash, client_modified_at, deleted_at, created_at, updated_at`,
			finalKey, session.ExpectedSize, session.MimeType, session.ContentHash, session.ClientModifiedAt, *session.TargetEntryID,
		).Scan(&entry.ID, &entry.UserID, &entry.ParentID, &entry.Name, &entry.Kind, &entry.StorageKey, &entry.SizeBytes, &entry.MimeType, &entry.ContentVersion, &entry.ContentHash, &entry.ClientModifiedAt, &entry.DeletedAt, &entry.CreatedAt, &entry.UpdatedAt)
	} else {
		var driveID string
		_ = tx.QueryRow(ctx, `select coalesce(drive_id::text,'') from file_entries where id = nullif($1,'')::uuid`, parentID).Scan(&driveID)
		err = tx.QueryRow(ctx, `
			insert into file_entries (user_id, drive_id, parent_id, name, kind, storage_key, size_bytes, mime_type, content_hash, client_modified_at)
			values ($1::uuid, nullif($2,'')::uuid, nullif($3, '')::uuid, $4, 'file', $5, $6, $7, $8, $9)
			returning id::text, user_id::text, parent_id::text, name, kind, storage_key, size_bytes, mime_type, content_version, content_hash, client_modified_at, deleted_at, created_at, updated_at`,
			userID, driveID, parentID, session.FileName, finalKey, session.ExpectedSize, session.MimeType, session.ContentHash, session.ClientModifiedAt,
		).Scan(&entry.ID, &entry.UserID, &entry.ParentID, &entry.Name, &entry.Kind, &entry.StorageKey, &entry.SizeBytes, &entry.MimeType, &entry.ContentVersion, &entry.ContentHash, &entry.ClientModifiedAt, &entry.DeletedAt, &entry.CreatedAt, &entry.UpdatedAt)
	}
	if err != nil {
		_ = s.storage.Delete(finalKey)
		return nil, fmt.Errorf("file name conflict or create failed: %w", err)
	}
	if _, err := tx.Exec(ctx, `update upload_sessions set status = 'complete', updated_at = now() where id = $1::uuid`, sessionID); err != nil {
		_ = s.storage.Delete(finalKey)
		return nil, err
	}
	if _, err := tx.Exec(ctx, `update users set used_bytes = greatest(0, used_bytes + $1 - $2), reserved_bytes = greatest(0, reserved_bytes - $1) where id = $3::uuid`, session.ExpectedSize, oldSize, userID); err != nil {
		_ = s.storage.Delete(finalKey)
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		update upload_batches
		set reserved_bytes = greatest(0, reserved_bytes - $1), updated_at = now(),
		    status = case when not exists (
		        select 1 from upload_sessions where batch_id = $2::uuid and status = 'open'
		    ) then 'done' else status end
		where id = $2::uuid`,
		session.ExpectedSize, session.BatchID,
	); err != nil {
		_ = s.storage.Delete(finalKey)
		return nil, err
	}
	if err := changelog.Append(ctx, tx, userID, entry.ID, "upsert", entry.Name, entry.ParentID, entry.Kind, entry.SizeBytes, entry.MimeType, entry.ContentVersion, entry.ContentHash, session.DeviceID, entry.ClientModifiedAt); err != nil {
		_ = s.storage.Delete(finalKey)
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		_ = s.storage.Delete(finalKey)
		return nil, err
	}
	if replacing {
		// keep previous blob for version history; trim after commit
		_, _ = s.db.Exec(ctx, `
			delete from file_versions where entry_id = $1::uuid and id in (
				select id from file_versions where entry_id = $1::uuid order by version desc offset 20
			)`, entry.ID)
	} else if oldKey != "" {
		_ = s.storage.Delete(oldKey)
	}
	return &entry, nil
}

func (s *Service) AbortBatch(ctx context.Context, userID, batchID string) error {
	unlock := s.lockUser(userID)
	defer unlock()

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var reserved int64
	var status string
	err = tx.QueryRow(ctx, `
		select reserved_bytes, status
		from upload_batches
		where id = $1::uuid and user_id = $2::uuid
		for update`,
		batchID, userID,
	).Scan(&reserved, &status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errors.New("batch not found")
		}
		return err
	}
	if status != "open" {
		return nil
	}

	rows, err := tx.Query(ctx, `
		select temp_key from upload_sessions
		where batch_id = $1::uuid and status = 'open'`,
		batchID,
	)
	if err != nil {
		return err
	}
	tempKeys := make([]string, 0)
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			rows.Close()
			return err
		}
		tempKeys = append(tempKeys, key)
	}
	rows.Close()

	if _, err := tx.Exec(ctx, `update upload_sessions set status = 'aborted', updated_at = now() where batch_id = $1::uuid and status = 'open'`, batchID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `update upload_batches set status = 'aborted', reserved_bytes = 0, updated_at = now() where id = $1::uuid`, batchID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `update users set reserved_bytes = greatest(0, reserved_bytes - $1) where id = $2::uuid`, reserved, userID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	for _, key := range tempKeys {
		_ = s.storage.Delete(key)
	}
	return nil
}

func (s *Service) CleanupExpired(ctx context.Context) error {
	rows, err := s.db.Query(ctx, `
		select id::text, user_id::text, reserved_bytes
		from upload_batches
		where status = 'open' and expires_at <= now()`)
	if err != nil {
		return err
	}
	defer rows.Close()

	type expired struct {
		id, userID string
		reserved   int64
	}
	items := make([]expired, 0)
	for rows.Next() {
		var item expired
		if err := rows.Scan(&item.id, &item.userID, &item.reserved); err != nil {
			return err
		}
		items = append(items, item)
	}
	for _, item := range items {
		_ = s.expireBatch(ctx, item.id, item.userID, item.reserved)
	}
	return nil
}

func (s *Service) expireBatch(ctx context.Context, batchID, userID string, reserved int64) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, `select temp_key from upload_sessions where batch_id = $1::uuid and status = 'open'`, batchID)
	if err != nil {
		return err
	}
	keys := make([]string, 0)
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			rows.Close()
			return err
		}
		keys = append(keys, key)
	}
	rows.Close()

	if _, err := tx.Exec(ctx, `update upload_sessions set status = 'expired', updated_at = now() where batch_id = $1::uuid and status = 'open'`, batchID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `update upload_batches set status = 'expired', reserved_bytes = 0, updated_at = now() where id = $1::uuid and status = 'open'`, batchID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `update users set reserved_bytes = greatest(0, reserved_bytes - $1) where id = $2::uuid`, reserved, userID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	for _, key := range keys {
		_ = s.storage.Delete(key)
	}
	return nil
}

func (s *Service) sessionsForBatch(ctx context.Context, userID, batchID string) ([]domain.UploadSession, error) {
	rows, err := s.db.Query(ctx, `
		select id::text, batch_id::text, parent_id::text, relative_path, file_name, mime_type,
		       expected_size, received_bytes, last_modified_ms, target_entry_id::text, expected_version,
		       content_hash, client_modified_at, device_id::text, status, created_at, updated_at
		from upload_sessions
		where batch_id = $1::uuid and user_id = $2::uuid
		order by relative_path asc`,
		batchID, userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.UploadSession, 0)
	for rows.Next() {
		var item domain.UploadSession
		if err := rows.Scan(
			&item.ID, &item.BatchID, &item.ParentID, &item.RelativePath, &item.FileName, &item.MimeType,
			&item.ExpectedSize, &item.ReceivedBytes, &item.LastModifiedMs, &item.TargetEntryID, &item.ExpectedVersion,
			&item.ContentHash, &item.ClientModifiedAt, &item.DeviceID, &item.Status, &item.CreatedAt, &item.UpdatedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Service) lockUser(userID string) func() {
	value, _ := s.userLocks.LoadOrStore(userID, &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func normalizeManifest(file ManifestFile) (ManifestFile, error) {
	rel := strings.ReplaceAll(strings.TrimSpace(file.RelativePath), "\\", "/")
	if rel == "" || strings.HasPrefix(rel, "/") {
		return ManifestFile{}, errors.New("invalid relative path")
	}
	rel = strings.Trim(rel, "/")
	if rel == "" {
		return ManifestFile{}, errors.New("invalid relative path")
	}
	for _, part := range strings.Split(rel, "/") {
		if part == "" || part == "." || part == ".." {
			return ManifestFile{}, errors.New("invalid relative path")
		}
	}
	rel = path.Clean(rel)
	if rel == "." || strings.HasPrefix(rel, "..") {
		return ManifestFile{}, errors.New("invalid relative path")
	}
	name := strings.TrimSpace(file.FileName)
	if name == "" {
		name = path.Base(rel)
	}
	name = sanitizeName(name)
	if name == "" {
		return ManifestFile{}, errors.New("file name is required")
	}
	if file.ExpectedSize < 0 {
		return ManifestFile{}, errors.New("invalid file size")
	}
	mimeType := strings.TrimSpace(file.MimeType)
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	return ManifestFile{
		RelativePath:   rel,
		FileName:       name,
		MimeType:       mimeType,
		ExpectedSize:   file.ExpectedSize,
		LastModifiedMs: file.LastModifiedMs,
		TargetEntryID: file.TargetEntryID,
		ExpectedVersion: file.ExpectedVersion,
		ContentHash: file.ContentHash,
		ClientModifiedAt: file.ClientModifiedAt,
		DeviceID: file.DeviceID,
	}, nil
}

func validateOwnedFolder(ctx context.Context, tx pgx.Tx, userID, folderID string) error {
	var exists bool
	err := tx.QueryRow(ctx, `
		select exists(
			select 1 from file_entries
			where id = $1::uuid and user_id = $2::uuid and kind = 'folder' and deleted_at is null
		)`, folderID, userID).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return errors.New("parent folder not found")
	}
	return nil
}

func ensureFolderPath(ctx context.Context, tx pgx.Tx, userID, rootID, relativeDir string, cache map[string]string) (string, error) {
	if relativeDir == "" || relativeDir == "." {
		return rootID, nil
	}
	if id, ok := cache[relativeDir]; ok {
		return id, nil
	}
	parts := strings.Split(relativeDir, "/")
	currentParent := rootID
	built := ""
	for _, part := range parts {
		part = sanitizeName(part)
		if part == "" {
			return "", errors.New("invalid folder name")
		}
		if built == "" {
			built = part
		} else {
			built += "/" + part
		}
		if id, ok := cache[built]; ok {
			currentParent = id
			continue
		}
		var existing string
		err := tx.QueryRow(ctx, `
			select id::text from file_entries
			where user_id = $1::uuid and parent_id = $2::uuid and kind = 'folder'
			  and lower(name) = lower($3) and deleted_at is null`,
			userID, currentParent, part,
		).Scan(&existing)
		if err == nil {
			cache[built] = existing
			currentParent = existing
			continue
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return "", err
		}
		var created string
		err = tx.QueryRow(ctx, `
			insert into file_entries (user_id, parent_id, name, kind, storage_key, size_bytes, mime_type)
			values ($1::uuid, $2::uuid, $3, 'folder', $4, 0, 'inode/directory')
			returning id::text`,
			userID, currentParent, part, "folder/"+uuid.NewString(),
		).Scan(&created)
		if err != nil {
			return "", err
		}
		cache[built] = created
		currentParent = created
	}
	return currentParent, nil
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
