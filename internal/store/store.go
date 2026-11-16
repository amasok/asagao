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
	"strconv"
	"time"

	"modernc.org/sqlite"
)

type Store struct {
	db   *sql.DB
	path string // the database file (the push lock file lives next to it)
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

	// _txlock=immediate: a transaction begun for writing takes the write lock at BEGIN,
	// waiting for it under busy_timeout. Begun deferred, it took the lock only at its first
	// write, and when another connection (another process: TUI, MCP server, CLI sync) had
	// written since it began, SQLite failed that write at once as busy, without waiting
	// (#50). Reads outside a transaction never take the write lock; a read-only
	// transaction, if one is ever needed, should be begun with sql.TxOptions{ReadOnly: true},
	// which the driver begins deferred, so that it neither waits for writers nor holds them up.
	dsn := "file:" + (&url.URL{Path: path}).EscapedPath() +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(" + strconv.Itoa(int(busyTimeout/time.Millisecond)) + ")&_pragma=foreign_keys(1)&_txlock=immediate"
	db := sql.OpenDB(safeConnector{dsn: dsn, drv: &sqlite.Driver{}})
	s := &Store{db: db, path: path}
	if err := s.migrateRetry(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate %s: %w", path, err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Path is the database file.
func (s *Store) Path() string { return s.path }

// busyTimeout is the busy_timeout set in Open's DSN.
const busyTimeout = 5 * time.Second

// migrateRetry migrates, trying again while it fails as busy, for up to busyTimeout. When
// several processes open a new file at the same moment, setting journal_mode=WAL on a
// connection can fail as busy without waiting for busy_timeout. migrate is safe to run
// again: it reads the version under the write lock.
func (s *Store) migrateRetry(ctx context.Context) error {
	deadline := time.Now().Add(busyTimeout)
	for {
		err := s.migrate(ctx)
		if !isBusy(err) || time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func (s *Store) migrate(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version >= len(migrations) {
		return nil // up to date: no need for the write lock
	}
	for {
		// The version is read under the write lock: another process opening the same file
		// at the same time may have applied the migration while this one waited for it.
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		var i int
		if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&i); err != nil {
			tx.Rollback()
			return err
		}
		if i >= len(migrations) {
			return tx.Rollback()
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
}

// tx runs fn in a write transaction. It holds the write lock from the start (see Open), so
// other writers wait for it: keep fn short, with no network calls in it.
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
