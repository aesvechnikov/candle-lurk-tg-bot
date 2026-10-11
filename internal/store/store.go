package store

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// Channel is a saved channel with optional Telegram metadata (title/description).
type Channel struct {
	Name  string
	Title string
	About string
}

// Store is a thin SQLite-backed persistence layer for saved channel lists.
// MTProto sessions are stored separately as encrypted files by the backend.
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
CREATE TABLE IF NOT EXISTS channels (
	user_id  INTEGER NOT NULL,
	channel  TEXT NOT NULL,
	added_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
	title    TEXT NOT NULL DEFAULT '',
	about    TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (user_id, channel)
);
`
	if _, err := db.Exec(schema); err != nil {
		return err
	}
	return ensureColumns(db)
}

// ensureColumns adds title/about to databases created before those columns
// existed (SQLite has no "ADD COLUMN IF NOT EXISTS").
func ensureColumns(db *sql.DB) error {
	cols, err := existingColumns(db, "channels")
	if err != nil {
		return err
	}
	has := func(name string) bool {
		for _, c := range cols {
			if c == name {
				return true
			}
		}
		return false
	}
	if !has("title") {
		if _, err := db.Exec(`ALTER TABLE channels ADD COLUMN title TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	if !has("about") {
		if _, err := db.Exec(`ALTER TABLE channels ADD COLUMN about TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	return nil
}

func existingColumns(db *sql.DB, table string) ([]string, error) {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var cols []string
	for rows.Next() {
		var (
			cid         int
			name, typ   string
			notnull, pk int
			dflt        sql.NullString
		)
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		cols = append(cols, name)
	}
	return cols, rows.Err()
}

func (s *Store) Close() error { return s.db.Close() }

// ListChannels returns the saved channels for a user in insertion order.
func (s *Store) ListChannels(userID int64) ([]Channel, error) {
	rows, err := s.db.Query(
		`SELECT channel, title, about FROM channels WHERE user_id = ? ORDER BY added_at, rowid`, userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Channel
	for rows.Next() {
		var c Channel
		if err := rows.Scan(&c.Name, &c.Title, &c.About); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// AddChannels appends channels (with optional metadata) to a user's list,
// refreshing title/about for channels that already exist.
func (s *Store) AddChannels(userID int64, channels []Channel) error {
	for _, c := range channels {
		if _, err := s.db.Exec(
			`INSERT INTO channels (user_id, channel, title, about) VALUES (?, ?, ?, ?)
			 ON CONFLICT(user_id, channel) DO UPDATE SET title = excluded.title, about = excluded.about`,
			userID, c.Name, c.Title, c.About,
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

// DeleteUser removes all saved data for a user (their channel list).
func (s *Store) DeleteUser(userID int64) error {
	_, err := s.db.Exec(`DELETE FROM channels WHERE user_id = ?`, userID)
	return err
}
