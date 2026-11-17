package store

import (
	"context"
	"fmt"
	"testing"
)

func countTasks(t *testing.T, s *Store, where string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRowContext(context.Background(), "SELECT count(*) FROM tasks WHERE "+where).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Edges around the rules: an empty keep set, a keep set with gids that are not cached (a
// returned parent of an orphan), a huge keep set, a second prune, and local tasks.
func TestPruneTasksEdges(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	// t1..t4 are cached (see newTestStore); t1a is t1's subtask.
	total := countTasks(t, s, "1=1")

	// nil keep: everything goes, except what the outbox holds.
	if err := s.UpdateTaskLocal(ctx, "t2", map[string]any{"name": "ローカル"}, testNow); err != nil {
		t.Fatal(err)
	}
	local, err := s.CreateTaskLocal(ctx, NewTask{Name: "offline", WorkspaceGID: "w1"}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	n, err := s.PruneTasks(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if int(n) != total-1 { // the queued t2 stays (the local task was not counted in total)
		t.Errorf("deleted %d of %d", n, total)
	}
	if countTasks(t, s, "gid IN ('t2', '"+local+"')") != 2 {
		t.Errorf("queued tasks must stay")
	}
	// idempotent, and the temp table is gone so a later prune works the same
	if n, err := s.PruneTasks(ctx, map[string]bool{}); err != nil || n != 0 {
		t.Errorf("second prune = %d, %v", n, err)
	}

	// a subtask of a returned parent stays even if the parent is not cached
	s2 := newTestStore(t)
	if _, err := s2.db.ExecContext(ctx, "UPDATE tasks SET parent_gid = 'ghost' WHERE gid = 't1a'"); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.PruneTasks(ctx, map[string]bool{"ghost": true}); err != nil {
		t.Fatal(err)
	}
	if countTasks(t, s2, "gid = 't1a'") != 1 {
		t.Errorf("subtask of a returned (uncached) parent was pruned")
	}
	if countTasks(t, s2, "1=1") != 1 {
		t.Errorf("others must go")
	}

	// a large keep set (past SQLite's variable limit if it were bound at once)
	s3 := newTestStore(t)
	keep := map[string]bool{"t1": true}
	for i := 0; i < 40000; i++ {
		keep[fmt.Sprintf("k%d", i)] = true
	}
	if _, err := s3.PruneTasks(ctx, keep); err != nil {
		t.Fatal(err)
	}
	if countTasks(t, s3, "gid IN ('t1', 't1a')") != 2 || countTasks(t, s3, "1=1") != 2 {
		t.Errorf("t1 and its subtask must stay, nothing else")
	}
}
