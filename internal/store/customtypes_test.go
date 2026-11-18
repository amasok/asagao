package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/asana"
)

// Custom task types (#59) in the cache.

func ptrBool(b bool) *bool { return &b }

// bugTypes is a project's list: バグ (対応中 / 完了 / 古い, which can no longer be chosen)
// and 依頼 (受付 / 済).
var bugTypes = []asana.CustomType{
	{GID: "ct1", Name: "バグ", StatusOptions: []asana.CustomTypeStatusOption{
		{GID: "cs-done", Name: "完了", CompletionState: "Complete", Color: "green"},
		{GID: "cs-old", Name: "古い", CompletionState: "Incomplete", Enabled: ptrBool(false)},
		{GID: "cs-doing", Name: "対応中", CompletionState: "Incomplete", Color: "blue"},
	}},
	{GID: "ct2", Name: "依頼", StatusOptions: []asana.CustomTypeStatusOption{
		{GID: "cr-new", Name: "受付", CompletionState: "Incomplete"},
		{GID: "cr-done", Name: "済", CompletionState: "Complete"},
	}},
}

func openTypes(t *testing.T) (*Store, context.Context) {
	t.Helper()
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	must(t, err)
	t.Cleanup(func() { st.Close() })
	must(t, st.UpsertProjects(ctx, "w1", []asana.Project{{GID: "p1", Name: "P"}}, "me"))
	must(t, st.ReplaceProjectCustomTypes(ctx, "p1", bugTypes))
	must(t, st.UpsertTasks(ctx, []asana.Task{
		{GID: "t1", Name: "custom", ResourceSubtype: "custom", Workspace: &asana.Ref{GID: "w1"},
			CustomType:             &asana.Ref{GID: "ct1", Name: "バグ"},
			CustomTypeStatusOption: &asana.Ref{GID: "cs-doing", Name: "対応中"},
			Memberships:            []asana.Membership{{Project: &asana.Ref{GID: "p1"}}}},
		{GID: "t2", Name: "plain", Workspace: &asana.Ref{GID: "w1"},
			Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}}}},
	}))
	return st, ctx
}

func TestCustomTypeOfTaskIsShown(t *testing.T) {
	st, ctx := openTypes(t)
	d, err := st.GetTask(ctx, "t1")
	must(t, err)
	if d.CustomType == nil || d.CustomType.Name != "バグ" || d.CustomTypeStatus == nil || d.CustomTypeStatus.Name != "対応中" ||
		d.CustomTypeStatus.Color != "blue" || d.CustomTypeStatus.Complete {
		t.Fatalf("type %+v status %+v", d.CustomType, d.CustomTypeStatus)
	}
	if len(d.CustomTypes) != 2 || d.CustomTypes[0].Name != "バグ" || d.CustomTypes[1].Name != "依頼" {
		t.Fatalf("choices %+v", d.CustomTypes)
	}
	// In the project's order; a status that can no longer be chosen is not offered.
	var names []string
	for _, s := range d.CustomTypes[0].Statuses {
		names = append(names, s.Name)
	}
	if got := strings.Join(names, ","); got != "完了,対応中" {
		t.Fatalf("statuses of バグ = %s", got)
	}
	list, err := st.ListTasks(ctx, TaskFilter{ProjectGID: "p1"})
	must(t, err)
	for _, tk := range list {
		if tk.GID == "t1" && (tk.CustomType == nil || tk.CustomTypeStatus == nil || tk.CustomTypeStatus.Name != "対応中") {
			t.Fatalf("list: %+v", tk)
		}
		if tk.GID == "t2" && (tk.CustomType != nil || tk.CustomTypeStatus != nil) {
			t.Fatalf("plain task has a custom type: %+v", tk)
		}
	}
}

// A task read without the optional fields keeps its type; one that is no longer custom
// loses it.
func TestCustomTypePartialAndCleared(t *testing.T) {
	st, ctx := openTypes(t)
	must(t, st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "custom", ResourceSubtype: "custom", Partial: true,
		Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}}}}}))
	if d, _ := st.GetTask(ctx, "t1"); d.CustomType == nil || d.CustomTypeStatus == nil {
		t.Fatalf("partial read dropped the type: %+v", d.TaskSummary)
	}
	must(t, st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "custom", ResourceSubtype: "default_task",
		CustomType: &asana.Ref{GID: "ct1"}, Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}}}}}))
	if d, _ := st.GetTask(ctx, "t1"); d.CustomType != nil || d.CustomTypeStatus != nil {
		t.Fatalf("default_task kept the type: %+v", d.TaskSummary)
	}
}

