package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
)

// openPair opens the same database file twice, as two processes (TUI, MCP server, CLI
// sync) would.
func openPair(t *testing.T) (*Store, *Store) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "store.db")
	a, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	b, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	return a, b
}

// A write transaction that reads first and writes later must not fail as busy because
// another connection wrote in between (#50). Begun deferred, it held only a read snapshot,
// the other write went through, and its own write then failed at once with
// SQLITE_BUSY_SNAPSHOT, without waiting for busy_timeout. Begun immediate, it holds the
// write lock from the start: the other write waits for it and both succeed.
func TestWriteTxReadThenWriteDoesNotFailAsBusy(t *testing.T) {
	ctx := context.Background()
	a, b := openPair(t)

	read := make(chan struct{})
	proceed := make(chan struct{})
	errA := make(chan error, 1)
	go func() {
		errA <- a.tx(ctx, func(tx *sql.Tx) error {
			var n int
			if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM sync_state").Scan(&n); err != nil {
				return err
			}
			close(read)
			<-proceed
			_, err := tx.ExecContext(ctx, "INSERT INTO sync_state(key, value) VALUES ('a', '1')")
			return err
		})
	}()
	<-read

	errB := make(chan error, 1)
	go func() { errB <- b.SetSyncState(ctx, "b", "1") }()
	// Let b's write go through (as it did with deferred transactions) or start waiting.
	select {
	case err := <-errB:
		errB <- err
	case <-time.After(300 * time.Millisecond):
	}
	close(proceed)

	if err := <-errA; err != nil {
		t.Fatalf("read-then-write transaction: %v", err)
	}
	if err := <-errB; err != nil {
		t.Fatalf("other connection's write: %v", err)
	}
	for _, k := range []string{"a", "b"} {
		if v, err := b.SyncState(ctx, k); err != nil || v != "1" {
			t.Fatalf("sync_state[%s] = %q, %v; want 1", k, v, err)
		}
	}
}

// The reproduction from #50: two stores on one file writing at the same time, one with
// read-then-write transactions, the other with single statements and transactions. Every
// write must succeed within busy_timeout.
func TestConcurrentWritersFromTwoStoresDoNotFailAsBusy(t *testing.T) {
	ctx := context.Background()
	a, b := openPair(t)
	const n = 200
	for i := range n {
		if _, err := a.db.ExecContext(ctx, "INSERT INTO tasks(gid, name) VALUES (?, ?)",
			fmt.Sprintf("%s%d", LocalPrefix, i), "t"); err != nil {
			t.Fatal(err)
		}
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2*n)
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := range n {
			if err := a.ReplaceLocalTaskGID(ctx, fmt.Sprintf("%s%d", LocalPrefix, i), fmt.Sprintf("9%d", i)); err != nil {
				errs <- fmt.Errorf("ReplaceLocalTaskGID %d: %w", i, err)
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := range n {
			var err error
			if i%2 == 0 {
				err = b.SetSyncState(ctx, "k", fmt.Sprint(i))
			} else {
				err = b.UpsertWorkspaces(ctx, []asana.Workspace{{GID: "w1", Name: fmt.Sprint(i)}})
			}
			if err != nil {
				errs <- fmt.Errorf("write %d: %w", i, err)
			}
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// Processes opening a new database file at the same time must not apply a migration
// twice ("table already exists") or fail as busy: each reads the version under the write
// lock.
func TestConcurrentOpenMigratesOnce(t *testing.T) {
	ctx := context.Background()
	for round := range 5 {
		path := filepath.Join(t.TempDir(), "store.db")
		const n = 4
		var wg sync.WaitGroup
		errs := make(chan error, n)
		for range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				s, err := Open(ctx, path)
				if err != nil {
					errs <- err
					return
				}
				s.Close()
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Errorf("round %d: Open: %v", round, err)
		}
	}
}
