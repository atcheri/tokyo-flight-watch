// Package store keeps the bot's users and watches in SQLite.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")

type Store struct {
	db *sql.DB
}

type User struct {
	ChatID    int64
	Name      string
	APIKey    []byte // encrypted SearchApi key, nil if not set
	Wizard    string // JSON state of an ongoing conversation, empty if none
	CreatedAt time.Time
}

type Watch struct {
	ID        int64
	ChatID    int64
	Config    string // YAML, see config.Config
	EveryDays int
	Paused    bool
	NextRun   time.Time
	History   string // JSON, see state.History
	LastRun   time.Time
	LastError string
	CreatedAt time.Time
}

const schema = `
CREATE TABLE IF NOT EXISTS users (
	chat_id    INTEGER PRIMARY KEY,
	name       TEXT NOT NULL DEFAULT '',
	api_key    BLOB,
	wizard     TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS watches (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	chat_id    INTEGER NOT NULL REFERENCES users(chat_id) ON DELETE CASCADE,
	config     TEXT NOT NULL,
	every_days INTEGER NOT NULL,
	paused     INTEGER NOT NULL DEFAULT 0,
	next_run   TEXT NOT NULL,
	history    TEXT NOT NULL DEFAULT '',
	last_run   TEXT NOT NULL DEFAULT '',
	last_error TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS watches_due ON watches(paused, next_run);
CREATE TABLE IF NOT EXISTS meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);`

// Open opens (and creates if needed) the database at path.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // one writer; the bot is tiny
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Backup writes a consistent copy of the database to path.
func (s *Store) Backup(ctx context.Context, path string) error {
	_, err := s.db.ExecContext(ctx, "VACUUM INTO ?", path)
	return err
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

// --- users

func (s *Store) CreateUser(ctx context.Context, chatID int64, name string, now time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO users (chat_id, name, created_at) VALUES (?, ?, ?)
		 ON CONFLICT(chat_id) DO UPDATE SET name = excluded.name`, chatID, name, formatTime(now))
	return err
}

func (s *Store) User(ctx context.Context, chatID int64) (User, error) {
	var u User
	var created string
	err := s.db.QueryRowContext(ctx, `SELECT chat_id, name, api_key, wizard, created_at FROM users WHERE chat_id = ?`, chatID).
		Scan(&u.ChatID, &u.Name, &u.APIKey, &u.Wizard, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	u.CreatedAt = parseTime(created)
	return u, err
}

func (s *Store) SetAPIKey(ctx context.Context, chatID int64, sealed []byte) error {
	return s.exec1(ctx, `UPDATE users SET api_key = ? WHERE chat_id = ?`, sealed, chatID)
}

func (s *Store) SetWizard(ctx context.Context, chatID int64, wizard string) error {
	return s.exec1(ctx, `UPDATE users SET wizard = ? WHERE chat_id = ?`, wizard, chatID)
}

// DeleteUser removes a user and all their watches.
func (s *Store) DeleteUser(ctx context.Context, chatID int64) error {
	return s.exec1(ctx, `DELETE FROM users WHERE chat_id = ?`, chatID)
}

// --- watches

func (s *Store) CreateWatch(ctx context.Context, w Watch) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO watches (chat_id, config, every_days, paused, next_run, history, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		w.ChatID, w.Config, w.EveryDays, w.Paused, formatTime(w.NextRun), w.History, formatTime(w.CreatedAt))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// SaveWatch updates everything but the owner and creation date.
func (s *Store) SaveWatch(ctx context.Context, w Watch) error {
	return s.exec1(ctx,
		`UPDATE watches SET config = ?, every_days = ?, paused = ?, next_run = ?, history = ?, last_run = ?, last_error = ?
		 WHERE id = ?`,
		w.Config, w.EveryDays, w.Paused, formatTime(w.NextRun), w.History, formatTime(w.LastRun), w.LastError, w.ID)
}

func (s *Store) DeleteWatch(ctx context.Context, chatID, id int64) error {
	return s.exec1(ctx, `DELETE FROM watches WHERE id = ? AND chat_id = ?`, id, chatID)
}

// Watch returns the watch id owned by chatID.
func (s *Store) Watch(ctx context.Context, chatID, id int64) (Watch, error) {
	ws, err := s.queryWatches(ctx, `WHERE id = ? AND chat_id = ?`, id, chatID)
	if err != nil {
		return Watch{}, err
	}
	if len(ws) == 0 {
		return Watch{}, ErrNotFound
	}
	return ws[0], nil
}

func (s *Store) Watches(ctx context.Context, chatID int64) ([]Watch, error) {
	return s.queryWatches(ctx, `WHERE chat_id = ? ORDER BY id`, chatID)
}

// DueWatches returns the active watches whose next run is at or before now.
func (s *Store) DueWatches(ctx context.Context, now time.Time) ([]Watch, error) {
	return s.queryWatches(ctx, `WHERE paused = 0 AND next_run <= ? ORDER BY next_run`, formatTime(now))
}

func (s *Store) queryWatches(ctx context.Context, where string, args ...any) ([]Watch, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, chat_id, config, every_days, paused, next_run, history, last_run, last_error, created_at FROM watches `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ws []Watch
	for rows.Next() {
		var w Watch
		var next, last, created string
		if err := rows.Scan(&w.ID, &w.ChatID, &w.Config, &w.EveryDays, &w.Paused, &next, &w.History, &last, &w.LastError, &created); err != nil {
			return nil, err
		}
		w.NextRun, w.LastRun, w.CreatedAt = parseTime(next), parseTime(last), parseTime(created)
		ws = append(ws, w)
	}
	return ws, rows.Err()
}

// --- meta

func (s *Store) Meta(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (s *Store) SetMeta(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// exec1 runs a statement that must affect exactly one row.
func (s *Store) exec1(ctx context.Context, query string, args ...any) error {
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
