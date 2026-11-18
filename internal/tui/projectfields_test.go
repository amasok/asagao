package tui

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/store"
)

func setupFieldHarness(t *testing.T, subtype string) (*harness, *store.Store) {
	t.Helper()
	h := newHarness(t, 140, 30)
	st, ctx := h.m.deps.Store.(*store.Store), context.Background()
	settings := []asana.CustomFieldSetting{{CustomField: asana.CustomField{GID: "cf", Name: "優先度", ResourceSubtype: subtype,
		EnumOptions: []asana.EnumOption{{GID: "a", Name: "高"}}}}}
	if err := st.ReplaceProjectCustomFields(ctx, "p1", settings); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceWorkspaceCustomFields(ctx, "w1", []asana.CustomField{settings[0].CustomField,
		{GID: "cm", Name: "メモ", ResourceSubtype: store.CFText}}); err != nil {
		t.Fatal(err)
	}
	return h, st
}

func (h *harness) field(gid string) readmodel.CustomFieldView {
	h.t.Helper()
	d, err := h.m.deps.Store.GetTask(context.Background(), "t1")
	if err != nil {
		h.t.Fatal(err)
	}
	for _, f := range d.CustomFields {
		if f.GID == gid {
			return f
		}
	}
	h.t.Fatalf("no field %s", gid)
	return readmodel.CustomFieldView{}
}

func (h *harness) pickLabel(label string) {
	h.t.Helper()
	i := slices.IndexFunc(h.m.picker.items, func(it pickerItem) bool { return strings.Contains(it.label, label) })
	if i < 0 {
		h.t.Fatalf("no %q in %+v", label, h.m.picker.items)
	}
	h.m.picker.cursor = i
	h.key("enter")
}

// The chooser of an enum field offers to add an option; the name typed is added and set.
func TestNewOptionFromChooser(t *testing.T) {
	for _, subtype := range []string{store.CFEnum, store.CFMultiEnum} {
		t.Run(subtype, func(t *testing.T) {
			h, st := setupFieldHarness(t, subtype)
			ctx := context.Background()
			if subtype == store.CFMultiEnum {
				if _, err := st.SetCustomFieldLocal(ctx, "t1", "cf", []string{"a"}, testNow); err != nil {
					t.Fatal(err)
				}
			}
			d, _ := st.GetTask(ctx, "t1")
			h.m.showFieldPicker(d.TaskSummary, h.field("cf"))
			h.pickLabel("選択肢を足して設定する")
			if h.m.promptMode != promptFieldOption {
				t.Fatalf("prompt = %v", h.m.promptMode)
			}
			h.typeText("低")
			h.key("enter")
			f := h.field("cf")
			want := "低"
			if subtype == store.CFMultiEnum {
				want = "高, 低"
			}
			if f.Display != want {
				t.Fatalf("field = %+v; status %q", f, h.m.status)
			}
			if !slices.ContainsFunc(f.Options, func(o readmodel.EnumOptionView) bool { return o.Name == "低" && store.IsLocalGID(o.GID) }) {
				t.Fatalf("options = %+v", f.Options)
			}
			if !slices.ContainsFunc(h.ops(), func(o store.Op) bool { return o.Kind == store.OpCreateEnumOption }) {
				t.Fatalf("ops = %+v", h.ops())
			}
		})
	}
}

// Not offered when Asana refused it (402 for the plan, 403 for the field).
func TestNewOptionNotOfferedWhenRefused(t *testing.T) {
	for _, key := range []string{readmodel.KeyPlanUnavailable + readmodel.PlanCustomFields, readmodel.KeyFieldOptionsLocked + "cf"} {
		h, st := setupFieldHarness(t, store.CFEnum)
		ctx := context.Background()
		if err := st.SetSyncState(ctx, key, "2026-11-13T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
		d, _ := st.GetTask(ctx, "t1")
		h.m.showFieldPicker(d.TaskSummary, h.field("cf"))
		if slices.ContainsFunc(h.m.picker.items, func(it pickerItem) bool { return it.value == newOptionValue }) {
			t.Fatalf("%s: offered", key)
		}
	}
}

// space G lists the workspace's fields; one is added at once, removing asks first.
func TestProjectFieldsToggle(t *testing.T) {
	h, _ := setupFieldHarness(t, store.CFEnum)
	h.run(h.m.loadProjectFields("p1"))
	if h.m.picker == nil || h.m.picker.kind != pickProjectFields || len(h.m.picker.items) != 2 {
		t.Fatalf("picker = %+v; status %q", h.m.picker, h.m.status)
	}
	h.pickLabel("メモ")
	if f := h.field("cm"); !f.Editable {
		t.Fatalf("not added: %+v", f)
	}
	if h.m.picker == nil || !h.m.picker.items[1].checked || !strings.Contains(h.m.picker.items[1].hint, "未送信") {
		t.Fatalf("picker after add = %+v", h.m.picker)
	}
	h.pickLabel("優先度")
	if h.m.picker.kind != pickRemoveField {
		t.Fatalf("no confirmation: %+v", h.m.picker)
	}
	h.pickLabel("やめる")
	fields, _ := h.m.deps.Store.ProjectFields(context.Background(), "p1")
	if !fields[0].Attached {
		t.Fatalf("removed without asking: %+v", fields)
	}
	h.run(h.m.loadProjectFields("p1"))
	h.pickLabel("優先度")
	h.pickLabel("外す")
	fields, _ = h.m.deps.Store.ProjectFields(context.Background(), "p1")
	for _, f := range fields {
		if f.GID == "cf" && f.Attached {
			t.Fatalf("not removed: %+v", fields)
		}
	}
}

// A project whose fields Asana refused to change (403) says so instead of listing them.
func TestProjectFieldsLocked(t *testing.T) {
	h, st := setupFieldHarness(t, store.CFEnum)
	if err := st.SetSyncState(context.Background(), readmodel.KeyProjectFieldsLocked+"p1", "x"); err != nil {
		t.Fatal(err)
	}
	h.run(h.m.loadProjectFields("p1"))
	if h.m.picker != nil || !strings.Contains(h.m.status, "403") {
		t.Fatalf("picker %+v, status %q", h.m.picker, h.m.status)
	}
}
