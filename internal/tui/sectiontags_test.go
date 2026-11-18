package tui

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/store"
)

// openProjectList opens 新居プロジェクト (p1) as a list, on t1 (家具).
func openProjectList(t *testing.T, st *store.Store) *harness {
	t.Helper()
	m := New(context.Background(), Deps{Store: st, Now: func() time.Time { return testNow }})
	h := &harness{t: t, m: m}
	h.send(tea.WindowSizeMsg{Width: 140, Height: 30})
	h.run(m.Init())
	h.key("h", "j", "j", "l")
	if h.m.currentProject() != "p1" || h.selected() != "t1" {
		t.Fatalf("not on t1 in p1: project %q, selected %q", h.m.currentProject(), h.selected())
	}
	return h
}

func typeText(h *harness, s string) {
	for _, r := range s {
		h.key(string(r))
	}
}

func opKindsOf(h *harness) []string {
	var out []string
	for _, op := range h.ops() {
		out = append(out, op.Kind)
	}
	return out
}

// A section is added after the one the cursor is in, shown at once even though it is
// empty, and can be selected and deleted (after confirming). Never sent, nothing is left
// to send.
func TestTUIAddSelectAndDeleteSection(t *testing.T) {
	st := fixtureStore(t)
	h := openProjectList(t, st)
	h.key("space", "n")
	typeText(h, "保留")
	h.key("enter")
	if got := opKindsOf(h); !slices.Equal(got, []string{store.OpCreateSection}) {
		t.Fatalf("ops = %v (status %q)", got, h.m.status)
	}
	s := h.screen()
	if i, j, k := strings.Index(s, "家具"), strings.Index(s, "保留"), strings.Index(s, "手続き"); !(i >= 0 && i < j && j < k) {
		t.Fatalf("the empty section is not shown after 家具:\n%s", s)
	}
	h.key("j") // from t1 to the empty 保留
	if _, gid, name := h.m.sectionHere(); name != "保留" || !store.IsLocalGID(gid) {
		t.Fatalf("on %q (%s), want the new section", name, gid)
	}
	h.key("space", "H")
	if h.m.picker == nil || !strings.Contains(h.m.picker.title, "保留") {
		t.Fatalf("no confirmation: %+v", h.m.picker)
	}
	h.key("enter") // やめる is first
	if got := opKindsOf(h); len(got) != 1 {
		t.Fatalf("cancelled, but ops = %v", got)
	}
	h.key("space", "H", "j", "enter")
	if got := opKindsOf(h); len(got) != 0 {
		t.Fatalf("ops = %v, want none (created and deleted offline)", got)
	}
	for _, r := range h.m.rows {
		if r.header == "保留" {
			t.Error("deleted section still shown")
		}
	}
}

// A section with tasks is not deleted; moving one down queues the move.
func TestTUIMoveSectionAndRefuseNonEmpty(t *testing.T) {
	st := fixtureStore(t)
	h := openProjectList(t, st)
	h.key("space", "H", "j", "enter")
	if got := opKindsOf(h); len(got) != 0 || !strings.Contains(h.m.status, "タスクが残って") {
		t.Fatalf("ops = %v, status %q", got, h.m.status)
	}
	h.key("space", "J")
	ops := h.ops()
	if len(ops) != 1 || ops[0].Kind != store.OpInsertSection || ops[0].TaskGID != "s1" || ops[0].Payload["after"] != "s2" {
		t.Fatalf("ops = %+v", ops)
	}
	s := h.screen()
	if strings.Index(s, "手続き") > strings.Index(s, "家具") {
		t.Errorf("家具 not moved below 手続き:\n%s", s)
	}
	h.key("space", "J") // already last
	if len(h.ops()) != 1 || !strings.Contains(h.m.status, "これ以上") {
		t.Errorf("ops = %v, status %q", opKindsOf(h), h.m.status)
	}
}

// Tags: rename, recolor and delete (after confirming) from the tag manager.
func TestTUITagManager(t *testing.T) {
	st := fixtureStore(t)
	ctx := context.Background()
	if err := st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "ソファを買う", DueOn: "2026-11-20", Assignee: &asana.Ref{GID: "me"},
		Tags:        []asana.Ref{{GID: "g1", Name: "急ぎ"}},
		Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}, Section: &asana.Ref{GID: "s1"}}}}}); err != nil {
		t.Fatal(err)
	}
	h := openProjectList(t, st)
	h.key("space", "y", "enter") // 急ぎ
	if h.m.picker == nil || h.m.picker.kind != pickTagAction {
		t.Fatalf("picker = %+v", h.m.picker)
	}
	h.key("j", "enter") // 色を変える
	h.key("j", "j", "enter")
	if g, _ := st.GetTag(ctx, "g1"); g.Color != "dark-green" {
		t.Fatalf("g1 = %+v (status %q)", g, h.m.status)
	}
	h.key("space", "y", "enter", "j", "j", "enter") // 削除する
	if h.m.picker == nil || !strings.Contains(h.m.picker.title, "1 件") {
		t.Fatalf("no confirmation with the task count: %+v", h.m.picker)
	}
	h.key("j", "enter")
	if _, err := st.GetTag(ctx, "g1"); err == nil {
		t.Fatal("tag not deleted")
	}
	if got := opKindsOf(h); !slices.Equal(got, []string{store.OpDeleteTag}) {
		t.Errorf("ops = %v (the unsent color change goes with the tag)", got)
	}
}

// After Asana refused a feature with a 402 its entries leave the menu.
func TestTUIHidesUnavailableFeatures(t *testing.T) {
	st := fixtureStore(t)
	h := openProjectList(t, st)
	labels := func() string {
		var out []string
		for _, a := range h.m.available() {
			out = append(out, a.label)
		}
		return strings.Join(out, "\n")
	}
	if !strings.Contains(labels(), "セクションを追加") {
		t.Fatalf("menu without セクションを追加:\n%s", labels())
	}
	if err := st.SetSyncState(context.Background(), readmodel.KeyPlanUnavailable+readmodel.PlanSections, "x"); err != nil {
		t.Fatal(err)
	}
	h.run(h.m.loadOutbox())
	s := labels()
	h.key("space")
	if strings.Contains(s, "セクションを追加") || strings.Contains(s, "空のセクションを削除") {
		t.Errorf("unavailable entries still in the menu:\n%s", s)
	}
	if !strings.Contains(s, "セクション名を変更") || !strings.Contains(s, "タグの名前・色") {
		t.Errorf("other entries gone:\n%s", s)
	}
	h.key("n")
	if got := opKindsOf(h); len(got) != 0 || h.m.promptMode != promptNone {
		t.Errorf("n still works: ops %v, prompt %v", got, h.m.promptMode)
	}
}
