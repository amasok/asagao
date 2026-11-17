package tui

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/store"
)

func opList(t *testing.T, st *store.Store) []string {
	t.Helper()
	var out []string
	for _, op := range opsOf(t, st) {
		out = append(out, op.Kind+":"+op.TaskGID)
	}
	return out
}

// Linking a dependency from the TUI queues it on the task that waits (the other one for a
// dependent), and unlinking cancels the unsent change.
func TestSetDependencyQueues(t *testing.T) {
	h := newHarness(t, 140, 30)
	st, ctx := h.m.deps.Store.(*store.Store), context.Background()
	d, err := st.GetTask(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	h.run(h.m.setDependency(d.TaskSummary, "t2", false, true))
	h.run(h.m.setDependency(d.TaskSummary, "t3", true, true))
	want := []string{store.OpAddDependency + ":t1", store.OpAddDependency + ":t3"}
	if got := opList(t, st); !slices.Equal(got, want) {
		t.Fatalf("ops = %v, want %v", got, want)
	}
	if !strings.Contains(h.m.status, "依存関係を追加しました") {
		t.Errorf("status = %q", h.m.status)
	}
	h.run(h.m.setDependency(d.TaskSummary, "t3", true, false))
	if got := opList(t, st); !slices.Equal(got, want[:1]) {
		t.Errorf("after unlinking: ops = %v, want %v", got, want[:1])
	}
	h.run(h.m.setDependency(d.TaskSummary, "t1", false, true)) // itself: refused
	if got := opList(t, st); !slices.Equal(got, want[:1]) {
		t.Errorf("a link to itself was queued: %v", got)
	}
}

// A value typed for a custom field is read with the time the key was pressed (+7d from
// then) and queued; a value that does not read writes nothing.
func TestSubmitFieldReadsAndQueues(t *testing.T) {
	h := newHarness(t, 140, 30)
	st, ctx := h.m.deps.Store.(*store.Store), context.Background()
	if err := st.ReplaceProjectCustomFields(ctx, "p1", []asana.CustomFieldSetting{
		{CustomField: asana.CustomField{GID: "cf-date", Name: "締切", ResourceSubtype: store.CFDate}},
		{CustomField: asana.CustomField{GID: "cf-num", Name: "見積もり", ResourceSubtype: store.CFNumber}},
	}); err != nil {
		t.Fatal(err)
	}
	d, err := st.GetTask(ctx, "t1")
	if err != nil || len(d.CustomFields) != 2 {
		t.Fatalf("task = %+v, %v", d, err)
	}
	field := func(gid string) store.CustomFieldView {
		for _, f := range d.CustomFields {
			if f.GID == gid {
				return f
			}
		}
		t.Fatalf("no field %s", gid)
		return store.CustomFieldView{}
	}
	h.m.fieldTask, h.m.fieldEdit = d.TaskSummary, field("cf-num")
	h.run(h.m.submitField("数")) // not a number
	if got := opList(t, st); len(got) != 0 {
		t.Fatalf("an unreadable value was queued: %v", got)
	}
	h.m.fieldTask, h.m.fieldEdit = d.TaskSummary, field("cf-date")
	h.run(h.m.submitField("+7d"))
	if got := opList(t, st); !slices.Equal(got, []string{store.OpSetCustomField + ":t1"}) {
		t.Fatalf("ops = %v", got)
	}
	d, _ = st.GetTask(ctx, "t1")
	wantDate := testNow.AddDate(0, 0, 7).Format("2006-01-02")
	for _, f := range d.CustomFields {
		if f.GID == "cf-date" && f.Value != wantDate {
			t.Errorf("date = %v, want %s", f.Value, wantDate)
		}
	}
	if !strings.Contains(h.m.status, "「締切」を") {
		t.Errorf("status = %q", h.m.status)
	}
}

// Toggling a follower and a tag from the choosers queues the change; toggling back
// cancels it.
func TestToggleFollowerAndTagQueue(t *testing.T) {
	h := newHarness(t, 140, 30)
	st, ctx := h.m.deps.Store.(*store.Store), context.Background()
	if err := st.UpsertTags(ctx, []asana.Ref{{GID: "g1", Name: "急ぎ"}}); err != nil {
		t.Fatal(err)
	}
	d, err := st.GetTask(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	h.run(h.m.toggleFollower(d.TaskSummary, pickerItem{value: "u2"}))
	h.run(h.m.toggleTag(d.TaskSummary, pickerItem{value: "g1"}))
	h.m.tagTask = d.TaskSummary
	h.run(h.m.submitNewTag("新しいタグ"))
	want := []string{store.OpAddFollower + ":t1", store.OpAddTag + ":t1", store.OpAddTag + ":t1"}
	if got := opList(t, st); !slices.Equal(got, want) {
		t.Fatalf("ops = %v, want %v", got, want)
	}
	h.run(h.m.toggleFollower(d.TaskSummary, pickerItem{value: "u2", checked: true}))
	h.run(h.m.toggleTag(d.TaskSummary, pickerItem{value: "g1", checked: true}))
	if got := opList(t, st); !slices.Equal(got, want[2:]) {
		t.Errorf("after toggling back: ops = %v, want %v", got, want[2:])
	}
}

// Duplicating and attaching from the TUI queue a copy and the file, copied into the
// cache directory the TUI was given.
func TestDuplicateAndAttachQueue(t *testing.T) {
	cache := t.TempDir()
	m := New(context.Background(), Deps{Store: fixtureStore(t), Now: func() time.Time { return testNow }, CacheDir: cache})
	h := &harness{t: t, m: m}
	h.send(tea.WindowSizeMsg{Width: 140, Height: 30})
	h.run(m.Init())
	st, ctx := m.deps.Store.(*store.Store), context.Background()
	m.dupSource = "t1"
	h.run(m.duplicate("ソファを買う (2)"))
	file := filepath.Join(t.TempDir(), "図面.png")
	if err := os.WriteFile(file, []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.attachTask = "t1"
	h.run(m.attach(`"` + file + `"`))
	ops := opsOf(t, st)
	if len(ops) != 2 || ops[0].Kind != store.OpDuplicateTask || !store.IsLocalGID(ops[0].TaskGID) ||
		ops[1].Kind != store.OpUploadAttachment || ops[1].TaskGID != "t1" {
		t.Fatalf("ops = %v", opList(t, st))
	}
	if c, err := st.GetTask(ctx, ops[0].TaskGID); err != nil || c.Name != "ソファを買う (2)" {
		t.Errorf("copy = %+v, %v", c, err)
	}
	d, _ := st.GetTask(ctx, "t1")
	if len(d.Attachments) != 1 || !strings.HasPrefix(d.Attachments[0].LocalPath, cache) {
		t.Errorf("attachments = %+v, want one under %s", d.Attachments, cache)
	}
}
