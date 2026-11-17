package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
)

// manyTasks opens a store with n tasks; every task has a tag and a project.
func manyTasks(t *testing.T, n int) *Store {
	t.Helper()
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	tasks := make([]asana.Task, n)
	for i := range tasks {
		tasks[i] = asana.Task{GID: fmt.Sprint(100000 + i), Name: fmt.Sprintf("task %05d", i),
			ModifiedAt:  time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
			Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1", Name: "P"}}},
			Tags:        []asana.Ref{{GID: "g1", Name: "タグ"}}}
	}
	if err := s.UpsertTasks(ctx, tasks); err != nil {
		t.Fatal(err)
	}
	return s
}

// Listing more tasks than one query may name (no limit, as the TUI and CLI do) still loads
// the tags and projects of every task. (#41)
func TestListTasksManyTasksFillsRelations(t *testing.T) {
	const n = 2*relationBatch + 7
	s := manyTasks(t, n)
	tasks, err := s.ListTasks(context.Background(), TaskFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != n {
		t.Fatalf("got %d tasks, want %d", len(tasks), n)
	}
	for _, tk := range tasks {
		if len(tk.Tags) != 1 || len(tk.Memberships) != 1 {
			t.Fatalf("task %s: tags %v memberships %v", tk.GID, tk.Tags, tk.Memberships)
		}
	}
}

// A huge modified_within_days means "all" instead of overflowing the date. (#41)
func TestModifiedWithinDaysIsCapped(t *testing.T) {
	s := manyTasks(t, 3)
	now := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	for _, days := range []int{MaxModifiedWithinDays + 1, 1 << 40, int(^uint(0) >> 1)} {
		f, err := s.BuildFilter(context.Background(), FilterSpec{ModifiedWithinDays: days}, now)
		if err != nil {
			t.Fatal(err)
		}
		if want := now.AddDate(0, 0, -MaxModifiedWithinDays).Format(time.RFC3339); f.ModifiedAfter != want {
			t.Fatalf("days %d: ModifiedAfter = %q, want %q", days, f.ModifiedAfter, want)
		}
		tasks, err := s.ListTasks(context.Background(), f)
		if err != nil || len(tasks) != 3 {
			t.Fatalf("days %d: %d tasks, %v", days, len(tasks), err)
		}
	}
}
