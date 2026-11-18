package store

import (
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/asana"
)

// Attack tests for custom task types (#59): ways around the checks, and the consistency of
// type, status and completed.

// Setting a status that completes the task, and then reopening the task: the user's last
// word (not completed) must hold, here and in what is queued.
func TestAttackReopenAfterDoneStatus(t *testing.T) {
	st, ctx := openTypes(t)
	must(t, st.UpdateTaskLocal(ctx, "t1", map[string]any{"custom_type_status_option": "cs-done"}, testNow))
	must(t, st.UpdateTaskLocal(ctx, "t1", map[string]any{"completed": false}, testNow))
	d, _ := st.GetTask(ctx, "t1")
	if d.Completed {
		t.Fatalf("reopened by the user, but completed (status %+v)", d.CustomTypeStatus)
	}
	ops, _ := st.PendingOps(ctx)
	for _, op := range ops {
		if op.Payload["custom_type_status_option"] == "cs-done" && op.Payload["completed"] == false {
			t.Fatalf("one entry says done by status and not completed: %+v", op.Payload)
		}
	}
}

// Back to a type and its status from custom -> milestone -> custom keeps the checks.
func TestAttackRoundTripThroughMilestone(t *testing.T) {
	st, ctx := openTypes(t)
	must(t, st.UpdateTaskLocal(ctx, "t1", map[string]any{"resource_subtype": "milestone"}, testNow))
	// The old status must not be usable on a milestone.
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"custom_type_status_option": "cs-done"}, testNow); err == nil {
		t.Fatal("status on a milestone")
	}
	must(t, st.UpdateTaskLocal(ctx, "t1", map[string]any{"resource_subtype": "custom", "custom_type": "ct2"}, testNow))
	d, _ := st.GetTask(ctx, "t1")
	if d.CustomType == nil || d.CustomType.GID != "ct2" || d.CustomTypeStatus == nil || d.CustomTypeStatus.GID != "cr-new" {
		t.Fatalf("%+v %+v", d.CustomType, d.CustomTypeStatus)
	}
	if d.Completed {
		t.Fatal("completed by a default status")
	}
}

// An unknown resource_subtype is not queued.
func TestAttackUnknownSubtype(t *testing.T) {
	st, ctx := openTypes(t)
	for _, v := range []any{"bogus", "", "CUSTOM ", 5} {
		if err := st.UpdateTaskLocal(ctx, "t2", map[string]any{"resource_subtype": v}, testNow); err == nil {
			// "custom" with a type is the only odd one; these are not it.
			t.Errorf("resource_subtype %#v was queued", v)
		}
	}
}

// The task's own type survives a pull that does not list it, and a type whose project list
// drops it: the task still shows its type and status names.
func TestAttackTypeRemovedFromProject(t *testing.T) {
	st, ctx := openTypes(t)
	must(t, st.ReplaceProjectCustomTypes(ctx, "p1", nil))
	d, err := st.GetTask(ctx, "t1")
	must(t, err)
	if d.CustomType == nil || d.CustomType.Name != "バグ" || d.CustomTypeStatus == nil || d.CustomTypeStatus.Name != "対応中" {
		t.Fatalf("lost %+v %+v", d.CustomType, d.CustomTypeStatus)
	}
	// ...and a status of the type, not of the (gone) list, is not refused as "another type's".
	k, err := st.TaskKind(ctx, "t1")
	must(t, err)
	if k.Type == nil || len(k.Choices) != 1 {
		t.Fatalf("kind %+v", k)
	}
}

// A type renamed in Asana: the new name replaces the old; an empty name keeps it.
func TestAttackTypeRenamed(t *testing.T) {
	st, ctx := openTypes(t)
	must(t, st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "custom", ResourceSubtype: "custom", Workspace: &asana.Ref{GID: "w1"},
		CustomType:             &asana.Ref{GID: "ct1", Name: "不具合"},
		CustomTypeStatusOption: &asana.Ref{GID: "cs-doing", Name: "作業中"},
		Memberships:            []asana.Membership{{Project: &asana.Ref{GID: "p1"}}}}}))
	d, _ := st.GetTask(ctx, "t1")
	if d.CustomType.Name != "不具合" || d.CustomTypeStatus.Name != "作業中" {
		t.Fatalf("%+v %+v", d.CustomType, d.CustomTypeStatus)
	}
	// A refetch without names keeps them.
	must(t, st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "custom", ResourceSubtype: "custom", Workspace: &asana.Ref{GID: "w1"},
		CustomType: &asana.Ref{GID: "ct1"}, CustomTypeStatusOption: &asana.Ref{GID: "cs-doing"},
		Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}}}}}))
	d, _ = st.GetTask(ctx, "t1")
	if d.CustomType.Name != "不具合" || d.CustomTypeStatus.Name != "作業中" {
		t.Fatalf("names lost: %+v %+v", d.CustomType, d.CustomTypeStatus)
	}
}

