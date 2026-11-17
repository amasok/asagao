package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
)

// bulkTasks builds n tasks of one project, each with a section, a tag and an assignee, as a
// pull of a large project returns them.
func bulkTasks(n int, prefix string) []asana.Task {
	tasks := make([]asana.Task, n)
	for i := range tasks {
		tasks[i] = asana.Task{
			GID: fmt.Sprintf("%s%d", prefix, i), Name: fmt.Sprintf("タスク %d", i), Notes: "メモ",
			Assignee:    &asana.Ref{GID: "u1", Name: "自分"},
			Workspace:   &asana.Ref{GID: "w1"},
			Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1", Name: "大きなプロジェクト"}, Section: &asana.Ref{GID: "s1", Name: "未着手"}}},
			Tags:        []asana.Ref{{GID: "tag1", Name: "重要"}},
			ModifiedAt:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		}
	}
	return tasks
}

// otherProcess opens path as another process would, with its own busy_timeout.
func otherProcess(t *testing.T, path string, busy time.Duration) *sql.DB {
	t.Helper()
	dsn := "file:" + (&url.URL{Path: path}).EscapedPath() +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(" + strconv.Itoa(int(busy/time.Millisecond)) + ")&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// While a pull writes a large project, another process's write must go through within its
// busy_timeout (#55). Written in one transaction, the tasks held the write lock for longer
// than that, and the other write failed as "database is locked". The other process here
// waits at most otherBusy, shorter than the real busy_timeout so that the test needs fewer
// tasks; the pull's transactions must stay well within it.
func TestUpsertTasksLetsOtherWritersIn(t *testing.T) {
	if testing.Short() {
		t.Skip("writes many tasks")
	}
	const otherBusy = 1500 * time.Millisecond
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "store.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	other := otherProcess(t, path, otherBusy)
	if _, err := other.ExecContext(ctx, "SELECT 1"); err != nil {
		t.Fatal(err)
	}

	// Enough tasks that writing them takes about three times otherBusy, measured on a
	// sample (this machine, -race or not).
	const sample = 300
	start := time.Now()
	if err := s.UpsertTasks(ctx, bulkTasks(sample, "sample")); err != nil {
		t.Fatal(err)
	}
	n := min(int(3*otherBusy*sample/max(time.Since(start), time.Millisecond)), 100000)
	tasks := bulkTasks(n, "t")

	done := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	var worst time.Duration
	var writes int
	var otherErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-done:
				return
			case <-time.After(20 * time.Millisecond):
			}
			start := time.Now()
			_, err := other.ExecContext(ctx, "INSERT OR REPLACE INTO sync_state(key, value) VALUES ('other', ?)", strconv.Itoa(i))
			d := time.Since(start)
			mu.Lock()
			worst = max(worst, d)
			writes++
			if err != nil && otherErr == nil {
				otherErr = fmt.Errorf("write %d after %v: %w", i, d, err)
			}
			mu.Unlock()
		}
	}()
	start = time.Now()
	err = s.UpsertTasks(ctx, tasks)
	took := time.Since(start)
	close(done)
	wg.Wait()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d tasks in %v; other process: %d writes, longest wait %v", n, took, writes, worst)
	if otherErr != nil {
		t.Fatalf("the other process's write failed while %d tasks were written: %v", n, otherErr)
	}
	if writes < 2 {
		t.Fatalf("the other process wrote %d times in %v", writes, took)
	}
	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM tasks WHERE gid LIKE 't%'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != n {
		t.Fatalf("%d of %d tasks stored", count, n)
	}
}

// setWriteSlices makes UpsertTasks write one task per transaction and wait yield between
// them, until the test ends.
func setWriteSlices(t *testing.T, yield time.Duration) {
	t.Cleanup(SetTaskWriteSlicesForTest(0, yield))
}

// storedTasks is the gids of the stored tasks, with how many memberships and tags each has.
func storedTasks(t *testing.T, s *Store) map[string][2]int {
	t.Helper()
	rows, err := s.db.QueryContext(context.Background(), `SELECT gid,
		(SELECT count(*) FROM task_memberships WHERE task_gid = tasks.gid),
		(SELECT count(*) FROM task_tags WHERE task_gid = tasks.gid) FROM tasks`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string][2]int{}
	for rows.Next() {
		var gid string
		var m, tg int
		if err := rows.Scan(&gid, &m, &tg); err != nil {
			t.Fatal(err)
		}
		got[gid] = [2]int{m, tg}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return got
}

// Written in several transactions, a failure part way keeps what was written before it
// (each task whole, with its memberships and tags) and returns the error; written again,
// the tasks end up as if written at once.
func TestUpsertTasksFailurePartWay(t *testing.T) {
	ctx := context.Background()
	setWriteSlices(t, time.Millisecond)
	s, err := Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tasks := bulkTasks(10, "t")
	if _, err := s.db.ExecContext(ctx, `CREATE TRIGGER fail_t6 BEFORE INSERT ON task_tags WHEN NEW.task_gid = 't6'
		BEGIN SELECT RAISE(ABORT, 'injected'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertTasks(ctx, tasks); err == nil {
		t.Fatal("UpsertTasks succeeded through the failing task")
	}
	got := storedTasks(t, s)
	for i, tk := range tasks {
		c, ok := got[tk.GID]
		switch {
		case i < 6 && (!ok || c != [2]int{1, 1}):
			t.Errorf("%s written before the failure: stored %v %v, want whole", tk.GID, ok, c)
		case i >= 6 && ok:
			t.Errorf("%s stored (%v), though the write failed at t6", tk.GID, c)
		}
	}
	if _, err := s.db.ExecContext(ctx, "DROP TRIGGER fail_t6"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertTasks(ctx, tasks); err != nil {
		t.Fatal(err)
	}
	got = storedTasks(t, s)
	if len(got) != len(tasks) {
		t.Fatalf("%d tasks stored after writing again, want %d", len(got), len(tasks))
	}
	for gid, c := range got {
		if c != [2]int{1, 1} {
			t.Errorf("%s: %v memberships and tags after writing again", gid, c)
		}
	}
}

// Cancelled between two transactions, UpsertTasks stops at once with the context's error.
func TestUpsertTasksCancelledBetweenTransactions(t *testing.T) {
	setWriteSlices(t, time.Minute)
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	start := time.Now()
	err = s.UpsertTasks(ctx, bulkTasks(3, "t"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("UpsertTasks = %v, want context.Canceled", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("UpsertTasks took %v after the cancel", d)
	}
	if got := storedTasks(t, s); len(got) != 1 || got["t0"] != [2]int{1, 1} {
		t.Fatalf("stored %v, want only t0 (written before the cancel)", got)
	}
}
