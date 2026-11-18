package sync

import (
	"context"
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

// A 402 for the write says that the plan lacks custom types, and records it.
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
	res, _ := s.Push(ctx)
	if res.Failed != 1 {
		t.Fatalf("push %+v", res)
	}
	if v, _ := st.SyncState(ctx, readmodel.PlanKey(readmodel.PlanCustomTypes, "w1")); v == "" {
		t.Fatal("402 not recorded")
	}
	ops, _ := st.ListOps(ctx)
	if len(ops) != 1 || ops[0].State != "failed" || !strings.Contains(ops[0].LastError, "カスタムタイプ") {
		t.Fatalf("failed %+v", ops)
	}
}

// Asana refuses the custom type (400), or answers without applying it: the change stops
// as failed with the reason, is not sent again, and the local value stays until it is
// discarded (which restores the task).
func TestPushCustomTypeNotSupported(t *testing.T) {
	for _, mode := range []string{"reject", "ignore"} {
		t.Run(mode, func(t *testing.T) {
			f, s, st, _ := setupTypes(t)
			ctx := context.Background()
			if err := s.syncCustomTypes(ctx, "p1", "w1", false); err != nil {
				t.Fatal(err)
			}
			f.reject, f.ignore = mode == "reject", mode == "ignore"
			if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"resource_subtype": "custom", "custom_type": "ct1"}, time.Now()); err != nil {
				t.Fatal(err)
			}
			res, _ := s.Push(ctx)
			if res.Failed != 1 || len(f.puts) != 1 {
				t.Fatalf("push %+v, puts %d", res, len(f.puts))
			}
			ops, _ := st.ListOps(ctx)
			if len(ops) != 1 || ops[0].State != "failed" || !strings.Contains(ops[0].LastError, "API が対応していない") {
				t.Fatalf("ops %+v", ops)
			}
			if _, err := s.Push(ctx); err != nil || len(f.puts) != 1 {
				t.Fatalf("sent again: %d, %v", len(f.puts), err)
			}
			if v, _ := st.SyncState(ctx, readmodel.PlanKey(readmodel.PlanCustomTypes, "w1")); v != "" {
				t.Fatal("recorded as the plan's")
			}
			if err := st.DiscardOp(ctx, ops[0].ID); err != nil {
				t.Fatal(err)
			}
			if d, _ := st.GetTask(ctx, "t1"); d.ResourceSubtype != "default_task" || d.CustomType != nil {
				t.Fatalf("after discard %+v", d.TaskSummary)
			}
		})
	}
}
