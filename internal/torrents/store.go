package torrents

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Torrent states as persisted.
const (
	StateActive    = "active"
	StatePaused    = "paused"
	StateCompleted = "completed"
	StateError     = "error"
)

// Record is a torrent as stored. Magnet and Metainfo are needed to resume a
// download after a restart; they can embed private tracker passkeys and are
// never returned by the API or written to logs.
//
// FilePriorities holds the per-file choices (skip/normal/high) as a JSON
// object mapping file path to priority name; it is applied every time the
// torrent is attached to the client.
//
// DetachedAt is set when the administrator removes the torrent from the list
// without deleting its data. The row is kept (hidden from List) so that
// adding the same torrent again recognises the existing files, verifies them
// and continues instead of starting over; old detached rows are pruned.
type Record struct {
	ID             string
	InfoHash       string
	Name           string
	ContentPath    string
	Magnet         string
	Metainfo       []byte
	State          string
	Error          string
	TotalBytes     int64
	CompletedBytes int64
	AddedBy        string
	AddedAt        time.Time
	CompletedAt    *time.Time

	// Seeding statistics and limits (0 = unlimited/off).
	UploadedBytes     int64
	SeededSeconds     int64
	SeedRatioLimit    float64
	SeedTimeLimitSec  int
	DownloadLimitKBps int64
	UploadLimitKBps   int64

	FilePriorities string
	SeedStopped    bool
	DetachedAt     *time.Time
}

var (
	ErrDuplicate = errors.New("bu torrent zaten listede")
	ErrNotFound  = errors.New("torrent bulunamadı")
)

// Store is the persistence the torrent service needs.
type Store interface {
	// List returns the torrents shown in the panel; detached rows are hidden.
	List(ctx context.Context) ([]Record, error)
	Insert(ctx context.Context, rec Record) (Record, error)
	Save(ctx context.Context, rec Record) error
	Delete(ctx context.Context, id string) error
	// Detach hides a torrent from the list but keeps its row (and therefore
	// the link between the info hash and the files on disk).
	Detach(ctx context.Context, id string) error
	// DetachedByInfoHash finds a hidden row so it can be revived.
	DetachedByInfoHash(ctx context.Context, infoHash string) (Record, bool, error)
	// PruneDetached forgets detached rows older than the cutoff (files stay).
	PruneDetached(ctx context.Context, before time.Time) error
	Setting(ctx context.Context, key string) (string, bool, error)
	SetSetting(ctx context.Context, key, value string) error
	Notify(ctx context.Context, userID, kind, title, body string) error
}

type pgStore struct {
	db *pgxpool.Pool
}

// NewPostgresStore returns the production Store.
func NewPostgresStore(db *pgxpool.Pool) Store { return &pgStore{db: db} }

const recordColumns = `id::text, info_hash, name, content_path, magnet, metainfo, state, error,
		       total_bytes, completed_bytes, coalesce(added_by_user_id::text, ''), added_at, completed_at,
		       uploaded_bytes, seeded_seconds, seed_ratio_limit, seed_time_limit_sec,
		       download_limit_kbps, upload_limit_kbps, file_priorities, seed_stopped, detached_at`

func scanRecord(row pgx.Row) (Record, error) {
	var rec Record
	err := row.Scan(&rec.ID, &rec.InfoHash, &rec.Name, &rec.ContentPath, &rec.Magnet, &rec.Metainfo, &rec.State, &rec.Error,
		&rec.TotalBytes, &rec.CompletedBytes, &rec.AddedBy, &rec.AddedAt, &rec.CompletedAt,
		&rec.UploadedBytes, &rec.SeededSeconds, &rec.SeedRatioLimit, &rec.SeedTimeLimitSec,
		&rec.DownloadLimitKBps, &rec.UploadLimitKBps, &rec.FilePriorities, &rec.SeedStopped, &rec.DetachedAt)
	return rec, err
}

