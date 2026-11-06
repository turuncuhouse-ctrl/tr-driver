// Package blobgc removes storage blobs that are no longer referenced by the
// database. It is used after version pruning and entry purges.
package blobgc

import (
	"context"
	"log"

	"necipdrive/internal/storage"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PruneVersions keeps the newest keep versions for entryID and deletes the
// detached blobs. Reference checks run after the delete transaction commits.
func PruneVersions(ctx context.Context, db *pgxpool.Pool, store *storage.Local, entryID string, keep int) error {
	if keep < 0 {
		keep = 0
	}
	rows, err := db.Query(ctx, `
		delete from file_versions
		where entry_id = $1::uuid and id in (
			select id from file_versions where entry_id = $1::uuid order by version desc offset $2
		)
		returning storage_key`, entryID, keep)
	if err != nil {
		return err
	}
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			rows.Close()
			return err
		}
		keys = append(keys, key)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	DeleteUnreferenced(ctx, db, store, keys)
	return nil
}

// DeleteUnreferenced removes keys that are not used by any live entry, version
// or open upload session. Errors are logged because the DB rows are already gone.
func DeleteUnreferenced(ctx context.Context, db *pgxpool.Pool, store *storage.Local, keys []string) {
	seen := map[string]bool{}
	for _, key := range keys {
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		var referenced bool
		err := db.QueryRow(ctx, `
			select exists(
				select 1 from file_entries where storage_key = $1
				union all
				select 1 from file_versions where storage_key = $1
				union all
				select 1 from upload_sessions where temp_key = $1 and status = 'open'
			)`, key).Scan(&referenced)
		if err != nil {
			log.Printf("blobgc reference check %s: %v", key, err)
			continue
		}
		if referenced {
			continue
		}
		if err := store.Delete(key); err != nil {
			log.Printf("blobgc delete %s: %v", key, err)
		}
	}
}
