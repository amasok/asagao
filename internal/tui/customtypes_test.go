package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/amasok/asagao/internal/asana"
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
	h.m.detail = d
	out := ansi.Strip(h.m.renderDetail(100))
	if !strings.Contains(out, "バグ") || !strings.Contains(out, "修正済み") {
		t.Fatalf("detail:\n%s", out)
	}
	found := false
	for _, it := range h.m.detailItems {
		found = found || it.kind == itemCustomStatus
	}
	if !found {
		t.Fatal("no status item")
	}
}
