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

	s := &Store{db: db}
	if err := s.ensureSchema(); err != nil {
		db.Close()
		return nil, err
	}

	return s, nil
}

// ensureSchema creates or migrates the instances table.
func (s *Store) ensureSchema() error {
	// Check if the table exists at all.
	var count int
	err := s.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='instances'").Scan(&count)
	if err != nil {
		return err
	}

	if count == 0 {
		// Fresh install — create table without window_id.
		const ddl = `CREATE TABLE instances (
			id         TEXT PRIMARY KEY,
			name       TEXT NOT NULL,
			dir        TEXT NOT NULL,
			task       TEXT NOT NULL DEFAULT '',
			mode       TEXT NOT NULL DEFAULT 'safe',
			model      TEXT NOT NULL DEFAULT '',
			host       TEXT NOT NULL DEFAULT '',
			group_name TEXT NOT NULL DEFAULT '',
			session_id TEXT NOT NULL DEFAULT '',
			started_at TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`
		_, err := s.db.Exec(ddl)
		return err
	}

	// Table exists — handle migrations.
	// Add host column if missing (ignore error if already exists).
	_, _ = s.db.Exec("ALTER TABLE instances ADD COLUMN host TEXT NOT NULL DEFAULT ''")

	// Check if it has the id column (old schema migration).
	hasID := false
	rows, err := s.db.Query("PRAGMA table_info(instances)")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull int
		var dfltValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			return err
		}
		if name == "id" {
			hasID = true
		}
	}

	if hasID {
		return nil
	}

	// Migrate: old schema has name as PK, no id column.
	_, err = s.db.Exec(`
		CREATE TABLE instances_new (
			id         TEXT PRIMARY KEY,
			name       TEXT NOT NULL,
			dir        TEXT NOT NULL,
			task       TEXT NOT NULL DEFAULT '',
			mode       TEXT NOT NULL DEFAULT 'safe',
			model      TEXT NOT NULL DEFAULT '',
			host       TEXT NOT NULL DEFAULT '',
			group_name TEXT NOT NULL DEFAULT '',
			session_id TEXT NOT NULL DEFAULT '',
			started_at TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`)
	if err != nil {
		return err
	}

	_, _ = s.db.Exec("ALTER TABLE instances ADD COLUMN model TEXT NOT NULL DEFAULT ''")
	_, _ = s.db.Exec("ALTER TABLE instances ADD COLUMN group_name TEXT NOT NULL DEFAULT ''")

	_, err = s.db.Exec(`
		INSERT INTO instances_new (id, name, dir, task, mode, model, group_name, session_id, started_at, created_at)
		SELECT lower(hex(randomblob(4))), name, dir, task, mode, model, group_name, session_id, started_at, created_at
		FROM instances`)
	if err != nil {
		s.db.Exec("DROP TABLE instances_new")
		return err
	}

	_, err = s.db.Exec("DROP TABLE instances")
	if err != nil {
		return err
	}
	_, err = s.db.Exec("ALTER TABLE instances_new RENAME TO instances")
	return err
}

// Close closes the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// Save persists an instance row (INSERT OR REPLACE).
func (s *Store) Save(id, name, dir, task, mode, model, host, groupName, sessionID, startedAt string) error {
	const q = `INSERT OR REPLACE INTO instances
		(id, name, dir, task, mode, model, host, group_name, session_id, started_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err := s.db.Exec(q, id, name, dir, task, mode, model, host, groupName, sessionID, startedAt)
	return err
}

// All returns all persisted instance rows.
func (s *Store) All() ([]instance.StoreRow, error) {
	const q = `SELECT id, name, dir, task, mode, model, host, group_name, session_id, started_at FROM instances`
	rows, err := s.db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []instance.StoreRow
	for rows.Next() {
		var r instance.StoreRow
		if err := rows.Scan(&r.ID, &r.Name, &r.Dir, &r.Task, &r.Mode, &r.Model, &r.Host, &r.GroupName, &r.SessionID, &r.StartedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Delete permanently removes an instance row by ID.
func (s *Store) Delete(id string) error {
	_, err := s.db.Exec("DELETE FROM instances WHERE id = ?", id)
	return err
}
