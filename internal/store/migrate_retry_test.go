package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

// busyErr returns a real SQLITE_BUSY error: a write lock taken while another connection
// holds it, without waiting.
func busyErr(t *testing.T) error {
	t.Helper()
	ctx := context.Background()
	a, _ := openPair(t)
	release := holdWriteLock(t, a)
	defer release()
	conn, err := a.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "PRAGMA busy_timeout = 0"); err != nil {
		t.Fatal(err)
	}
	_, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE")
	if !isBusy(err) {
		t.Fatalf("want a busy error, got %v", err)
	}
	return err
}

// Cancelled while retrying a busy migration, the error says so (#54): it is the context's
// error, with the busy error kept.
func TestRetryBusyReportsCancel(t *testing.T) {
	busy := busyErr(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tries := 0
	err := retryBusy(ctx, time.Minute, func(context.Context, time.Duration) error {
		tries++
		if tries == 3 {
			cancel()
		}
		return busy
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel not reported: %v", err)
	}
	if !isBusy(err) {
		t.Fatalf("busy cause lost: %v", err)
	}
	if tries != 3 {
		t.Fatalf("tried %d times, want to stop right after the cancel (3)", tries)
	}
}

// The tries together wait no longer than the budget (#54): a try that starts near the end
// may only wait for what is left, not a whole busy_timeout again (which made the worst case
// about twice busy_timeout). Each try's wait here stands for busy_timeout: it sleeps for it.
func TestRetryBusyStaysWithinBudget(t *testing.T) {
	busy := busyErr(t)
	const budget = 600 * time.Millisecond
	t0 := time.Now()
	var waits []time.Duration
	err := retryBusy(context.Background(), budget, func(_ context.Context, wait time.Duration) error {
		waits = append(waits, wait)
		if time.Since(t0) < budget/2 {
			return busy // fails at once, like setting journal_mode=WAL
		}
		time.Sleep(wait) // waits for the lock for as long as it is allowed to
		return busy
	})
	d := time.Since(t0)
	if !isBusy(err) {
		t.Fatalf("want the busy error, got %v", err)
	}
	if d > budget+200*time.Millisecond {
		t.Fatalf("gave up after %v, want about the budget %v (waits: %v)", d, budget, waits)
	}
	for i, w := range waits {
		if w > budget {
			t.Fatalf("try %d may wait %v, more than the budget %v", i, w, budget)
		}
	}
	if len(waits) < 2 {
		t.Fatalf("tried %d times, want retries while busy", len(waits))
	}
}

// A try that may wait only briefly gives up as busy soon, and the connection it used goes
// back to the pool with the normal busy_timeout.
func TestMigrateWithinWaitsOnlyAsAllowed(t *testing.T) {
	ctx := context.Background()
	a, _ := openPair(t)
	a.db.SetMaxOpenConns(2) // the lock holder and one more: the try's connection is reused below
	rollbackTo(t, a.db, len(migrations)-1)
	release := holdWriteLock(t, a)
	t0 := time.Now()
	err := a.migrateWithin(ctx, 200*time.Millisecond)
	d := time.Since(t0)
	release()
	if !isBusy(err) {
		t.Fatalf("want busy while the lock is held, got %v", err)
	}
	if d > 2*time.Second {
		t.Fatalf("waited %v, want about 200ms (not busy_timeout %v)", d, busyTimeout)
	}
	var conns []*sql.Conn
	for i := range 2 { // both pooled connections: the lock holder's and the try's
		conn, err := a.db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, conn)
		var ms int64
		if err := conn.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&ms); err != nil {
			t.Fatal(err)
		}
		if ms != busyTimeout.Milliseconds() {
			t.Fatalf("pooled connection %d keeps busy_timeout %dms, want %dms", i, ms, busyTimeout.Milliseconds())
		}
	}
	for _, c := range conns {
		c.Close()
	}
	if err := a.migrateRetry(ctx); err != nil {
		t.Fatalf("migrate after release: %v", err)
	}
}
