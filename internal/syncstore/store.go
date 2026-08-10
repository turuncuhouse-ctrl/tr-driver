// Package syncstore persists the state and durable work queue for the sync client.
package syncstore

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct{ DB *sql.DB }

type Root struct {
	ID, LocalPath, RemoteParentID string
	Cursor                        int64
	Paused                        bool
}
type Node struct {
	ID, RootID, LocalRel, RemoteID, Kind, ContentHash, SyncState string
	Size, MtimeMS, ContentVersion                                int64
}
type Job struct {
	ID                               int64
	RootID, Kind, Payload, LastError string
	Attempts, NextRunAt, CreatedAt   int64
}
type Activity struct {
	ID                           int64
	RootID, Kind, Path, Message string
	CreatedAt                    int64
}

func Open(path string) (*Store, error) {
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	s := &Store{DB: db}
	if err := s.init(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}
func (s *Store) Close() error { return s.DB.Close() }
func (s *Store) init() error {
	_, err := s.DB.Exec(`
		PRAGMA foreign_keys=ON;
		CREATE TABLE IF NOT EXISTS meta(key TEXT PRIMARY KEY, value TEXT NOT NULL);
		CREATE TABLE IF NOT EXISTS roots(id TEXT PRIMARY KEY, local_path TEXT NOT NULL, remote_parent_id TEXT NOT NULL, cursor INTEGER NOT NULL DEFAULT 0, paused INTEGER NOT NULL DEFAULT 0);
		CREATE TABLE IF NOT EXISTS nodes(id TEXT PRIMARY KEY, root_id TEXT NOT NULL, local_rel TEXT NOT NULL, remote_id TEXT, kind TEXT, size INTEGER, mtime_ms INTEGER, content_hash TEXT, content_version INTEGER, sync_state TEXT);
		CREATE UNIQUE INDEX IF NOT EXISTS nodes_root_rel ON nodes(root_id, local_rel);
		CREATE TABLE IF NOT EXISTS jobs(id INTEGER PRIMARY KEY AUTOINCREMENT, root_id TEXT NOT NULL, kind TEXT NOT NULL, payload TEXT NOT NULL, attempts INTEGER NOT NULL DEFAULT 0, next_run_at INTEGER NOT NULL, last_error TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL);
		CREATE INDEX IF NOT EXISTS jobs_due ON jobs(next_run_at);
		CREATE TABLE IF NOT EXISTS activities(
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			root_id TEXT NOT NULL DEFAULT '',
			kind TEXT NOT NULL,
			path TEXT NOT NULL DEFAULT '',
			message TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS activities_created ON activities(created_at DESC);`)
	return err
}
func (s *Store) SetMeta(key, value string) error {
	_, e := s.DB.Exec(`INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return e
}
func (s *Store) GetMeta(key string) (string, error) {
	var value string
	e := s.DB.QueryRow(`SELECT value FROM meta WHERE key=?`, key).Scan(&value)
	if errors.Is(e, sql.ErrNoRows) {
		return "", nil
	}
	return value, e
}
func (s *Store) AddRoot(r Root) error {
	_, e := s.DB.Exec(`INSERT INTO roots(id,local_path,remote_parent_id,cursor,paused) VALUES(?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET local_path=excluded.local_path,remote_parent_id=excluded.remote_parent_id`, r.ID, r.LocalPath, r.RemoteParentID, r.Cursor, boolInt(r.Paused))
	return e
}
func (s *Store) ListRoots() ([]Root, error) {
	rows, e := s.DB.Query(`SELECT id,local_path,remote_parent_id,cursor,paused FROM roots ORDER BY local_path`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []Root
	for rows.Next() {
		var r Root
		var p int
		if e = rows.Scan(&r.ID, &r.LocalPath, &r.RemoteParentID, &r.Cursor, &p); e != nil {
			return nil, e
		}
		r.Paused = p != 0
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *Store) SetCursor(id string, cursor int64) error {
	_, e := s.DB.Exec(`UPDATE roots SET cursor=? WHERE id=?`, cursor, id)
	return e
}
func (s *Store) SetPaused(id string, paused bool) error {
	_, e := s.DB.Exec(`UPDATE roots SET paused=? WHERE id=?`, boolInt(paused), id)
	return e
}
func (s *Store) RemoveRoot(id string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, query := range []string{
		`DELETE FROM nodes WHERE root_id=?`,
		`DELETE FROM jobs WHERE root_id=?`,
		`DELETE FROM roots WHERE id=?`,
	} {
		if _, err := tx.Exec(query, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Store) UpsertNode(n Node) error {
	_, e := s.DB.Exec(`INSERT INTO nodes(id,root_id,local_rel,remote_id,kind,size,mtime_ms,content_hash,content_version,sync_state) VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(root_id,local_rel) DO UPDATE SET id=excluded.id,remote_id=excluded.remote_id,kind=excluded.kind,size=excluded.size,mtime_ms=excluded.mtime_ms,content_hash=excluded.content_hash,content_version=excluded.content_version,sync_state=excluded.sync_state`, n.ID, n.RootID, n.LocalRel, n.RemoteID, n.Kind, n.Size, n.MtimeMS, n.ContentHash, n.ContentVersion, n.SyncState)
	return e
}
func scanNode(row *sql.Row) (Node, error) {
	var n Node
	e := row.Scan(&n.ID, &n.RootID, &n.LocalRel, &n.RemoteID, &n.Kind, &n.Size, &n.MtimeMS, &n.ContentHash, &n.ContentVersion, &n.SyncState)
	if errors.Is(e, sql.ErrNoRows) {
		return Node{}, nil
	}
	return n, e
}
func (s *Store) GetNodeByRel(rootID, rel string) (Node, error) {
	return scanNode(s.DB.QueryRow(`SELECT id,root_id,local_rel,remote_id,kind,size,mtime_ms,content_hash,content_version,sync_state FROM nodes WHERE root_id=? AND local_rel=?`, rootID, rel))
}
func (s *Store) GetNodeByRemote(rootID, remoteID string) (Node, error) {
	return scanNode(s.DB.QueryRow(`SELECT id,root_id,local_rel,remote_id,kind,size,mtime_ms,content_hash,content_version,sync_state FROM nodes WHERE root_id=? AND remote_id=?`, rootID, remoteID))
}
func (s *Store) EnqueueJob(rootID, kind, payload string) (int64, error) {
	var id int64
	err := s.DB.QueryRow(`SELECT id FROM jobs WHERE root_id=? AND kind=? AND payload=? LIMIT 1`, rootID, kind, payload).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	now := time.Now().UnixMilli()
	r, e := s.DB.Exec(`INSERT INTO jobs(root_id,kind,payload,attempts,next_run_at,last_error,created_at) VALUES(?,?,?,0,?,'',?)`, rootID, kind, payload, now, now)
	if e != nil {
		return 0, e
	}
	return r.LastInsertId()
}
func (s *Store) CountPendingJobs() (int64, error) {
	var count int64
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM jobs`).Scan(&count)
	return count, err
}
func (s *Store) ListFailedJobs(limit int) ([]Job, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.DB.Query(`SELECT id,root_id,kind,payload,attempts,next_run_at,last_error,created_at FROM jobs WHERE attempts>0 AND last_error!='' ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		var j Job
		if err := rows.Scan(&j.ID, &j.RootID, &j.Kind, &j.Payload, &j.Attempts, &j.NextRunAt, &j.LastError, &j.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}
func (s *Store) AddActivity(rootID, kind, path, message string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO activities(root_id,kind,path,message,created_at) VALUES(?,?,?,?,?)`, rootID, kind, path, message, time.Now().UnixMilli()); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM activities WHERE id NOT IN (SELECT id FROM activities ORDER BY created_at DESC, id DESC LIMIT 200)`); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) ListActivities(limit int) ([]Activity, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.DB.Query(`SELECT id,root_id,kind,path,message,created_at FROM activities ORDER BY created_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Activity
	for rows.Next() {
		var a Activity
		if err := rows.Scan(&a.ID, &a.RootID, &a.Kind, &a.Path, &a.Message, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
func (s *Store) ListDueJobs(limit int) ([]Job, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, e := s.DB.Query(`SELECT id,root_id,kind,payload,attempts,next_run_at,last_error,created_at FROM jobs WHERE next_run_at<=? ORDER BY id LIMIT ?`, time.Now().UnixMilli(), limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		var j Job
		e = rows.Scan(&j.ID, &j.RootID, &j.Kind, &j.Payload, &j.Attempts, &j.NextRunAt, &j.LastError, &j.CreatedAt)
		if e != nil {
			return nil, e
		}
		out = append(out, j)
	}
	return out, rows.Err()
}
func (s *Store) MarkJobDone(id int64) error {
	_, e := s.DB.Exec(`DELETE FROM jobs WHERE id=?`, id)
	return e
}
func (s *Store) MarkJobRetry(id int64, attempts int64, message string) error {
	shift := attempts
	if shift > 8 {
		shift = 8
	}
	delay := time.Duration(1<<uint(shift)) * time.Second
	_, e := s.DB.Exec(`UPDATE jobs SET attempts=?,next_run_at=?,last_error=? WHERE id=?`, attempts, time.Now().Add(delay).UnixMilli(), message, id)
	return e
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
