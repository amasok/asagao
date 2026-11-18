package sync

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/readmodel"
)

// PUT /tasks gets resource_subtype "custom", the type and the status; a status alone goes
// with its type; the task is refreshed from the answer.
func TestPushCustomType(t *testing.T) {
	f, s, st, _ := setupTypes(t)
	ctx := context.Background()
	if err := s.syncCustomTypes(ctx, "p1", "w1", false); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"resource_subtype": "custom", "custom_type": "ct1"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Push(ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.puts) != 1 || f.puts[0]["resource_subtype"] != "custom" || f.puts[0]["custom_type"] != "ct1" || f.puts[0]["custom_type_status_option"] != "cs-doing" {
		t.Fatalf("PUT %v", f.puts)
	}
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"custom_type_status_option": "cs-done"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Push(ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.puts) != 2 || f.puts[1]["resource_subtype"] != "custom" || f.puts[1]["custom_type"] != "ct1" || f.puts[1]["custom_type_status_option"] != "cs-done" {
		t.Fatalf("PUT %v", f.puts)
	}
	if n, _ := st.PendingOps(ctx); len(n) != 0 {
		t.Fatalf("left %+v", n)
	}
	d, _ := st.GetTask(ctx, "t1")
	if d.CustomTypeStatus == nil || d.CustomTypeStatus.GID != "cs-done" {
		t.Fatalf("after push %+v", d.CustomTypeStatus)
	}
}

// Someone changed the status in Asana meanwhile: a conflict, not an overwrite.
func TestPushCustomTypeConflict(t *testing.T) {
	f, s, st, _ := setupTypes(t)
	ctx := context.Background()
	f.subtype, f.typ, f.status = "custom", "ct1", "cs-doing"
	if err := s.syncCustomTypes(ctx, "p1", "w1", false); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "A", ResourceSubtype: "custom", Workspace: &asana.Ref{GID: "w1"},
		CustomType: &asana.Ref{GID: "ct1"}, CustomTypeStatusOption: &asana.Ref{GID: "cs-doing"},
		Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}}}}}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"custom_type_status_option": "cs-done"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	f.status = "cs-other"
	res, err := s.Push(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Conflicts != 1 || len(f.puts) != 0 {
		t.Fatalf("push %+v, puts %v", res, f.puts)
	}
}

// A 402 for the write records that the plan lacks custom types, and drops the custom
// type with the reason; the task goes back to what it was.
func TestPushCustomTypePaymentRequired(t *testing.T) {
	f, s, st, _ := setupTypes(t)
	ctx := context.Background()
	if err := s.syncCustomTypes(ctx, "p1", "w1", false); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"resource_subtype": "custom", "custom_type": "ct1"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	f.refuse = true
	res, err := s.Push(ctx)
	if err != nil || len(res.Discarded) != 1 || !strings.Contains(res.Discarded[0].Reason, "カスタムタイプ") || res.Failed != 0 {
		t.Fatalf("push %+v, %v", res, err)
	}
	if v, _ := st.SyncState(ctx, readmodel.PlanKey(readmodel.PlanCustomTypes, "w1")); v == "" {
		t.Fatal("402 not recorded")
	}
	if ops, _ := st.ListOps(ctx); len(ops) != 0 {
		t.Fatalf("left %+v", ops)
	}
	if d, _ := st.GetTask(ctx, "t1"); d.ResourceSubtype != "default_task" || d.CustomType != nil {
		t.Fatalf("not restored: %+v", d.TaskSummary)
	}
}

// Asana refuses the custom type (400), or answers without applying it: the custom type is
// dropped with the reason and the task's type restored, but the rest of the same change
// (a new name) reaches Asana and stays; nothing is sent again after that.
func TestPushCustomTypeNotSupported(t *testing.T) {
	for _, mode := range []string{"reject", "ignore"} {
		for _, withName := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s name=%v", mode, withName), func(t *testing.T) {
				f, s, st, _ := setupTypes(t)
				ctx := context.Background()
				if err := s.syncCustomTypes(ctx, "p1", "w1", false); err != nil {
					t.Fatal(err)
				}
				f.reject, f.ignore = mode == "reject", mode == "ignore"
				if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"resource_subtype": "custom", "custom_type": "ct1"}, time.Now()); err != nil {
					t.Fatal(err)
				}
				if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"custom_type_status_option": "cs-done"}, time.Now()); err != nil {
					t.Fatal(err)
				}
				if withName {
					if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "新しい名前"}, time.Now()); err != nil {
						t.Fatal(err)
					}
				}
				res, err := s.Push(ctx)
				if err != nil || res.Failed != 0 || len(res.Discarded) != 1 || !strings.Contains(res.Discarded[0].Reason, "API が対応していない") {
					t.Fatalf("push %+v, %v", res, err)
				}
				if ops, _ := st.ListOps(ctx); len(ops) != 0 {
					t.Fatalf("left %+v", ops)
				}
				d, _ := st.GetTask(ctx, "t1")
				if d.ResourceSubtype != "default_task" || d.CustomType != nil || d.Completed {
					t.Fatalf("not restored: %+v", d.TaskSummary)
				}
				wantName := "A"
				if withName {
					wantName = "新しい名前"
				}
				if d.Name != wantName || (withName && f.name != "新しい名前") {
					t.Fatalf("name: cache %q, Asana %q", d.Name, f.name)
				}
				puts := len(f.puts)
				if _, err := s.Push(ctx); err != nil || len(f.puts) != puts {
					t.Fatalf("sent again: %d → %d, %v", puts, len(f.puts), err)
				}
			})
		}
	}
}

// A 402 to an entry with a start date and a custom type takes out the custom type only;
// the start date goes with the rest, and start dates are not hidden.
func TestPushCustomTypeAndStartOnPaymentRequired(t *testing.T) {
	f, s, st, _ := setupTypes(t)
	ctx := context.Background()
	if err := s.syncCustomTypes(ctx, "p1", "w1", false); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "A", Workspace: &asana.Ref{GID: "w1"}, DueOn: "2026-12-31",
		Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}}}}}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"resource_subtype": "custom", "custom_type": "ct1", "start_on": "2026-12-01"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	f.refuse, f.failRest = true, true // the rest is not sent this time
	res, _ := s.Push(ctx)
	if len(res.Discarded) != 1 || !strings.Contains(res.Discarded[0].Reason, "カスタムタイプ") {
		t.Fatalf("push %+v", res)
	}
	if v, _ := st.SyncState(ctx, readmodel.PlanKey(readmodel.PlanStartOn, "w1")); v != "" {
		t.Fatal("start dates hidden for a custom type's 402")
	}
	f.failRest = false
	if _, err := s.Push(ctx); err != nil {
		t.Fatal(err)
	}
	if last := f.puts[len(f.puts)-1]; last["start_on"] != "2026-12-01" {
		t.Fatalf("puts %v", f.puts)
	}
	if ops, _ := st.ListOps(ctx); len(ops) != 0 {
		t.Fatalf("left %+v", ops)
	}
}
