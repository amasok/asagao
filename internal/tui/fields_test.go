package tui

import (
	"context"
	"testing"

	"github.com/amasok/asana-tui/internal/asana"
	"github.com/amasok/asana-tui/internal/store"
)

// Two quick toggles in a multi_enum chooser keep both: the second is made on the updated
// chooser and saved after the first.
func TestMultiEnumQuickTogglesKeepEachOther(t *testing.T) {
	h := newHarness(t, 140, 30)
	st, ctx := h.m.deps.Store, context.Background()
	if err := st.ReplaceProjectCustomFields(ctx, "p1", []asana.CustomFieldSetting{{CustomField: asana.CustomField{
		GID: "cf", Name: "ラベル", ResourceSubtype: store.CFMultiEnum,
		EnumOptions: []asana.EnumOption{{GID: "a", Name: "A"}, {GID: "b", Name: "B"}}}}}); err != nil {
		t.Fatal(err)
	}
	d, err := st.GetTask(ctx, "t1")
	if err != nil || len(d.CustomFields) != 1 {
		t.Fatalf("task = %+v, %v", d, err)
	}
	h.m.showFieldPicker(d.TaskSummary, d.CustomFields[0])
	p := h.m.picker
	first := h.m.toggleFieldOption(p, p.items[0])  // A on: being saved
	second := h.m.toggleFieldOption(p, p.items[1]) // B on: queued, with A
	if second != nil || !p.items[0].checked || !p.items[1].checked {
		t.Fatalf("second toggle: cmd %v, items %+v", second, p.items)
	}
	h.run(first) // saves A, then the queued A+B
	d, _ = st.GetTask(ctx, "t1")
	if got := d.CustomFields[0].Value; !sameStrings(got, []string{"a", "b"}) {
		t.Fatalf("value = %v", got)
	}
	if h.m.fieldSaving || h.m.fieldQueued != nil {
		t.Fatal("nothing should be left to save")
	}
}

func sameStrings(v any, want []string) bool {
	got, ok := v.([]string)
	if !ok || len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestFieldPromptStartsWithRawNumber(t *testing.T) {
	f := store.CustomFieldView{Type: store.CFNumber, Value: 1000.0, Display: "¥1,000"}
	if got := fieldInput(f); got != "1000" {
		t.Fatalf("input = %q", got)
	}
}
