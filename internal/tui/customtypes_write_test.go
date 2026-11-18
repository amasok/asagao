package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/store"
)

// Custom task types (#59): the type chooser offers the project's types, and a custom task
// gets a chooser of its type's statuses.
func TestCustomTypePickers(t *testing.T) {
	h := newHarness(t, 140, 30)
	st, ctx := h.m.deps.Store.(*store.Store), context.Background()
	if err := st.ReplaceProjectCustomTypes(ctx, "p1", []asana.CustomType{{GID: "ct1", Name: "バグ", StatusOptions: []asana.CustomTypeStatusOption{
		{GID: "cs1", Name: "対応中", CompletionState: "Incomplete"}, {GID: "cs2", Name: "修正済み", CompletionState: "Complete"}}}}); err != nil {
		t.Fatal(err)
	}
	d, _ := st.GetTask(ctx, "t1")
	// A plain task has no status to choose.
	h.m.picker = nil
	h.m.openCustomStatusPicker(*d)
	if h.m.picker != nil {
		t.Fatal("status chooser for a plain task")
	}
	h.m.openSubtypePicker(*d)
	p := h.m.picker
	if p == nil || len(p.items) != 4 || p.items[3].label != "バグ" || p.items[3].hint != "カスタムタイプ" {
		t.Fatalf("type chooser %+v", p)
	}
	cmd, ok := h.m.handleDepPicker(p, p.items[3])
	if !ok {
		t.Fatal("not handled")
	}
	h.run(cmd)
	d, _ = st.GetTask(ctx, "t1")
	if d.CustomType == nil || d.CustomType.Name != "バグ" || d.CustomTypeStatus == nil || d.CustomTypeStatus.Name != "対応中" {
		t.Fatalf("after choosing: %+v %+v", d.CustomType, d.CustomTypeStatus)
	}
	h.m.openSubtypePicker(*d)
	if p := h.m.picker; p.cursor != 3 || p.items[3].hint != "現在" {
		t.Fatalf("current type not marked: %+v", p)
	}
	h.m.openCustomStatusPicker(*d)
	p = h.m.picker
	if p == nil || p.kind != pickTypeStatus || len(p.items) != 2 || p.cursor != 0 || p.items[1].hint != "完了" {
		t.Fatalf("status chooser %+v", p)
	}
	cmd, _ = h.m.handleDepPicker(p, p.items[1])
	h.run(cmd)
	if d, _ = st.GetTask(ctx, "t1"); d.CustomTypeStatus.Name != "修正済み" || !d.Completed {
		t.Fatalf("after status: %+v completed=%v", d.CustomTypeStatus, d.Completed)
	}
	if got := h.m.status; got != "ステータスを「修正済み」にしました" {
		t.Errorf("status line %q", got)
	}
	// The detail shows the type by its name, and the status as an item of its own.
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
