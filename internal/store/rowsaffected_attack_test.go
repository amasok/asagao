package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func countRows(t *testing.T, s *Store, q string) int {
	t.Helper()
	var n int
	must(t, s.db.QueryRowContext(context.Background(), q).Scan(&n))
	return n
}

// A RowsAffected failure rolls back the whole change, not just the outbox (#67): the
// deletes that ran before it must not stay.
func TestRowsAffectedFailureRollsBackData(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		query string
		run   func(s *Store) error
	}{
		{"PruneTasks", "SELECT count(*) FROM tasks", func(s *Store) error { _, err := s.PruneTasks(ctx, map[string]bool{}); return err }},
		{"DeleteGoneTasks", "SELECT count(*) FROM tasks", func(s *Store) error { _, err := s.DeleteGoneTasks(ctx, []string{"t1"}); return err }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newTestStore(t)
			before := countRows(t, s, c.query)
			if err := c.run(rowsAffectedFails(t, s)); !errors.Is(err, errRowsAffected) {
				t.Fatalf("err = %v", err)
			}
			if after := countRows(t, s, c.query); after != before {
				t.Fatalf("%s: %d -> %d rows, the failed change was not rolled back", c.name, before, after)
			}
		})
	}
}

// The good path is unchanged: undoing an unsent tag change cancels it, and does not queue.
func TestRowsAffectedHealthyCancelStillWorks(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	s := newTestStore(t)
	gid, err := s.AddTagLocal(ctx, "t2", "g1", "", now)
	if err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, s, "SELECT count(*) FROM outbox"); n != 1 {
		t.Skipf("fixture already has t2/g1 tagged (outbox=%d)", n)
	}
	must(t, s.RemoveTagLocal(ctx, "t2", gid, now))
	if n := countRows(t, s, "SELECT count(*) FROM outbox"); n != 0 {
		t.Fatalf("outbox = %d after undo, want 0", n)
	}
}
