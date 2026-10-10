package store

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// Store is a thin SQLite-backed persistence layer for users and their
// saved channel lists. MTProto sessions are stored separately as encrypted
// files by the backend.
type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func migrate(db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS users (
	user_id    INTEGER PRIMARY KEY,
	phone      TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);
CREATE TABLE IF NOT EXISTS channels (
	user_id  INTEGER NOT NULL,
	channel  TEXT NOT NULL,
	added_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
	PRIMARY KEY (user_id, channel)
);
`
	_, err := db.Exec(schema)
	return err
}

func (s *Store) Close() error { return s.db.Close() }

// UpsertUser records (or refreshes) the phone number associated with a
// Telegram user id.
func (s *Store) UpsertUser(userID int64, phone string) error {
	_, err := s.db.Exec(
		`INSERT INTO users (user_id, phone) VALUES (?, ?)
		 ON CONFLICT(user_id) DO UPDATE SET phone = excluded.phone`,
		userID, phone,
	)
	return err
}

// ListChannels returns the saved channels for a user in insertion order.
func (s *Store) ListChannels(userID int64) ([]string, error) {
	rows, err := s.db.Query(
		`SELECT channel FROM channels WHERE user_id = ? ORDER BY added_at, rowid`, userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SetChannels replaces the saved channel list for a user.
func (s *Store) SetChannels(userID int64, channels []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM channels WHERE user_id = ?`, userID); err != nil {
		return err
	}
	for _, c := range channels {
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO channels (user_id, channel) VALUES (?, ?)`, userID, c,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// AddChannels appends channels to a user's saved list (deduplicated).
func (s *Store) AddChannels(userID int64, channels []string) error {
	for _, c := range channels {
		if _, err := s.db.Exec(
			`INSERT OR IGNORE INTO channels (user_id, channel) VALUES (?, ?)`, userID, c,
		); err != nil {
			return err
		}
	}
	return nil
}

// RemoveChannel removes a single channel from a user's saved list.
func (s *Store) RemoveChannel(userID int64, channel string) error {
	_, err := s.db.Exec(
		`DELETE FROM channels WHERE user_id = ? AND channel = ?`, userID, channel,
	)
	return err
}
