package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

const (
	envChildPath = "STORE_BUSY_ATTACK_PATH"
	envChildID   = "STORE_BUSY_ATTACK_ID"
	childWrites  = 40
)

// TestBusyAttackChild is the body of a child process, not a test of its own: it opens the
// store at $STORE_BUSY_ATTACK_PATH and writes. The parent launches it with exec so that the
// writers are real separate processes (separate file locks), not goroutines.
func TestBusyAttackChild(t *testing.T) {
	path := os.Getenv(envChildPath)
	if path == "" {
		t.Skip("child helper; run by the parent tests")
	}
	id := os.Getenv(envChildID)
	ctx := context.Background()
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("child %s: open: %v", id, err)
	}
	defer s.Close()
	for i := 0; i < childWrites; i++ {
		if err := s.SetSyncState(ctx, fmt.Sprintf("c%s-%d", id, i), "v"); err != nil {
			t.Fatalf("child %s: write %d: %v", id, i, err)
		}
		if _, _, _, err := s.OutboxCounts(ctx); err != nil {
			t.Fatalf("child %s: read %d: %v", id, i, err)
		}
	}
}

func runChildren(t *testing.T, path string, n int) {
	t.Helper()
	var wg sync.WaitGroup
	outs := make([][]byte, n)
	errs := make([]error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestBusyAttackChild$", "-test.count=1")
		cmd.Env = append(os.Environ(), envChildPath+"="+path, envChildID+"="+strconv.Itoa(i))
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			outs[i], errs[i] = cmd.CombinedOutput()
		}(i)
	}
	close(start)
	wg.Wait()
	for i := range errs {
		if errs[i] != nil {
			t.Errorf("child %d failed: %v\n%s", i, errs[i], outs[i])
		}
	}
}

// Several real processes open a brand-new file at the same moment and write at once: none
// may fail as busy, the migrations are applied exactly once, and no write is lost.
func TestBusyAttackProcessesOpenFreshAndWrite(t *testing.T) {
	const n = 6
	path := filepath.Join(t.TempDir(), "store.db")
	runChildren(t, path, n)

	ctx := context.Background()
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != len(migrations) {
		t.Fatalf("user_version = %d, want %d", version, len(migrations))
	}
	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM sync_state WHERE key LIKE 'c%-%'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != n*childWrites {
		t.Fatalf("rows written = %d, want %d (a write was lost)", count, n*childWrites)
	}
}

// Writers in other processes keep going while this process holds the push lock, and the
// lock does not deadlock with the database's write lock.
func TestBusyAttackPushLockWithWritersInOtherProcesses(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "store.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	unlock, err := s.TryLockPush()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		runChildren(t, path, 3)
	}()
	for i := 0; i < 50; i++ { // write while holding the push lock
		if err := s.SetSyncState(ctx, fmt.Sprintf("p%d", i), "v"); err != nil {
			t.Errorf("write under push lock: %v", err)
		}
		if _, err := s.TryLockPush(); err == nil {
			t.Error("second TryLockPush succeeded while held")
		}
	}
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("deadlock: children did not finish")
	}
	unlock()
	unlock2, err := s.TryLockPush()
	if err != nil {
		t.Fatalf("lock not released: %v", err)
	}
	unlock2()
}

