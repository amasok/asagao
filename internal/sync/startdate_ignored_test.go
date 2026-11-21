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

// #62: Asana may answer 200 to a start date the workspace's plan lacks and keep the task
// without it. The rest of the change is done; the start date is reported as a 402's is,
// and start dates are hidden in the workspace for a while.

func TestPushStartOnIgnoredUpdate(t *testing.T) {
	for _, tc := range []struct {
		name, had string // the start date in Asana before
	}{{"none before", ""}, {"another before", "2026-11-30"}} {
		t.Run(tc.name, func(t *testing.T) {
			task := map[string]any{"due_on": "2026-12-10", "start_on": nil}
			if tc.had != "" {
				task["start_on"] = tc.had
			}
			f, s, st := setupStart(t, task)
			st.SetDescribeChange(format.DescribeChange)
			ctx := context.Background()
			s.now = func() time.Time { return now }
			f.ignoreStart = true
			if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "詳細設計", "start_on": "2026-12-01"}, now); err != nil {
				t.Fatal(err)
			}
			res, err := s.Push(ctx)
			if err != nil || res.Failed != 0 || res.Sent != 1 || len(res.Discarded) != 1 {
				t.Fatalf("push = %+v, %v (puts %v)", res, err, f.puts)
			}
			if len(f.puts) != 1 {
				t.Fatalf("sent again: puts = %v", f.puts)
			}
			d := res.Discarded[0]
			if !strings.Contains(d.Reason, "タスクの開始日") {
				t.Fatalf("discard reason = %q", d.Reason)
			}
			if d.TaskGID != "t1" || !strings.Contains(d.Summary, "開始日を 2026-12-01") {
				t.Fatalf("discard = %+v", d)
			}
			if ds, _ := st.PendingDiscards(ctx); len(ds) != 1 {
				t.Fatalf("pending discards = %+v", ds)
			}
			if at, ok, _ := st.PlanRefusedAt(ctx, plan.StartOn, "w1"); !ok || !plan.OffAt(at, now) {
				t.Fatalf("start dates not hidden in w1: (%v, %v)", at, ok)
			}
			if ops, _ := st.ListOps(ctx); len(ops) != 0 {
				t.Fatalf("ops = %+v", ops)
			}
			if got, _ := st.GetTask(ctx, "t1"); got.Name != "詳細設計" || got.StartOn != tc.had || got.Pending {
				t.Fatalf("t1 = name %q start %q pending %v, want start %q", got.Name, got.StartOn, got.Pending, tc.had)
			}
			// Hidden: another start date is refused before it is queued.
			if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"start_on": "2026-12-03"}, now); err == nil {
				t.Fatal("a start date was queued while start dates are hidden")
			}
		})
	}
}

func TestPushStartOnIgnoredCreate(t *testing.T) {
	f, s, st := setupStart(t, nil)
	st.SetDescribeChange(format.DescribeChange)
	ctx := context.Background()
	s.now = func() time.Time { return now }
	f.ignoreStart = true
	gid, err := st.CreateTaskLocal(ctx, store.NewTask{Name: "見積もり", ProjectGID: "p1", DueOn: "2026-12-10", StartOn: "2026-12-01"}, now)
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.Push(ctx)
	if err != nil || res.Failed != 0 || res.Sent != 1 || len(res.Discarded) != 1 {
		t.Fatalf("push = %+v, %v (created %v)", res, err, f.created)
	}
	if f.created["start_on"] != "2026-12-01" {
		t.Fatalf("created %v", f.created)
	}
	real, err := st.RealGID(ctx, gid)
	if err != nil || real != "new1" {
		t.Fatalf("real gid = %q, %v", real, err)
	}
	if d := res.Discarded[0]; d.TaskGID != "new1" || !strings.Contains(d.Reason, "タスクの開始日") || !strings.Contains(d.Summary, "開始日を 2026-12-01") {
		t.Fatalf("discard = %+v", d)
	}
	if got, _ := st.GetTask(ctx, real); got == nil || got.StartOn != "" || got.DueOn != "2026-12-10" || got.Pending {
		t.Fatalf("created task = %+v", got)
	}
	if _, ok, _ := st.PlanRefusedAt(ctx, plan.StartOn, "w1"); !ok {
		t.Fatal("start dates not hidden in w1")
	}
	if ops, _ := st.ListOps(ctx); len(ops) != 0 {
		t.Fatalf("ops = %+v", ops)
	}
}

