package changelog

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

func Append(ctx context.Context, tx pgx.Tx, userID, entryID, op, name string, parentID *string, kind string, size int64, mime string, version int64, hash string, deviceID *string, clientMod *time.Time) error {
	_, err := tx.Exec(ctx, `
		insert into file_changes (
			user_id, entry_id, op, name, parent_id, kind, size_bytes, mime_type,
			content_version, content_hash, device_id, client_modified_at
		) values (
			$1::uuid, $2::uuid, $3, $4, nullif($5, '')::uuid, $6, $7, $8,
			$9, $10, nullif($11, '')::uuid, $12
		)`,
		userID, entryID, op, name, parentID, kind, size, mime, version, hash, deviceID, clientMod,
	)
	return err
}