// An open SELECT (rows not closed) must not stop another connection from writing, and a
// write transaction that is sitting open must not stop reads (WAL).
func TestBusyAttackReadersAndWritersDoNotBlockEachOther(t *testing.T) {
	ctx := context.Background()
	a, b := openPair(t)
	if err := a.SetSyncState(ctx, "seed", "1"); err != nil {
		t.Fatal(err)
	}

	rows, err := a.db.QueryContext(ctx, "SELECT key FROM sync_state")
	if err != nil {
		t.Fatal(err)
	}
	if !rows.Next() {
		t.Fatal("no rows")
	}
	wrote := make(chan error, 1)
	go func() { wrote <- b.SetSyncState(ctx, "while-reading", "1") }()
	select {
	case err := <-wrote:
		if err != nil {
			t.Fatalf("write while another connection holds rows open: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("writer blocked by an open reader")
	}
	rows.Close()

	// A writer holding the lock: reads on the other connection still go through.
	inTx := make(chan struct{})
	release := make(chan struct{})
	txErr := make(chan error, 1)
	go func() {
		txErr <- a.tx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, "INSERT INTO sync_state(key, value) VALUES ('held', '1')"); err != nil {
				return err
			}
			close(inTx)
			<-release
			return nil
		})
	}()
	<-inTx
	readDone := make(chan error, 1)
	go func() {
		_, err := b.SyncState(ctx, "seed")
		if err == nil {
			_, _, _, err = b.OutboxCounts(ctx)
		}
		readDone <- err
	}()
	select {
	case err := <-readDone:
		if err != nil {
			t.Fatalf("read while another connection writes: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("reader blocked by an open write transaction")
	}
	close(release)
	if err := <-txErr; err != nil {
		t.Fatal(err)
	}
}

// holdWriteLock takes the write lock from a raw second connection and returns a release func.
func holdWriteLock(t *testing.T, s *Store) func() {
	t.Helper()
	ctx := context.Background()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	return func() {
		conn.ExecContext(ctx, "ROLLBACK")
		conn.Close()
	}
}

// When the wait is cancelled through the context, the writer stops waiting promptly and
// reports the error; the write is not applied.
func TestBusyAttackContextCancelStopsWait(t *testing.T) {
	a, b := openPair(t)
	release := holdWriteLock(t, a)
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	t0 := time.Now()
	err := b.SetSyncState(ctx, "cancelled", "1")
	if err == nil {
		t.Fatal("write succeeded while the write lock was held")
	}
	if d := time.Since(t0); d > 3*time.Second {
		t.Fatalf("cancel took %v, want well under busy_timeout", d)
	}
	release()
	if v, _ := b.SyncState(context.Background(), "cancelled"); v != "" {
		t.Fatalf("cancelled write was applied: %q", v)
	}
}

// Past busy_timeout the writer gets an error (not a hang, not a silent success), and once the
// lock is released the same write goes through, so a retry does not lose the user's change.
func TestBusyAttackTimeoutReturnsErrorThenRetrySucceeds(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for busy_timeout")
	}
	ctx := context.Background()
	a, b := openPair(t)
	release := holdWriteLock(t, a)
	defer release()

	t0 := time.Now()
	err := b.SetSyncState(ctx, "late", "1")
	d := time.Since(t0)
	if err == nil {
		t.Fatal("write succeeded while the write lock was held")
	}
	if d < busyTimeout-time.Second {
		t.Fatalf("gave up after %v, before busy_timeout %v", d, busyTimeout)
	}
	if d > busyTimeout+10*time.Second {
		t.Fatalf("waited %v, far past busy_timeout", d)
	}
	if v, _ := b.SyncState(ctx, "late"); v != "" {
		t.Fatalf("failed write was applied: %q", v)
	}
	release()
	if err := b.SetSyncState(ctx, "late", "1"); err != nil {
		t.Fatalf("retry after release: %v", err)
	}
}

// A writer that waits shorter than busy_timeout gets in once the lock is released.
func TestBusyAttackWriterWaitsForLongTransaction(t *testing.T) {
	ctx := context.Background()
	a, b := openPair(t)
	release := holdWriteLock(t, a)
	errc := make(chan error, 1)
	go func() { errc <- b.SetSyncState(ctx, "waited", "1") }()
	time.Sleep(1500 * time.Millisecond)
	select {
	case err := <-errc:
		t.Fatalf("write returned while lock held: %v", err)
	default:
	}
	release()
	if err := <-errc; err != nil {
		t.Fatalf("waiting writer failed: %v", err)
	}
}

// Many goroutines over two stores mixing writes, reads and read-then-write transactions:
// nothing may fail. Meant for -race.
func TestBusyAttackMixedLoad(t *testing.T) {
	ctx := context.Background()
	a, b := openPair(t)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		s := a
		if g%2 == 1 {
			s = b
		}
		wg.Add(1)
		go func(g int, s *Store) {
			defer wg.Done()
			for i := 0; i < 30; i++ {
				key := fmt.Sprintf("m%d-%d", g, i)
				var err error
				switch i % 3 {
				case 0:
					err = s.SetSyncState(ctx, key, "v")
				case 1:
					_, _, _, err = s.OutboxCounts(ctx)
				default:
					err = s.tx(ctx, func(tx *sql.Tx) error {
						var n int
						if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM sync_state").Scan(&n); err != nil {
							return err
						}
						_, err := tx.ExecContext(ctx, "INSERT INTO sync_state(key, value) VALUES (?, ?)", key, "v")
						return err
					})
				}
				if err != nil {
					t.Errorf("g%d i%d: %v", g, i, err)
					return
				}
			}
		}(g, s)
	}
	wg.Wait()
}