// A project's list replaces the last one; a status a task has keeps its name.
func TestReplaceProjectCustomTypes(t *testing.T) {
	st, ctx := openTypes(t)
	must(t, st.ReplaceProjectCustomTypes(ctx, "p1", []asana.CustomType{{GID: "ct1", Name: "不具合", StatusOptions: []asana.CustomTypeStatusOption{
		{GID: "cs-new", Name: "新規", CompletionState: "Incomplete"}}}}))
	d, _ := st.GetTask(ctx, "t1")
	if len(d.CustomTypes) != 1 || d.CustomTypes[0].Name != "不具合" || len(d.CustomTypes[0].Statuses) != 1 {
		t.Fatalf("choices %+v", d.CustomTypes)
	}
	if d.CustomTypeStatus == nil || d.CustomTypeStatus.Name != "対応中" {
		t.Fatalf("the task's status lost its name: %+v", d.CustomTypeStatus)
	}
	must(t, st.ReplaceProjectCustomTypes(ctx, "p1", nil))
	if d, _ := st.GetTask(ctx, "t1"); len(d.CustomTypes) != 0 || d.CustomType == nil {
		t.Fatalf("after none: %+v %+v", d.CustomTypes, d.CustomType)
	}
}

// Asana's names go through termsafe like every other text.
func TestCustomTypeNamesAreTermsafe(t *testing.T) {
	st, ctx := openTypes(t)
	must(t, st.ReplaceProjectCustomTypes(ctx, "p1", []asana.CustomType{{GID: "ct1", Name: "バグ\x1b[31m", StatusOptions: []asana.CustomTypeStatusOption{
		{GID: "cs-doing", Name: "対応\x1b]0;x\x07中"}}}}))
	d, _ := st.GetTask(ctx, "t1")
	for _, s := range []string{d.CustomType.Name, d.CustomTypeStatus.Name, d.CustomTypes[0].Name, d.CustomTypes[0].Statuses[0].Name} {
		if strings.ContainsAny(s, "\x1b\x07") {
			t.Fatalf("control character in %q", s)
		}
	}
}

// A store from before custom task types (user_version 21) gets the columns and tables,
// and keeps its tasks.
func TestMigrateStoreFromBeforeCustomTypes(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "store.db")
	st, err := Open(ctx, path)
	must(t, err)
	must(t, st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "残る"}}))
	rollbackTo(t, st.db, 21)
	st.Close()

	st, err = Open(ctx, path)
	must(t, err)
	defer st.Close()
	must(t, st.ReplaceProjectCustomTypes(ctx, "p1", bugTypes))
	must(t, st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "残る", ResourceSubtype: "custom",
		CustomType: &asana.Ref{GID: "ct1"}, CustomTypeStatusOption: &asana.Ref{GID: "cs-doing"}}}))
	d, err := st.GetTask(ctx, "t1")
	must(t, err)
	if d.CustomType == nil || d.CustomType.Name != "バグ" || d.CustomTypeStatus == nil || d.CustomTypeStatus.Name != "対応中" {
		t.Fatalf("after migrating: %+v %+v", d.CustomType, d.CustomTypeStatus)
	}
}

// A task read without its custom type (#59: the plan refused only those fields) keeps the
// type it has in the cache, and its other optional fields are stored.
func TestCustomTypeNotFetchedIsKept(t *testing.T) {
	st, ctx := openTypes(t)
	must(t, st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "custom", ResourceSubtype: "custom", NoCustomType: true, ApprovalStatus: "",
		Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}}}}}))
	if d, _ := st.GetTask(ctx, "t1"); d.CustomType == nil || d.CustomTypeStatus == nil || d.CustomTypeStatus.Name != "対応中" {
		t.Fatalf("not fetched, yet dropped: %+v", d.TaskSummary)
	}
}
