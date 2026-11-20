package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
)

func openRawSQLite(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestOpenNewerSchemaFailsFast(t *testing.T) {
	ctx := context.Background()
	for _, delta := range []int{1, 10} {
		t.Run(fmt.Sprintf("plus_%d", delta), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "store.db")
			st, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			if err := st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "既存タスク"}}); err != nil {
				t.Fatal(err)
			}
			if err := st.SetSyncState(ctx, "marker", "keep-me"); err != nil {
				t.Fatal(err)
			}
			future := len(migrations) + delta
			if _, err := st.db.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", future)); err != nil {
				t.Fatal(err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}

			start := time.Now()
			got, err := Open(ctx, path)
			elapsed := time.Since(start)
			if got != nil {
				got.Close()
				t.Fatalf("Open succeeded on newer schema version %d", future)
			}
			if !errors.Is(err, ErrNewerSchema) {
				t.Fatalf("err = %v, want errors.Is(_, ErrNewerSchema)", err)
			}
			if !strings.Contains(err.Error(), strconv.Itoa(future)) || !strings.Contains(err.Error(), strconv.Itoa(len(migrations))) {
				t.Fatalf("err = %q, want both db version %d and supported %d", err.Error(), future, len(migrations))
			}
			if elapsed >= 2*time.Second {
				t.Fatalf("Open took %v, want fast failure without 5s busy retry", elapsed)
			}

			// Even if another connection holds the write lock, Open must fail fast without waiting for busyTimeout.
			holder := otherProcess(t, path, 0)
			if _, err := holder.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
				t.Fatal(err)
			}
			startLocked := time.Now()
			gotLocked, errLocked := Open(ctx, path)
			elapsedLocked := time.Since(startLocked)
			_, _ = holder.ExecContext(ctx, "ROLLBACK")
			if gotLocked != nil {
				gotLocked.Close()
				t.Fatalf("Open succeeded while locked on newer schema version %d", future)
			}
			if !errors.Is(errLocked, ErrNewerSchema) {
				t.Fatalf("locked err = %v, want errors.Is(_, ErrNewerSchema)", errLocked)
			}
			if elapsedLocked >= 2*time.Second {
				t.Fatalf("locked Open took %v, want fast failure without 5s busy wait", elapsedLocked)
			}

			raw := openRawSQLite(t, path)
			var ver int
			if err := raw.QueryRowContext(ctx, "PRAGMA user_version").Scan(&ver); err != nil {
				t.Fatal(err)
			}
			if ver != future {
				t.Fatalf("PRAGMA user_version = %d, want preserved %d", ver, future)
			}
			var taskName, marker string
			if err := raw.QueryRowContext(ctx, "SELECT name FROM tasks WHERE gid = 't1'").Scan(&taskName); err != nil {
				t.Fatal(err)
			}
			if taskName != "既存タスク" {
				t.Fatalf("task name = %q, want 既存タスク", taskName)
			}
			if err := raw.QueryRowContext(ctx, "SELECT value FROM sync_state WHERE key = 'marker'").Scan(&marker); err != nil {
				t.Fatal(err)
			}
			if marker != "keep-me" {
				t.Fatalf("sync_state marker = %q, want keep-me", marker)
			}
		})
	}
}

func TestOpenNegativeSchemaVersionFails(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "store.db")
	raw := openRawSQLite(t, path)
	if _, err := raw.ExecContext(ctx, "CREATE TABLE sentinel (v TEXT); INSERT INTO sentinel (v) VALUES ('keep'); PRAGMA user_version = -1;"); err != nil {
		t.Fatal(err)
	}

	got, err := Open(ctx, path)
	if got != nil {
		got.Close()
		t.Fatal("Open succeeded on negative schema version")
	}
	if err == nil || errors.Is(err, ErrNewerSchema) || !strings.Contains(err.Error(), "invalid schema version -1") {
		t.Fatalf("err = %v, want invalid schema version -1", err)
	}

	var ver int
	if err := raw.QueryRowContext(ctx, "PRAGMA user_version").Scan(&ver); err != nil {
		t.Fatal(err)
	}
	if ver != -1 {
		t.Fatalf("PRAGMA user_version = %d, want -1", ver)
	}
}

type raceMigrator struct {
	db      *sql.DB
	onBegin func() error
}

