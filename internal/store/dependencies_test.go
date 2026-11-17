package store

import (
	"context"
	"testing"

	"github.com/amasok/asagao/internal/asana"
)

func depGIDs(views []DependencyView) []string {
	var out []string
	for _, v := range views {
		out = append(out, v.GID)
	}
	return out
}

func TestDependenciesStoredBothWays(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	// t1 waits for t2 (incomplete) and x9 (not cached; name from task_refs)
	must(t, s.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "ソファを買う", ResourceSubtype: "milestone",
		Dependencies: []asana.Ref{{GID: "t2"}, {GID: "x9"}}, Dependents: []asana.Ref{}}}))
	must(t, s.ReplaceDependencies(ctx, "t1", []asana.TaskRef{{GID: "t2", Name: "転居届を出す"}, {GID: "x9", Name: "外のタスク", Completed: true}}, nil))
	d, err := s.GetTask(ctx, "t1")
	must(t, err)
	if got := depGIDs(d.Dependencies); len(got) != 2 || d.OpenDependencies != 1 {
		t.Fatalf("dependencies = %+v, open = %d", d.Dependencies, d.OpenDependencies)
	}
	if b := TaskBadges(d.TaskSummary); b != "◆ [待ち1]" {
		t.Fatalf("badges = %q", b)
	}
	t2, err := s.GetTask(ctx, "t2")
	must(t, err)
	if got := depGIDs(t2.Dependents); len(got) != 1 || got[0] != "t1" {
		t.Fatalf("dependents of t2 = %+v", t2.Dependents)
	}
}

func TestSetDependencyQueuesCancelsAndChecks(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	must(t, s.SetDependencyLocal(ctx, "t1", "t2", true, testNow))
	op := onlyOp(t, s)
	if op.Kind != OpAddDependency || op.TaskGID != "t1" || op.Payload["dependency"] != "t2" {
		t.Fatalf("op = %+v", op)
	}
	if got := DescribeChange(op); got != "「転居届を出す」を待つ" {
		t.Fatalf("describe = %q", got)
	}
	// a pull without the link keeps the unsent one (from either side)
	must(t, s.UpsertTasks(ctx, []asana.Task{{GID: "t2", Name: "転居届を出す", Dependents: []asana.Ref{}}}))
	d, _ := s.GetTask(ctx, "t1")
	if len(d.Dependencies) != 1 || !d.Dependencies[0].Pending {
		t.Fatalf("after pull = %+v", d.Dependencies)
	}
	if err := s.SetDependencyLocal(ctx, "t2", "t1", true, testNow); err == nil {
		t.Fatal("a cycle must be refused")
	}
	if err := s.SetDependencyLocal(ctx, "t1", "t1", true, testNow); err == nil {
		t.Fatal("a task cannot wait for itself")
	}
	// removing the unsent link cancels it
	must(t, s.SetDependencyLocal(ctx, "t1", "t2", false, testNow))
	if ops, _ := s.ListOps(ctx); len(ops) != 0 {
		t.Fatalf("ops = %+v", ops)
	}
	if d, _ := s.GetTask(ctx, "t1"); len(d.Dependencies) != 0 {
		t.Fatalf("dependencies = %+v", d.Dependencies)
	}
}

func TestDiscardDependencyRestores(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	must(t, s.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "ソファを買う", Dependencies: []asana.Ref{{GID: "t2"}}}}))
	must(t, s.SetDependencyLocal(ctx, "t1", "t2", false, testNow))
	must(t, s.DiscardOp(ctx, onlyOp(t, s).ID))
	if d, _ := s.GetTask(ctx, "t1"); len(d.Dependencies) != 1 {
		t.Fatalf("after discard = %+v", d.Dependencies)
	}
}

func TestApprovalStatusAndType(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	must(t, s.UpdateTaskLocal(ctx, "t1", map[string]any{"resource_subtype": "approval"}, testNow))
	must(t, s.UpdateTaskLocal(ctx, "t1", map[string]any{"approval_status": "approved"}, testNow))
	d, err := s.GetTask(ctx, "t1")
	must(t, err)
	if d.ResourceSubtype != "approval" || d.ApprovalStatus != "approved" || !d.Completed {
		t.Fatalf("task = %+v", d.TaskSummary)
	}
	if got := DescribeChange(onlyOp(t, s)); got != "種類を「承認」に変更、承認を「承認済み」に変更" {
		t.Fatalf("describe = %q", got)
	}
	if err := s.UpdateTaskLocal(ctx, "t1", map[string]any{"approval_status": "maybe"}, testNow); err == nil {
		t.Fatal("an unknown status must be refused")
	}
	for in, want := range map[string]string{"承認済み": "approved", "reject": "rejected", "pending": "pending"} {
		if got, err := ParseApprovalStatus(in); err != nil || got != want {
			t.Errorf("ParseApprovalStatus(%q) = %q, %v", in, got, err)
		}
	}
	if got, err := ParseSubtype("マイルストーン"); err != nil || got != "milestone" {
		t.Errorf("ParseSubtype = %q, %v", got, err)
	}
}

// A completed dependency pruned from the cache stays known (and does not count as open);
// one whose state was never known does not count either.
func TestOpenDependenciesAfterPrune(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	must(t, s.UpsertTasks(ctx, []asana.Task{
		{GID: "t2", Name: "転居届を出す", Completed: true},
		{GID: "t1", Name: "ソファを買う", Dependencies: []asana.Ref{{GID: "t2"}, {GID: "unknown"}}},
	}))
	_, err := s.PruneTasks(ctx, map[string]bool{"t1": true})
	must(t, err)
	d, err := s.GetTask(ctx, "t1")
	must(t, err)
	if d.OpenDependencies != 0 {
		t.Fatalf("open dependencies = %d", d.OpenDependencies)
	}
	if len(d.Dependencies) != 2 || d.Dependencies[1].Name != "転居届を出す" || !d.Dependencies[1].Completed {
		t.Fatalf("dependencies = %+v", d.Dependencies)
	}
}

func TestApprovalOnlyForApprovalTasks(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.UpdateTaskLocal(ctx, "t1", map[string]any{"approval_status": "approved"}, testNow); err == nil {
		t.Fatal("approval status on a normal task must be refused before sending")
	}
	if ops, _ := s.ListOps(ctx); len(ops) != 0 {
		t.Fatalf("nothing should be queued: %+v", ops)
	}
	// in the same change as making it an approval task: fine; discarding restores completed
	must(t, s.UpdateTaskLocal(ctx, "t1", map[string]any{"resource_subtype": "approval", "approval_status": "approved"}, testNow))
	must(t, s.DiscardOp(ctx, onlyOp(t, s).ID))
	d, _ := s.GetTask(ctx, "t1")
	if d.Completed || d.ResourceSubtype != "default_task" {
		t.Fatalf("after discard = %+v", d.TaskSummary)
	}
}
