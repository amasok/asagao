package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/domain/task"
)

// PruneTasks keeps its rules in the WHERE of one DELETE (see task.MayPrune for why). This
// checks that the SQL deletes exactly the tasks task.MayPrune allows, and remembers exactly
// those task.RememberRef says, for every combination of what decides it: a top-level task
// and its subtask, each returned or not, queued (in each outbox state) or not, and linked
// by a dependency (on either side) or not.
func TestPruneTasksFollowsTaskRules(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	type flags struct{ returned, queued, linked bool }
	all := func(f func(flags)) {
		for i := range 8 {
			f(flags{i&1 != 0, i&2 != 0, i&4 != 0})
		}
	}
	states := []string{OpPending, OpConflict, OpFailed, OpSending}

	keep := map[string]bool{"anchor": true}
	want := map[string]task.PruneCandidate{}
	var tasks []asana.Task
	ws := &asana.Ref{GID: "w1"}
	tasks = append(tasks, asana.Task{GID: "anchor", Name: "anchor", Workspace: ws})
	n := 0
	setUp := func(gid, parent string, f flags) {
		n++
		tk := asana.Task{GID: gid, Name: gid, Workspace: ws}
		if parent != "" {
			tk.Parent = &asana.Ref{GID: parent}
		}
		tasks = append(tasks, tk)
		if f.returned {
			keep[gid] = true
		}
		if f.queued {
			exec("INSERT INTO outbox(kind, task_gid, payload, state, created_at) VALUES(?, ?, '{}', ?, '2026-11-17T00:00:00Z')",
				OpUpdateTask, gid, states[n%len(states)])
		}
		if f.linked {
			if n%2 == 0 {
				exec("INSERT INTO task_dependencies(task_gid, dependency_gid) VALUES(?, 'anchor')", gid)
			} else {
				exec("INSERT INTO task_dependencies(task_gid, dependency_gid) VALUES('anchor', ?)", gid)
			}
		}
	}
	all(func(pf flags) {
		all(func(cf flags) {
			p := fmt.Sprintf("p-%v-%v-%v", pf.returned, pf.queued, pf.linked)
			c := fmt.Sprintf("c-%v-%v-%v/%s", cf.returned, cf.queued, cf.linked, p)
			if _, ok := want[p]; !ok {
				setUp(p, "", pf)
				want[p] = task.PruneCandidate{Returned: pf.returned, Queued: pf.queued, Linked: pf.linked}
			}
			setUp(c, p, cf)
			want[c] = task.PruneCandidate{Returned: cf.returned, Queued: cf.queued, Linked: cf.linked, HasParent: true, ParentReturned: pf.returned}
		})
	})
	if err := s.UpsertTasks(ctx, tasks); err != nil {
		t.Fatal(err)
	}
	exec("DELETE FROM task_refs")

	deleted, err := s.PruneTasks(ctx, keep)
	if err != nil {
		t.Fatal(err)
	}
	var wantDeleted int64
	for gid, c := range want {
		var inTasks, inRefs int
		if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM tasks WHERE gid = ?", gid).Scan(&inTasks); err != nil {
			t.Fatal(err)
		}
		if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM task_refs WHERE gid = ?", gid).Scan(&inRefs); err != nil {
			t.Fatal(err)
		}
		if task.MayPrune(c) {
			wantDeleted++
		}
		if (inTasks == 0) != task.MayPrune(c) {
			t.Errorf("%s %+v: deleted = %v, MayPrune = %v", gid, c, inTasks == 0, task.MayPrune(c))
		}
		if (inRefs == 1) != task.RememberRef(c) {
			t.Errorf("%s %+v: in task_refs = %v, RememberRef = %v", gid, c, inRefs == 1, task.RememberRef(c))
		}
	}
	if deleted != wantDeleted || wantDeleted == 0 || int(wantDeleted) == len(want) {
		t.Errorf("PruneTasks deleted %d of %d, want %d", deleted, len(want), wantDeleted)
	}
	var anchor int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM tasks WHERE gid = 'anchor'").Scan(&anchor); err != nil || anchor != 1 {
		t.Errorf("the returned anchor task: %d, %v", anchor, err)
	}
}
