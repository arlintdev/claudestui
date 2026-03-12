package store

import (
	"database/sql"
	"os"
	"path/filepath"

	"github.com/arlintdev/claudes/internal/instance"
	_ "modernc.org/sqlite"
)

// Store wraps the SQLite database for instance persistence.
type Store struct {
	db *sql.DB
}

// Open creates or opens the SQLite database at ~/.config/claudes/claudes.db.
// It enables WAL mode for better concurrent read performance.
func Open() (*Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	dir := filepath.Join(home, ".config", "claudes")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	dbPath := filepath.Join(dir, "claudes.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}

	// Enable WAL mode for concurrent reads.
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, err
	}

	// Create the instances table.
	const ddl = `CREATE TABLE IF NOT EXISTS instances (
		name       TEXT PRIMARY KEY,
		dir        TEXT NOT NULL,
		task       TEXT NOT NULL DEFAULT '',
		mode       TEXT NOT NULL DEFAULT 'safe',
		model      TEXT NOT NULL DEFAULT '',
		window_id  TEXT NOT NULL DEFAULT '',
		session_id TEXT NOT NULL DEFAULT '',
		started_at TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`
	if _, err := db.Exec(ddl); err != nil {
		db.Close()
		return nil, err
	}

	// Migration: add model column if missing (upgrade from older schema).
	s := &Store{db: db}
	s.migrate()

	return s, nil
}

// migrate adds columns that may be missing from older schemas.
func (s *Store) migrate() {
	// model column added after initial release
	_, _ = s.db.Exec("ALTER TABLE instances ADD COLUMN model TEXT NOT NULL DEFAULT ''")
	// group_name column for persistent instance groups
	_, _ = s.db.Exec("ALTER TABLE instances ADD COLUMN group_name TEXT NOT NULL DEFAULT ''")
}

// Close closes the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// Save persists an instance row (INSERT OR REPLACE).
func (s *Store) Save(name, dir, task, mode, model, groupName, windowID, sessionID, startedAt string) error {
	const q = `INSERT OR REPLACE INTO instances
		(name, dir, task, mode, model, group_name, window_id, session_id, started_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err := s.db.Exec(q, name, dir, task, mode, model, groupName, windowID, sessionID, startedAt)
	return err
}

// All returns all persisted instance rows.
func (s *Store) All() ([]instance.StoreRow, error) {
	const q = `SELECT name, dir, task, mode, model, group_name, window_id, session_id, started_at FROM instances`
	rows, err := s.db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []instance.StoreRow
	for rows.Next() {
		var r instance.StoreRow
		if err := rows.Scan(&r.Name, &r.Dir, &r.Task, &r.Mode, &r.Model, &r.GroupName, &r.WindowID, &r.SessionID, &r.StartedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Delete permanently removes an instance row by name.
func (s *Store) Delete(name string) error {
	_, err := s.db.Exec("DELETE FROM instances WHERE name = ?", name)
	return err
}
