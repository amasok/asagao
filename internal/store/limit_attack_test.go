package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/amasok/asana-tui/internal/asana"
)

// #41 (tester): around the batch boundary every task gets its own tags and projects, not
// those of a task in another batch (identical relations would hide a mix-up).
func TestListTasksRelationsStayWithTheirTaskAcrossBatches(t *testing.T) {
	for _, n := range []int{relationBatch - 1, relationBatch, relationBatch + 1, 2*relationBatch + 1} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			ctx := context.Background()
			s, err := Open(ctx, filepath.Join(t.TempDir(), "store.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			tasks := make([]asana.Task, n)
			for i := range tasks {
				tasks[i] = asana.Task{GID: fmt.Sprint(300000 + i), Name: fmt.Sprintf("t%05d", i),
					ModifiedAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
					Memberships: []asana.Membership{
						{Project: &asana.Ref{GID: fmt.Sprint("p", i), Name: "P"}},
						{Project: &asana.Ref{GID: "shared", Name: "S"}},
					},
					Tags: []asana.Ref{{GID: fmt.Sprint("g", i), Name: fmt.Sprint("tag", i)}}}
			}
			if err := s.UpsertTasks(ctx, tasks); err != nil {
				t.Fatal(err)
			}
			got, err := s.ListTasks(ctx, TaskFilter{})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != n {
				t.Fatalf("got %d tasks, want %d", len(got), n)
			}
			for _, tk := range got {
				var i int
				if _, err := fmt.Sscan(tk.GID, &i); err != nil {
					t.Fatal(err)
				}
				i -= 300000
				if len(tk.Tags) != 1 || tk.Tags[0] != fmt.Sprint("tag", i) {
					t.Fatalf("task %s: tags %v", tk.GID, tk.Tags)
				}
				if len(tk.Memberships) != 2 {
					t.Fatalf("task %s: memberships %v", tk.GID, tk.Memberships)
				}
				own := false
				for _, m := range tk.Memberships {
					own = own || m.ProjectGID == fmt.Sprint("p", i)
				}
				if !own {
					t.Fatalf("task %s: memberships %v, want p%d", tk.GID, tk.Memberships, i)
				}
			}
		})
	}
}

// Zero or negative modified_within_days is no filter, not a date in the future.
func TestModifiedWithinDaysNotPositive(t *testing.T) {
	s := manyTasks(t, 2)
	now := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	for _, days := range []int{0, -1, -(1 << 40), -int(^uint(0)>>1) - 1} {
		f, err := s.BuildFilter(context.Background(), FilterSpec{ModifiedWithinDays: days}, now)
		if err != nil {
			t.Fatal(err)
		}
		if f.ModifiedAfter != "" {
			t.Fatalf("days %d: ModifiedAfter = %q", days, f.ModifiedAfter)
		}
		if tasks, err := s.ListTasks(context.Background(), f); err != nil || len(tasks) != 2 {
			t.Fatalf("days %d: %d tasks, %v", days, len(tasks), err)
		}
	}
}
