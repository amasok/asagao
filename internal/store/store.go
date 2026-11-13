// Package store is the local SQLite cache that every front end (TUI, CLI, MCP) reads from.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the database at path with 0600 permissions and migrates it.
func Open(ctx context.Context, path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	f.Close()

	dsn := "file:" + (&url.URL{Path: path}).EscapedPath() +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate %s: %w", path, err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	for i := version; i < len(migrations); i++ {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// tx runs fn in a transaction.
func (s *Store) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// SyncState is a small key/value table for sync bookkeeping (last sync times, sync tokens).
func (s *Store) SyncState(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM sync_state WHERE key = ?", key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (s *Store) SetSyncState(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO sync_state(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value",
		key, value)
	return err
}

// Stats summarises what is in the store (for `sync status`).
type Stats struct {
	Workspaces int        `json:"workspaces"`
	Projects   int        `json:"projects"`
	Sections   int        `json:"sections"`
	Tasks      int        `json:"tasks"`
	OpenTasks  int        `json:"open_tasks"`
	LastSync   *time.Time `json:"last_sync,omitempty"`
}

func (s *Store) Stats(ctx context.Context) (Stats, error) {
	var st Stats
	q := `SELECT
		(SELECT count(*) FROM workspaces),
		(SELECT count(*) FROM projects),
		(SELECT count(*) FROM sections),
		(SELECT count(*) FROM tasks),
		(SELECT count(*) FROM tasks WHERE completed = 0)`
	if err := s.db.QueryRowContext(ctx, q).Scan(&st.Workspaces, &st.Projects, &st.Sections, &st.Tasks, &st.OpenTasks); err != nil {
		return st, err
	}
	v, err := s.SyncState(ctx, KeyLastSync)
	if err != nil {
		return st, err
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		st.LastSync = &t
	}
	return st, nil
}

const KeyLastSync = "last_sync"
