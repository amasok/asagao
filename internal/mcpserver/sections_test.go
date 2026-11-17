package mcpserver

import (
	"context"
	"slices"
	"testing"

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