func (s *pgStore) List(ctx context.Context) ([]Record, error) {
	rows, err := s.db.Query(ctx, `select `+recordColumns+`
		from torrents where detached_at is null order by added_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		var rec Record
		if err := rows.Scan(&rec.ID, &rec.InfoHash, &rec.Name, &rec.ContentPath, &rec.Magnet, &rec.Metainfo, &rec.State, &rec.Error,
			&rec.TotalBytes, &rec.CompletedBytes, &rec.AddedBy, &rec.AddedAt, &rec.CompletedAt,
			&rec.UploadedBytes, &rec.SeededSeconds, &rec.SeedRatioLimit, &rec.SeedTimeLimitSec,
			&rec.DownloadLimitKBps, &rec.UploadLimitKBps, &rec.FilePriorities, &rec.SeedStopped, &rec.DetachedAt); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func (s *pgStore) Insert(ctx context.Context, rec Record) (Record, error) {
	var addedBy *string
	if rec.AddedBy != "" {
		addedBy = &rec.AddedBy
	}
	err := s.db.QueryRow(ctx, `
		insert into torrents (info_hash, name, content_path, magnet, metainfo, state, error, total_bytes, completed_bytes, added_by_user_id)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::uuid)
		returning id::text, added_at`,
		rec.InfoHash, rec.Name, rec.ContentPath, rec.Magnet, rec.Metainfo, rec.State, rec.Error, rec.TotalBytes, rec.CompletedBytes, addedBy,
	).Scan(&rec.ID, &rec.AddedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return Record{}, ErrDuplicate
		}
		return Record{}, err
	}
	return rec, nil
}

func (s *pgStore) Save(ctx context.Context, rec Record) error {
	_, err := s.db.Exec(ctx, `
		update torrents set name = $2, content_path = $3, magnet = $4, metainfo = $5, state = $6, error = $7,
		       total_bytes = $8, completed_bytes = $9, completed_at = $10,
		       uploaded_bytes = $11, seeded_seconds = $12, seed_ratio_limit = $13, seed_time_limit_sec = $14,
		       download_limit_kbps = $15, upload_limit_kbps = $16, file_priorities = $17,
		       seed_stopped = $18, detached_at = $19, added_at = $20, updated_at = now()
		where id = $1::uuid`,
		rec.ID, rec.Name, rec.ContentPath, rec.Magnet, rec.Metainfo, rec.State, rec.Error,
		rec.TotalBytes, rec.CompletedBytes, rec.CompletedAt,
		rec.UploadedBytes, rec.SeededSeconds, rec.SeedRatioLimit, rec.SeedTimeLimitSec,
		rec.DownloadLimitKBps, rec.UploadLimitKBps, rec.FilePriorities, rec.SeedStopped, rec.DetachedAt,
		rec.AddedAt)
	return err
}

func (s *pgStore) Delete(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx, `delete from torrents where id = $1::uuid`, id)
	return err
}

func (s *pgStore) Detach(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `update torrents set detached_at = now(), updated_at = now() where id = $1::uuid`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *pgStore) DetachedByInfoHash(ctx context.Context, infoHash string) (Record, bool, error) {
	row := s.db.QueryRow(ctx, `select `+recordColumns+`
		from torrents where info_hash = $1 and detached_at is not null`, infoHash)
	rec, err := scanRecord(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	return rec, true, nil
}

func (s *pgStore) PruneDetached(ctx context.Context, before time.Time) error {
	_, err := s.db.Exec(ctx, `delete from torrents where detached_at is not null and detached_at < $1`, before)
	return err
}

func (s *pgStore) Setting(ctx context.Context, key string) (string, bool, error) {
	var value string
	err := s.db.QueryRow(ctx, `select value from app_settings where key = $1`, key).Scan(&value)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return value, err == nil, err
}

func (s *pgStore) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.Exec(ctx, `
		insert into app_settings (key, value, updated_at) values ($1, $2, now())
		on conflict (key) do update set value = excluded.value, updated_at = now()`, key, value)
	return err
}

func (s *pgStore) Notify(ctx context.Context, userID, kind, title, body string) error {
	if userID == "" {
		return nil
	}
	_, err := s.db.Exec(ctx, `
		insert into notifications (user_id, kind, title, body)
		select id, $2, $3, $4 from users where id = $1::uuid`, userID, kind, title, body)
	return err
}
