package store

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
)

// Migrating twice, or with odd rows (NULL-ish, empty, lookalike text, other reasons),
// rewrites only the exact legacy text and never touches other columns.
func TestMigrateResetReasonAttack(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "store.db")
	st, err := Open(ctx, path)
	must(t, err)
	reasons := []string{"", " " + legacyResetReason, legacyResetReason + " ", "reset", "RESET", "Reset", legacyResetReason, "ローカルのデータを初期化したため"}
	for _, r := range reasons {
		_, err := st.db.Exec(`INSERT INTO discarded_changes(kind, task_gid, task_name, summary, reason, discarded_at, payload)
			VALUES('add_comment', 't1', 't', ?, ?, '2026-11-01T00:00:00Z', '{"a":1}')`, "S:"+r, r)
		must(t, err)
	}
	rollbackTo(t, st.db, len(migrations)-1)
	st.Close()
	for round := 0; round < 3; round++ {
		if round > 0 {
			st, err = Open(ctx, path)
			must(t, err)
			rollbackTo(t, st.db, len(migrations)-1) // migrate again over the migrated rows
			st.Close()
		}
		st, err = Open(ctx, path)
		must(t, err)
		rows, err := st.db.Query(`SELECT reason, summary FROM discarded_changes ORDER BY id`)
		must(t, err)
		var i int
		for rows.Next() {
			var reason, summary string
			must(t, rows.Scan(&reason, &summary))
			want := reasons[i]
			if want == legacyResetReason {
				want = "reset"
			}
			if reason != want || summary != "S:"+reasons[i] {
				t.Errorf("round %d row %d: reason %q summary %q, want %q", round, i, reason, summary, want)
			}
			i++
		}
		rows.Close()
		if i != len(reasons) {
			t.Fatalf("rows %d", i)
		}
		st.Close()
	}
}

// Only the exact legacy text becomes a reset; IsReset is exact too.
func TestIsResetExact(t *testing.T) {
	s := newTestStore(t)
	for _, r := range []string{"Reset", "reset ", legacyResetReason, ""} {
		_, err := s.db.Exec(`INSERT INTO outbox(kind, task_gid, payload, base, base_modified_at, state, conflict, created_at)
			VALUES('add_comment', 't1', '{}', '{}', '', 'pending', '{}', '')`)
		must(t, err)
		if _, err := s.DiscardGoneOp(context.Background(), lastOutboxID(t, s), r, now); err != nil {
			t.Fatal(err)
		}
	}
	ds, err := s.PendingDiscards(context.Background())
	must(t, err)
	for _, d := range ds {
		if d.IsReset() {
			t.Errorf("reason %q taken as reset", d.Reason)
		}
	}
}

func lastOutboxID(t *testing.T, s *Store) int64 {
	t.Helper()
	var id int64
	must(t, s.db.QueryRow(`SELECT MAX(id) FROM outbox`).Scan(&id))
	return id
}

// Discards recorded concurrently with a describer set beforehand (race detector).
func TestDescribeChangeConcurrent(t *testing.T) {
	s := newTestStore(t)
	s.SetDescribeChange(func(op Op) string { return "S:" + string(op.Kind) })
	ctx := context.Background()
	var ids []int64
	for i := 0; i < 8; i++ {
		_, err := s.db.Exec(`INSERT INTO outbox(kind, task_gid, payload, base, base_modified_at, state, conflict, created_at)
			VALUES('add_comment', 't1', '{}', '{}', '', 'pending', '{}', '')`)
		must(t, err)
		ids = append(ids, lastOutboxID(t, s))
	}
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = s.DiscardGoneOp(ctx, id, "gone", now)
		}()
	}
	wg.Wait()
	ds, err := s.PendingDiscards(ctx)
	must(t, err)
	if len(ds) != 8 {
		t.Fatalf("got %d discards", len(ds))
	}
	for _, d := range ds {
		if d.Summary != "S:add_comment" {
			t.Errorf("summary %q", d.Summary)
		}
	}
}
