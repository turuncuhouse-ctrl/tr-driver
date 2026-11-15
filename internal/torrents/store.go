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
}

var (
	ErrDuplicate = errors.New("bu torrent zaten listede")
	ErrNotFound  = errors.New("torrent bulunamadı")
)

// Store is the persistence the torrent service needs.
type Store interface {
	List(ctx context.Context) ([]Record, error)
	Insert(ctx context.Context, rec Record) (Record, error)
	Save(ctx context.Context, rec Record) error
	Delete(ctx context.Context, id string) error
	Setting(ctx context.Context, key string) (string, bool, error)
	SetSetting(ctx context.Context, key, value string) error
	Notify(ctx context.Context, userID, kind, title, body string) error
}

type pgStore struct {
	db *pgxpool.Pool
}

// NewPostgresStore returns the production Store.
func NewPostgresStore(db *pgxpool.Pool) Store { return &pgStore{db: db} }

func (s *pgStore) List(ctx context.Context) ([]Record, error) {
	rows, err := s.db.Query(ctx, `
		select id::text, info_hash, name, content_path, magnet, metainfo, state, error,
		       total_bytes, completed_bytes, coalesce(added_by_user_id::text, ''), added_at, completed_at
		from torrents order by added_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		var rec Record
		if err := rows.Scan(&rec.ID, &rec.InfoHash, &rec.Name, &rec.ContentPath, &rec.Magnet, &rec.Metainfo, &rec.State, &rec.Error,
			&rec.TotalBytes, &rec.CompletedBytes, &rec.AddedBy, &rec.AddedAt, &rec.CompletedAt); err != nil {
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
		update torrents set name = $2, content_path = $3, metainfo = $4, state = $5, error = $6,
		       total_bytes = $7, completed_bytes = $8, completed_at = $9, updated_at = now()
		where id = $1::uuid`,
		rec.ID, rec.Name, rec.ContentPath, rec.Metainfo, rec.State, rec.Error, rec.TotalBytes, rec.CompletedBytes, rec.CompletedAt)
	return err
}

func (s *pgStore) Delete(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx, `delete from torrents where id = $1::uuid`, id)
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