// An answer that kept the start date forgets an earlier mark; one without start_on
// says nothing about it and is not taken for an ignored start date.
func TestPushStartOnKeptOrNotAnswered(t *testing.T) {
	for _, tc := range []struct {
		name      string
		omit      bool
		wantStart string // in the cache after the push ("-": not checked, the answer did not say)
	}{{"kept", false, "2026-12-01"}, {"not in the answer", true, "-"}} {
		t.Run(tc.name, func(t *testing.T) {
			f, s, st := setupStart(t, map[string]any{"due_on": "2026-12-10"})
			ctx := context.Background()
			s.now = func() time.Time { return now }
			f.omitStart = tc.omit
			if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"start_on": "2026-12-01"}, now); err != nil {
				t.Fatal(err)
			}
			// expired, so the start date was queued; the answer decides what is kept
			if err := st.RecordPlanRefusal(ctx, plan.StartOn, "w1", now.Add(-plan.RecheckAfter-time.Hour)); err != nil {
				t.Fatal(err)
			}
			res, err := s.Push(ctx)
			if err != nil || res.Failed != 0 || res.Sent != 1 || len(res.Discarded) != 0 {
				t.Fatalf("push = %+v, %v", res, err)
			}
			if ds, _ := st.PendingDiscards(ctx); len(ds) != 0 {
				t.Fatalf("pending discards = %+v", ds)
			}
			if at, ok, _ := st.PlanRefusedAt(ctx, plan.StartOn, "w1"); ok && plan.OffAt(at, now) {
				t.Fatalf("start dates hidden after the answer: %v", at)
			}
			if got, _ := st.GetTask(ctx, "t1"); (tc.wantStart != "-" && got.StartOn != tc.wantStart) || got.Pending {
				t.Fatalf("t1 start %q pending %v", got.StartOn, got.Pending)
			}
		})
	}
	t.Run("create not in the answer", func(t *testing.T) {
		f, s, st := setupStart(t, nil)
		ctx := context.Background()
		f.omitStart, f.ignoreStart = true, true
		if _, err := st.CreateTaskLocal(ctx, store.NewTask{Name: "見積もり", ProjectGID: "p1", DueOn: "2026-12-10", StartOn: "2026-12-01"}, now); err != nil {
			t.Fatal(err)
		}
		res, err := s.Push(ctx)
		if err != nil || res.Failed != 0 || res.Sent != 1 || len(res.Discarded) != 0 {
			t.Fatalf("push = %+v, %v", res, err)
		}
		if _, ok, _ := st.PlanRefusedAt(ctx, plan.StartOn, "w1"); ok {
			t.Fatal("start dates hidden after an answer without start_on")
		}
	})
}

// An update with a start date is not sent in a POST /batch (#62): on its own it gets the
// due date Asana requires with it, and an answer that did not keep it, or a 402, is
// handled (startOnOutcome).
func TestPushStartOnNotBatched(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	gids := addBatchTasks(t, ws, st, 3)
	for _, g := range gids {
		c := map[string]any{"completed": true}
		if g == "b2" {
			c = map[string]any{"start_on": "2026-12-01", "due_on": "2026-12-10"}
		}
		if err := st.UpdateTaskLocal(ctx, g, c, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Push(ctx); err != nil {
		t.Fatal(err)
	}
	for _, b := range ws.writeBatches() {
		for _, a := range b {
			if a["relative_path"] == "/tasks/b2" {
				t.Fatalf("the start date went out in a batch: %v", a)
			}
		}
	}
	if got := ws.outerCalls("PUT /tasks/b2"); len(got) != 1 {
		t.Fatalf("PUT /tasks/b2 = %v (calls %v)", got, ws.outer)
	}
}
