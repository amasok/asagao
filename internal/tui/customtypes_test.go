package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/store"
)

// Custom task types (#59): the detail shows the type by its name, and the status as an
// item of its own.
func TestCustomTypeInDetail(t *testing.T) {
	h := newHarness(t, 140, 30)
	st, ctx := h.m.deps.Store.(*store.Store), context.Background()
	if err := st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "custom", ResourceSubtype: "custom", Workspace: &asana.Ref{GID: "w1"},
		Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}}},
		CustomType:  &asana.Ref{GID: "ct1", Name: "バグ"}, CustomTypeStatusOption: &asana.Ref{GID: "cs2", Name: "修正済み"}}}); err != nil {
		t.Fatal(err)
	}
	d, _ := st.GetTask(ctx, "t1")
	h.m.detail.task = d
	out := ansi.Strip(h.m.renderDetail(100))
	if !strings.Contains(out, "バグ") || !strings.Contains(out, "修正済み") {
		t.Fatalf("detail:\n%s", out)
	}
	found := false
	for _, it := range h.m.detail.items {
		found = found || it.kind == itemCustomStatus
	}
	if !found {
		t.Fatal("no status item")
	}
}

// The timeline's name column shows a custom type and its status as the list does, and a
// milestone's ◆ once.
func TestTimelineNameShowsCustomType(t *testing.T) {
	h := newHarness(t, 140, 30)
	custom := readmodel.TaskSummary{GID: "x", Name: "落ちる", ResourceSubtype: "custom",
		CustomType: &readmodel.CustomTypeRef{GID: "ct1", Name: "バグ"}, CustomTypeStatus: &readmodel.CustomTypeStatusView{GID: "cs1", Name: "対応中"}}
	if got := ansi.Strip(h.m.tlNameCell(&custom, 0, 40, false, false)); !strings.Contains(got, "落ちる [バグ: 対応中]") {
		t.Fatalf("custom %q", got)
	}
	ms := readmodel.TaskSummary{GID: "y", Name: "リリース", ResourceSubtype: "milestone"}
	if got := ansi.Strip(h.m.tlNameCell(&ms, 0, 40, false, false)); strings.Count(got, "◆") != 1 {
		t.Fatalf("milestone %q", got)
	}
}
