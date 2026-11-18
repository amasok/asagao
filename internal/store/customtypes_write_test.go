package store

import (
	"github.com/amasok/asagao/internal/asana"
	"testing"
)

// A type no project lists is still named from the task.
func TestCustomTypeNamedByTaskOnly(t *testing.T) {
	st, ctx := openTypes(t)
	must(t, st.UpsertTasks(ctx, []asana.Task{{GID: "t3", Name: "other", ResourceSubtype: "custom",
		CustomType: &asana.Ref{GID: "ct9", Name: "外部"}, CustomTypeStatusOption: &asana.Ref{GID: "cx", Name: "保留"}}}))
	k, err := st.TaskKind(ctx, "t3")
	must(t, err)
	if k.Type == nil || k.Type.Name != "外部" || k.Status == nil || k.Status.Name != "保留" {
		t.Fatalf("kind %+v", k)
	}
	if len(k.Choices) != 1 || k.Choices[0].GID != "ct9" || len(k.Choices[0].Statuses) != 1 {
		t.Fatalf("its own type is a choice: %+v", k.Choices)
	}
}

func TestUpdateCustomTypeLocal(t *testing.T) {
	st, ctx := openTypes(t)
	// A new type without a status gets the first status that does not complete the task.
	must(t, st.UpdateTaskLocal(ctx, "t2", map[string]any{"resource_subtype": "custom", "custom_type": "ct1"}, testNow))
	d, _ := st.GetTask(ctx, "t2")
	if d.ResourceSubtype != "custom" || d.CustomType == nil || d.CustomType.GID != "ct1" || d.CustomTypeStatus == nil || d.CustomTypeStatus.GID != "cs-doing" {
		t.Fatalf("after setting the type: %+v / %+v", d.CustomType, d.CustomTypeStatus)
	}
	ops, err := st.PendingOps(ctx)
	must(t, err)
	if len(ops) != 1 || ops[0].Payload["custom_type_status_option"] != "cs-doing" || ops[0].Payload["custom_type"] != "ct1" {
		t.Fatalf("queued %+v", ops)
	}
	// A status that completes the task completes it.
	must(t, st.UpdateTaskLocal(ctx, "t2", map[string]any{"custom_type_status_option": "cs-done"}, testNow))
	if d, _ := st.GetTask(ctx, "t2"); !d.Completed || d.CustomTypeStatus.Name != "完了" {
		t.Fatalf("done status: completed=%v %+v", d.Completed, d.CustomTypeStatus)
	}
	// A status of another type is refused.
	if err := st.UpdateTaskLocal(ctx, "t2", map[string]any{"custom_type_status_option": "cr-new"}, testNow); err == nil {
		t.Fatal("a status of another type was accepted")
	}
	// Back to a plain task: the custom type is not sent, and is gone locally.
	must(t, st.UpdateTaskLocal(ctx, "t2", map[string]any{"resource_subtype": "milestone"}, testNow))
	d, _ = st.GetTask(ctx, "t2")
	if d.CustomType != nil || d.CustomTypeStatus != nil {
		t.Fatalf("milestone kept the type: %+v", d.TaskSummary)
	}
	ops, _ = st.PendingOps(ctx)
	if len(ops) != 1 || ops[0].Payload["resource_subtype"] != "milestone" || ops[0].Payload["custom_type"] != nil || ops[0].Payload["custom_type_status_option"] != nil {
		t.Fatalf("queued %+v", ops[0].Payload)
	}
}

func TestCustomTypeNeedsCustomSubtype(t *testing.T) {
	st, ctx := openTypes(t)
	for _, changes := range []map[string]any{
		{"custom_type": "ct1"},                    // a plain task
		{"custom_type_status_option": "cs-doing"}, // a plain task
		{"resource_subtype": "custom"},            // no type
		{"resource_subtype": "custom", "custom_type": ""},
	} {
		if err := st.UpdateTaskLocal(ctx, "t2", changes, testNow); err == nil {
			t.Errorf("%v was accepted", changes)
		}
	}
	// On a custom task, a status alone is fine.
	must(t, st.UpdateTaskLocal(ctx, "t1", map[string]any{"custom_type_status_option": "cs-done"}, testNow))
}

// Discarding the change restores the type, the status and completed.
func TestDiscardRestoresCustomType(t *testing.T) {
	st, ctx := openTypes(t)
	must(t, st.UpdateTaskLocal(ctx, "t1", map[string]any{"custom_type_status_option": "cs-done"}, testNow))
	must(t, st.UpdateTaskLocal(ctx, "t1", map[string]any{"resource_subtype": "default_task"}, testNow))
	ops, _ := st.PendingOps(ctx)
	if len(ops) != 1 {
		t.Fatalf("ops %+v", ops)
	}
	must(t, st.DiscardOp(ctx, ops[0].ID))
	d, _ := st.GetTask(ctx, "t1")
	if d.ResourceSubtype != "custom" || d.CustomType == nil || d.CustomType.GID != "ct1" || d.CustomTypeStatus == nil ||
		d.CustomTypeStatus.GID != "cs-doing" || d.Completed {
		t.Fatalf("after discard: %+v / %+v completed=%v", d.CustomType, d.CustomTypeStatus, d.Completed)
	}
}

// A task created offline gets its custom type in an update sent after the create.
func TestCustomTypeOfOfflineTaskIsAnUpdate(t *testing.T) {
	st, ctx := openTypes(t)
	gid, err := st.CreateTaskLocal(ctx, NewTask{Name: "new", ProjectGID: "p1", WorkspaceGID: "w1"}, testNow)
	must(t, err)
	must(t, st.UpdateTaskLocal(ctx, gid, map[string]any{"resource_subtype": "custom", "custom_type": "ct2"}, testNow))
	must(t, st.UpdateTaskLocal(ctx, gid, map[string]any{"name": "renamed"}, testNow))
	ops, _ := st.PendingOps(ctx)
	if len(ops) != 2 || ops[0].Kind != OpCreateTask || ops[1].Kind != OpUpdateTask {
		t.Fatalf("ops %+v", ops)
	}
	if _, ok := ops[0].Payload["custom_type"]; ok {
		t.Fatalf("the create has the type: %+v", ops[0].Payload)
	}
	if ops[1].Payload["custom_type"] != "ct2" || ops[1].Payload["custom_type_status_option"] != "cr-new" || ops[1].Payload["name"] != "renamed" {
		t.Fatalf("update %+v", ops[1].Payload)
	}
}
