package sync

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/domain/plan"
	"github.com/amasok/asagao/internal/store"
	"github.com/amasok/asagao/internal/ui/format"
)

// #62: Asana answers a start date on a milestone with a 400 cannot_set_start_date_on_milestone
// (measured on the real Asana, an update and a create alike). The start date is taken
// out with the reason and the rest sent, as after a 402, but start dates are not marked as
// unavailable in the workspace: it is not the plan.

// noStartMark fails when start dates are marked as unavailable in w1.
func noStartMark(t *testing.T, st *store.Store) {
	t.Helper()
	if at, ok, err := st.PlanRefusedAt(context.Background(), plan.StartOn, "w1"); ok || err != nil {
		t.Fatalf("start dates marked unavailable in w1 at %v (%v)", at, err)
	}
}

// The task became a milestone in Asana after the start date was queued (the cache did not
// know it): the start date is dropped, the name is sent.
func TestPushMilestoneStartOn400(t *testing.T) {
	f, s, st := setupStart(t, map[string]any{"due_on": "2026-12-10"})
	st.SetDescribeChange(format.DescribeChange)
	ctx := context.Background()
	s.now = func() time.Time { return now }
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "リリース", "start_on": "2026-12-01"}, now); err != nil {
		t.Fatal(err)
	}
	f.task["resource_subtype"] = "milestone"
	res, err := s.Push(ctx)
	if err != nil || res.Failed != 0 || res.Sent != 1 || len(res.Discarded) != 1 {
		t.Fatalf("push = %+v, %v (puts %v)", res, err, f.puts)
	}
	if d := res.Discarded[0]; !strings.Contains(d.Reason, "マイルストーン") || !strings.Contains(d.Reason, "2026-12-01") ||
		!strings.Contains(d.Summary, "開始日を 2026-12-01") {
		t.Fatalf("discard = %+v", d)
	}
	if f.task["name"] != "リリース" || f.task["start_on"] != nil {
		t.Fatalf("server task = %v", f.task)
	}
	if d, _ := st.GetTask(ctx, "t1"); d.Name != "リリース" || d.StartOn != "" || d.Pending {
		t.Fatalf("t1 = name %q start %q pending %v", d.Name, d.StartOn, d.Pending)
	}
	if ops, _ := st.ListOps(ctx); len(ops) != 0 {
		t.Fatalf("ops = %+v", ops)
	}
	noStartMark(t, st)
}

// Only the start date (what realcheck sent): the entry is gone and the cache has no start
// date, as Asana; start dates stay offered in the workspace.
func TestPushMilestoneStartOnOnly400(t *testing.T) {
	f, s, st := setupStart(t, map[string]any{"due_on": "2026-12-10"})
	ctx := context.Background()
	s.now = func() time.Time { return now }
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"start_on": "2026-12-01"}, now); err != nil {
		t.Fatal(err)
	}
	f.task["resource_subtype"] = "milestone"
	res, err := s.Push(ctx)
	if err != nil || res.Failed != 0 || len(res.Discarded) != 1 {
		t.Fatalf("push = %+v, %v (puts %v)", res, err, f.puts)
	}
	if ops, _ := st.ListOps(ctx); len(ops) != 0 {
		t.Fatalf("ops = %+v", ops)
	}
	noStartMark(t, st)
	if d, _ := st.GetTask(ctx, "t1"); d.StartOn != "" || d.Pending {
		t.Fatalf("t1 = start %q pending %v", d.StartOn, d.Pending)
	}
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"resource_subtype": "default_task", "start_on": "2026-12-03"}, now); err != nil {
		t.Fatalf("a start date with the type changed back: %v", err)
	}
}

// A task created with a start date and made a milestone before it was sent: Asana refuses
// the create (400); it is created again without the start date.
func TestPushCreateMilestoneStartOn400(t *testing.T) {
	f, s, st := setupStart(t, nil)
	ctx := context.Background()
	s.now = func() time.Time { return now }
	gid, err := st.CreateTaskLocal(ctx, store.NewTask{Name: "リリース", ProjectGID: "p1", DueOn: "2026-12-10", StartOn: "2026-12-01"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateTaskLocal(ctx, gid, map[string]any{"resource_subtype": "milestone"}, now); err != nil {
		t.Fatal(err)
	}
	res, err := s.Push(ctx)
	if err != nil || res.Failed != 0 || res.Sent != 1 || len(res.Discarded) != 1 {
		t.Fatalf("push = %+v, %v (created %v)", res, err, f.created)
	}
	if _, ok := f.created["start_on"]; ok || f.created["resource_subtype"] != "milestone" || f.created["due_on"] != "2026-12-10" {
		t.Fatalf("created %v", f.created)
	}
	real, err := st.RealGID(ctx, gid)
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := st.GetTask(ctx, real); d == nil || d.StartOn != "" || d.DueOn != "2026-12-10" {
		t.Fatalf("created task = %+v", d)
	}
	noStartMark(t, st)
}

// Another 400 to a start date is not taken for the milestone's: the entry fails as before.
func TestPushStartOnOther400Fails(t *testing.T) {
	f, s, st := setupStart(t, map[string]any{"due_on": "2026-12-10"})
	ctx := context.Background()
	s.now = func() time.Time { return now }
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"start_on": "2026-12-01"}, now); err != nil {
		t.Fatal(err)
	}
	f.badRequest = true
	res, err := s.Push(ctx)
	if err != nil || res.Failed != 1 || len(res.Discarded) != 0 {
		t.Fatalf("push = %+v, %v", res, err)
	}
	noStartMark(t, st)
}