func (m *raceMigrator) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return m.db.QueryRowContext(ctx, query, args...)
}

func (m *raceMigrator) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	if m.onBegin != nil {
		if err := m.onBegin(); err != nil {
			return nil, err
		}
	}
	return m.db.BeginTx(ctx, opts)
}

func TestMigrateRaceNewerSchemaRollsBack(t *testing.T) {
	ctx := context.Background()
	future := len(migrations) + 1

	t.Run("from_version_0", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "store.db")
		db := openRawSQLite(t, path)
		if _, err := db.ExecContext(ctx, "CREATE TABLE sentinel (v TEXT); INSERT INTO sentinel (v) VALUES ('untouched');"); err != nil {
			t.Fatal(err)
		}

		m := &raceMigrator{
			db: db,
			onBegin: func() error {
				_, err := db.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", future))
				return err
			},
		}
		err := migrate(ctx, m)
		if !errors.Is(err, ErrNewerSchema) {
			t.Fatalf("err = %v, want errors.Is(_, ErrNewerSchema)", err)
		}
		if !strings.Contains(err.Error(), strconv.Itoa(future)) || !strings.Contains(err.Error(), strconv.Itoa(len(migrations))) {
			t.Fatalf("err = %q, want both db version %d and supported %d", err.Error(), future, len(migrations))
		}

		// No migration ran (workspaces table must not exist), and rollback left DB writable.
		var count int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'workspaces'").Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("workspaces table was created despite newer schema version")
		}
		var v string
		if err := db.QueryRowContext(ctx, "SELECT v FROM sentinel").Scan(&v); err != nil || v != "untouched" {
			t.Fatalf("sentinel = %q, %v", v, err)
		}
		if _, err := db.ExecContext(ctx, "INSERT INTO sentinel (v) VALUES ('after')"); err != nil {
			t.Fatalf("write after rollback failed (tx left open?): %v", err)
		}
	})

	t.Run("from_penultimate_version", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "store.db")
		st, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		rollbackTo(t, st.db, len(migrations)-1)
		db := st.db
		defer st.Close()

		m := &raceMigrator{
			db: db,
			onBegin: func() error {
				_, err := db.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", future))
				return err
			},
		}
		err = migrate(ctx, m)
		if !errors.Is(err, ErrNewerSchema) {
			t.Fatalf("err = %v, want errors.Is(_, ErrNewerSchema)", err)
		}
		// Last migration adds tags.workspace_gid; verify it was NOT run.
		var hasCol int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM pragma_table_info('tags') WHERE name = 'workspace_gid'").Scan(&hasCol); err != nil {
			t.Fatal(err)
		}
		if hasCol != 0 {
			t.Fatalf("last migration ran despite newer schema version inside BeginTx")
		}
		var ver int
		if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&ver); err != nil {
			t.Fatal(err)
		}
		if ver != future {
			t.Fatalf("PRAGMA user_version = %d, want %d", ver, future)
		}
	})

	t.Run("negative_inside_tx", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "store.db")
		db := openRawSQLite(t, path)
		m := &raceMigrator{
			db: db,
			onBegin: func() error {
				_, err := db.ExecContext(ctx, "PRAGMA user_version = -1")
				return err
			},
		}
		err := migrate(ctx, m)
		if err == nil || errors.Is(err, ErrNewerSchema) || !strings.Contains(err.Error(), "invalid schema version -1") {
			t.Fatalf("err = %v, want invalid schema version -1", err)
		}
		if _, err := db.ExecContext(ctx, "CREATE TABLE ok (x INT)"); err != nil {
			t.Fatalf("write after rollback failed: %v", err)
		}
	})
}

func TestOpenSupportedSchemaSucceeds(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "store.db")
	st, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "既存タスク"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	st2, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen at supported schema failed: %v", err)
	}
	defer st2.Close()

	var ver int
	if err := st2.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&ver); err != nil {
		t.Fatal(err)
	}
	if ver != len(migrations) {
		t.Fatalf("PRAGMA user_version = %d, want %d", ver, len(migrations))
	}
	d, err := st2.GetTask(ctx, "t1")
	if err != nil || d.Name != "既存タスク" {
		t.Fatalf("task = %+v, %v", d, err)
	}
}
