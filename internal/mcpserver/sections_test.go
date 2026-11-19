package mcpserver

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/store"
)

// move_task and rename_section change the local copy and queue one change each.
func TestMoveTaskAndRenameSectionQueue(t *testing.T) {
	st := seed(t)
	ctx := context.Background()
	cs := connect(t, &Server{Store: st})
	var w WriteOutput
	if res := call(t, cs, "move_task", map[string]any{"gid": "t1", "section": "手続き", "project": "新居"}, &w); res.IsError {
		t.Fatal(toolError(res))
	}
	if d, _ := st.GetTask(ctx, "t1"); len(d.Memberships) != 1 || d.Memberships[0].SectionGID != "s2" {
		t.Errorf("t1 is in %+v, want 手続き (s2)", d.Memberships)
	}
	if res := call(t, cs, "rename_section", map[string]any{"project": "新居", "section": "家具", "name": "大型家具"}, &w); res.IsError {
		t.Fatal(toolError(res))
	}
	want := []string{store.OpMoveTask + ":t1", store.OpRenameSection + ":s1"}
	if got := opList(t, st); !slices.Equal(got, want) {
		t.Fatalf("ops = %v, want %v", got, want)
	}
	if name, err := st.SectionName(ctx, "s1"); err != nil || name != "大型家具" {
		t.Errorf("s1 is named %q (%v), want 大型家具", name, err)
	}
}

// create_section makes a section tasks can be moved to at once (offline too); move_section
// and delete_section queue their change; update_tag / delete_tag change the tag itself.
func TestSectionAndTagTools(t *testing.T) {
	st := seed(t)
	if err := st.UpsertTags(context.Background(), []asana.Tag{{GID: "g1", Name: "急ぎ"}}); err != nil {
		t.Fatal(err)
	}
	cs := connect(t, &Server{Store: st})
	var w WriteOutput
	if res := call(t, cs, "create_section", map[string]any{"project": "新居", "name": "保留", "after": "家具"}, &w); res.IsError {
		t.Fatal(toolError(res))
	}
	if !store.IsLocalGID(w.SectionGID) {
		t.Fatalf("section_gid = %q, want the local gid (offline)", w.SectionGID)
	}
	if res := call(t, cs, "move_task", map[string]any{"gid": "t1", "section": w.SectionGID, "project": "新居"}, &w); res.IsError {
		t.Fatal(toolError(res))
	}
	if res := call(t, cs, "move_section", map[string]any{"project": "新居", "section": "手続き", "before": "家具"}, &w); res.IsError {
		t.Fatal(toolError(res))
	}
	if res := call(t, cs, "move_section", map[string]any{"project": "新居", "section": "手続き"}, &w); !res.IsError {
		t.Error("move_section without before / after: no error")
	}
	if res := call(t, cs, "update_tag", map[string]any{"tag": "急ぎ", "color": "light-green"}, &w); res.IsError {
		t.Fatal(toolError(res))
	}
	if res := call(t, cs, "update_tag", map[string]any{"tag": "急ぎ", "color": "green"}, &w); !res.IsError {
		t.Error("update_tag with an unknown color: no error")
	}
	if res := call(t, cs, "update_tag", map[string]any{"tag": "急ぎ"}, &w); !res.IsError {
		t.Error("update_tag without a change: no error")
	}
	if res := call(t, cs, "delete_tag", map[string]any{"tag": "急ぎ"}, &w); res.IsError {
		t.Fatal(toolError(res))
	}
	got := opList(t, st)
	// The unsent color change of a tag deleted afterwards is not sent.
	want := []string{store.OpCreateSection, store.OpMoveTask + ":t1", store.OpInsertSection + ":s2", store.OpDeleteTag + ":g1"}
	if len(got) != len(want) {
		t.Fatalf("ops = %v, want %v", got, want)
	}
	for i := range want {
		if !strings.HasPrefix(got[i], want[i]) {
			t.Fatalf("ops = %v, want %v", got, want)
		}
	}
	// A section holding a task is not deleted.
	if res := call(t, cs, "delete_section", map[string]any{"project": "新居", "section": w.SectionGID}, &w); !res.IsError {
		t.Error("delete_section of a section with a task: no error")
	}
}

// A feature Asana refused with a 402 is not offered at all.
func TestSectionToolsHiddenAfterPaymentRequired(t *testing.T) {
	st := seed(t)
	ctx := context.Background()
	wss, err := st.WorkspaceGIDs(ctx)
	if err != nil || len(wss) == 0 {
		t.Fatalf("workspaces = %v, %v", wss, err)
	}
	at := testNow.Add(-time.Hour).UTC().Format(time.RFC3339)
	for _, ws := range wss { // refused in every workspace
		if err := st.SetSyncState(ctx, readmodel.PlanKey(readmodel.PlanSections, ws), at); err != nil {
			t.Fatal(err)
		}
	}
	cs := connect(t, &Server{Store: st})
	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	for _, hidden := range []string{"create_section", "move_section", "delete_section"} {
		if slices.Contains(names, hidden) {
			t.Errorf("%s offered after a 402", hidden)
		}
	}
	for _, kept := range []string{"rename_section", "update_tag", "delete_tag"} {
		if !slices.Contains(names, kept) {
			t.Errorf("%s not offered", kept)
		}
	}
}

// move_task with my_tasks moves within the sections of My Tasks; list_my_sections lists them.
func TestMoveTaskMyTasks(t *testing.T) {
	st := seed(t)
	ctx := context.Background()
	if err := st.ReplaceMyTaskSections(ctx, "w1", []asana.Section{{GID: "m1", Name: "今日やる"}, {GID: "m2", Name: "あとで"}}); err != nil {
		t.Fatal(err)
	}
	cs := connect(t, &Server{Store: st})
	var secs MySectionsOutput
	if res := call(t, cs, "list_my_sections", map[string]any{}, &secs); res.IsError || len(secs.Sections) != 2 {
		t.Fatalf("list_my_sections = %+v", secs)
	}
	var w WriteOutput
	if res := call(t, cs, "move_task", map[string]any{"gid": "t1", "section": "今日やる", "my_tasks": true, "project": "新居"}, &w); !res.IsError {
		t.Error("my_tasks with project: no error")
	}
	if res := call(t, cs, "move_task", map[string]any{"gid": "t3", "section": "今日やる", "my_tasks": true}, &w); !res.IsError {
		t.Error("moving another's task within My Tasks: no error")
	}
	if res := call(t, cs, "move_task", map[string]any{"gid": "t1", "section": "今日やる", "my_tasks": true}, &w); res.IsError {
		t.Fatal(toolError(res))
	}
	if got := opList(t, st); !slices.Equal(got, []string{store.OpMoveMySection + ":t1"}) {
		t.Fatalf("ops = %v", got)
	}
	var list struct {
		Tasks []readmodel.TaskSummary `json:"tasks"`
	}
	if res := call(t, cs, "list_tasks", map[string]any{}, &list); res.IsError {
		t.Fatal(toolError(res))
	}
	for _, tk := range list.Tasks {
		if tk.GID == "t1" && (tk.MySection == nil || tk.MySection.Name != "今日やる") {
			t.Errorf("t1 in list_tasks: my_section = %+v", tk.MySection)
		}
	}
}