// A pull that overwrites the task while a change is unsent: the pending type, status and
// completed win and agree (reapplyUpdates).
func TestAttackReapplyAfterPull(t *testing.T) {
	st, ctx := openTypes(t)
	must(t, st.UpdateTaskLocal(ctx, "t1", map[string]any{"custom_type_status_option": "cs-done"}, testNow))
	// Asana still has it in progress (or moved it to something else).
	must(t, st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "custom", ResourceSubtype: "custom", Workspace: &asana.Ref{GID: "w1"},
		CustomType:             &asana.Ref{GID: "ct1", Name: "バグ"},
		CustomTypeStatusOption: &asana.Ref{GID: "cs-doing", Name: "対応中"},
		Memberships:            []asana.Membership{{Project: &asana.Ref{GID: "p1"}}}}}))
	d, _ := st.GetTask(ctx, "t1")
	if d.CustomTypeStatus == nil || d.CustomTypeStatus.GID != "cs-done" || !d.Completed {
		t.Fatalf("pending status lost: %+v completed=%v", d.CustomTypeStatus, d.Completed)
	}
	// The server turns the task into a plain one; the pending status (of the type) must not
	// leave a status on a plain task.
	must(t, st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "custom", ResourceSubtype: "default_task", Workspace: &asana.Ref{GID: "w1"},
		Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}}}}}))
	d, _ = st.GetTask(ctx, "t1")
	if d.ResourceSubtype != "custom" && (d.CustomType != nil || d.CustomTypeStatus != nil) {
		t.Fatalf("a plain task with a custom status: %+v %+v", d.CustomType, d.CustomTypeStatus)
	}
}

// Asana's names are terminal-safe: control characters in a type or status name are gone.
func TestAttackNamesAreTermsafe(t *testing.T) {
	st, ctx := openTypes(t)
	evil := "バ\x1b[2Jグ\x07"
	must(t, st.ReplaceProjectCustomTypes(ctx, "p1", []asana.CustomType{{GID: "ct1", Name: evil, StatusOptions: []asana.CustomTypeStatusOption{
		{GID: "cs-doing", Name: "対\x1b]0;x\x07応", CompletionState: "Incomplete"}}}}))
	must(t, st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "custom", ResourceSubtype: "custom", Workspace: &asana.Ref{GID: "w1"},
		CustomType:             &asana.Ref{GID: "ct1", Name: evil},
		CustomTypeStatusOption: &asana.Ref{GID: "cs-doing", Name: "対\x1b]0;x\x07応"},
		Memberships:            []asana.Membership{{Project: &asana.Ref{GID: "p1"}}}}}))
	d, _ := st.GetTask(ctx, "t1")
	list, _ := st.ListTasks(ctx, TaskFilter{ProjectGID: "p1"})
	k, _ := st.TaskKind(ctx, "t1")
	var all []string
	all = append(all, d.CustomType.Name, d.CustomTypeStatus.Name)
	for _, c := range d.CustomTypes {
		all = append(all, c.Name)
		for _, s := range c.Statuses {
			all = append(all, s.Name)
		}
	}
	for _, tk := range list {
		if tk.CustomType != nil {
			all = append(all, tk.CustomType.Name, tk.CustomTypeStatus.Name)
		}
	}
	all = append(all, k.Type.Name, k.Status.Name)
	for _, c := range k.Choices {
		all = append(all, c.Name)
	}
	for _, s := range all {
		if strings.ContainsAny(s, "\x1b\x07") {
			t.Errorf("control character in %q", s)
		}
	}
}

// 50 types and 20 statuses each (the limits Asana documents) are stored and listed whole.
func TestAttackLimits(t *testing.T) {
	st, ctx := openTypes(t)
	var types []asana.CustomType
	for i := 0; i < 50; i++ {
		ct := asana.CustomType{GID: "T" + string(rune('A'+i/26)) + string(rune('a'+i%26)), Name: "t"}
		for j := 0; j < 20; j++ {
			ct.StatusOptions = append(ct.StatusOptions, asana.CustomTypeStatusOption{GID: ct.GID + "-s" + string(rune('a'+j)), Name: "s", CompletionState: "Incomplete"})
		}
		types = append(types, ct)
	}
	must(t, st.ReplaceProjectCustomTypes(ctx, "p1", types))
	got, err := st.CustomTypes(ctx, []string{"p1"})
	must(t, err)
	if len(got) != 50 {
		t.Fatalf("%d types", len(got))
	}
	for _, c := range got {
		if len(c.Statuses) != 20 {
			t.Fatalf("%s: %d statuses", c.GID, len(c.Statuses))
		}
	}
}

// Setting the type of a task that does not exist, and of an empty/odd gid.
func TestAttackMissingTask(t *testing.T) {
	st, ctx := openTypes(t)
	if err := st.UpdateTaskLocal(ctx, "nope", map[string]any{"resource_subtype": "custom", "custom_type": "ct1"}, testNow); err == nil {
		t.Fatal("no error for a missing task")
	}
}
