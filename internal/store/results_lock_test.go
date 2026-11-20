package store

import (
	"context"
	"testing"
	"time"
)

// A prune that finds nothing to change does not take the write lock (#55): while another
// process holds it, the prune returns at once instead of waiting for busy_timeout.
func TestPruneResultsWithNothingToChangeTakesNoWriteLock(t *testing.T) {
	st := resultsStore(t)
	now := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	mustSet(t, st, resultPrefixTask+"local:a", "1200")
	prune(t, st, now) // stamped: within the grace, the next prune changes nothing

	release := holdWriteLock(t, st)
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	n, err := st.PruneResults(ctx, now.Add(time.Hour))
	if err != nil || n != 0 {
		t.Fatalf("PruneResults = %d, %v", n, err)
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("PruneResults waited %v for the write lock with nothing to change", d)
	}
	release()
	if got := syncStateOf(t, st, keyResultSeen+resultPrefixTask+"local:a"); got != now.Format(time.RFC3339) {
		t.Fatalf("stamp = %q, want it unchanged", got)
	}
}

// What is to change is read again under the write lock: an outbox entry queued by another
// process after the read-only pass, referring to a mapping that pass found deletable, keeps
// the mapping.
func TestPruneResultsReadsAgainUnderTheWriteLock(t *testing.T) {
	ctx := context.Background()
	st := resultsStore(t)
	now := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	mustSet(t, st, resultPrefixTask+"local:a", "1200")
	prune(t, st, now)

	pruneResultsPlanned = func() {
		if _, err := st.db.ExecContext(ctx, `INSERT INTO outbox(kind, task_gid, payload, created_at) VALUES (?, 'local:a', '{"completed":true}', ?)`,
			OpUpdateTask, now.Format(time.RFC3339)); err != nil {
			t.Error(err)
		}
	}
	defer func() { pruneResultsPlanned = nil }()
	if n := prune(t, st, now.Add(ResultGrace)); n != 0 {
		t.Fatalf("deleted %d mappings, one still referred to", n)
	}
	if syncStateOf(t, st, resultPrefixTask+"local:a") != "1200" {
		t.Fatal("the mapping of local:a was deleted while an outbox entry refers to it")
	}
	if syncStateOf(t, st, keyResultSeen+resultPrefixTask+"local:a") != "" {
		t.Fatal("the grace should start over while the mapping is needed")
	}
}
