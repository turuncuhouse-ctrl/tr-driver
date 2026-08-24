package files

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"

	"necipdrive/internal/access"
)

const (
	maxZipFiles = 2000
	maxZipBytes = 2 * 1024 * 1024 * 1024 // 2 GiB
)

type zipFileItem struct {
	relPath    string
	storageKey string
	sizeBytes  int64
	modTime    time.Time
}

// WriteFolderZip streams a folder as application/zip (sets response headers).
func (s *Service) WriteFolderZip(ctx context.Context, userID, userRole, folderID string, w http.ResponseWriter) error {
	if err := s.access.Require(ctx, userID, userRole, folderID, access.ActionView); err != nil {
		return err
	}
	entry, err := s.entryByID(ctx, folderID)
	if err != nil {
		return err
	}
	if entry.Kind != "folder" {
		return errors.New("not a folder")
	}

	items, err := s.collectZipFiles(ctx, folderID)
	if err != nil {
		return err
	}
	var total int64
	for _, item := range items {
		total += item.sizeBytes
		if total > maxZipBytes {
			return fmt.Errorf("folder exceeds max zip size (%d bytes)", maxZipBytes)
		}
	}
	if len(items) > maxZipFiles {
		return fmt.Errorf("folder exceeds max zip file count (%d)", maxZipFiles)
	}

	zipName := entry.Name + ".zip"
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": zipName}))
	w.Header().Set("X-Content-Type-Options", "nosniff")

	zw := zip.NewWriter(w)
	defer zw.Close()

	for _, item := range items {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		reader, err := s.storage.Open(item.storageKey)
		if err != nil {
			return fmt.Errorf("open %s: %w", item.relPath, err)
		}
		header := &zip.FileHeader{
			Name:   item.relPath,
			Method: zip.Store,
		}
		header.SetModTime(item.modTime)
		header.UncompressedSize64 = uint64(item.sizeBytes)
		writer, err := zw.CreateHeader(header)
		if err != nil {
			reader.Close()
			return err
		}
		if _, err := io.CopyN(writer, reader, item.sizeBytes); err != nil {
			reader.Close()
			return fmt.Errorf("write %s: %w", item.relPath, err)
		}
		reader.Close()
	}
	return nil
}

func (s *Service) collectZipFiles(ctx context.Context, folderID string) ([]zipFileItem, error) {
	rows, err := s.db.Query(ctx, `
		with recursive tree as (
			select id, parent_id, name, kind, storage_key, size_bytes, updated_at,
			       ''::text as rel_path
			from file_entries
			where id = $1::uuid and deleted_at is null
			union all
			select f.id, f.parent_id, f.name, f.kind, f.storage_key, f.size_bytes, f.updated_at,
			       case when t.rel_path = '' then f.name else t.rel_path || '/' || f.name end
			from file_entries f
			join tree t on f.parent_id = t.id
			where f.deleted_at is null
		)
		select rel_path, storage_key, size_bytes, updated_at
		from tree
		where kind = 'file' and storage_key <> ''
		order by rel_path asc`,
		folderID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]zipFileItem, 0)
	seen := map[string]struct{}{}
	for rows.Next() {
		var item zipFileItem
		if err := rows.Scan(&item.relPath, &item.storageKey, &item.sizeBytes, &item.modTime); err != nil {
			return nil, err
		}
		safe, err := sanitizeZipPath(item.relPath)
		if err != nil {
			continue
		}
		item.relPath = safe
		if _, dup := seen[safe]; dup {
			item.relPath = dedupeZipPath(safe, seen)
		}
		seen[item.relPath] = struct{}{}
		items = append(items, item)
	}
	return items, rows.Err()
}

func sanitizeZipPath(rel string) (string, error) {
	rel = strings.ReplaceAll(rel, "\\", "/")
	rel = path.Clean(rel)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", errors.New("invalid zip path")
	}
	return rel, nil
}

func dedupeZipPath(base string, seen map[string]struct{}) string {
	ext := path.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	for i := 2; i < 10_000; i++ {
		candidate := fmt.Sprintf("%s (%d)%s", stem, i, ext)
		if _, ok := seen[candidate]; !ok {
			return candidate
		}
	}
	return fmt.Sprintf("%s-%d%s", stem, time.Now().UnixNano(), ext)
}
