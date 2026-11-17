package tui

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/store"
)

// markRows marks the tasks with the given gids by moving the cursor onto each and pressing V.
func (h *harness) markRows(gids ...string) {
	h.t.Helper()
	for _, gid := range gids {
		h.m.selectInList(gid)
		if h.selected() != gid {
			h.t.Fatalf("%s is not in the list: %v", gid, h.visibleGIDs())
		}
		h.key("V")
	}
}

func (h *harness) ops() []store.Op {
	h.t.Helper()
	ops, err := h.m.deps.Store.(*store.Store).ListOps(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	return ops
}

func (h *harness) footer() string { return h.screen()[strings.LastIndex(h.screen(), "\n")+1:] }

func TestBulkCompleteAsksThenQueuesEachTask(t *testing.T) {
	h := newHarness(t, 140, 30)
	h.markRows("t2", "t3")
	if s := h.screen(); !strings.Contains(s, "印 2 件") || strings.Count(s, "●") != 2 {
		t.Fatalf("marks should be shown:\n%s", s)
	}
	h.key("x")
	if f := h.footer(); !strings.Contains(f, "2 件を完了にします (y/n)") {
		t.Fatalf("should ask first, footer=%q", f)
	}
	if ops := h.ops(); len(ops) != 0 {
		t.Fatalf("nothing should be written before y: %+v", ops)
	}
	h.key("j") // other keys keep asking
	if h.m.bulkAsk == nil {
		t.Fatal("a non-answer key must not drop the question")
	}
	h.key("y")
	ops := h.ops()
	if len(ops) != 2 {
		t.Fatalf("one outbox entry per task, got %+v", ops)
	}
	for _, gid := range []string{"t2", "t3"} {
		d, err := h.m.deps.Store.GetTask(context.Background(), gid)
		if err != nil || !d.Completed || !d.Pending {
			t.Errorf("%s should be completed locally and pending: %+v %v", gid, d.TaskSummary, err)
		}
	}
	if d, _ := h.m.deps.Store.GetTask(context.Background(), "t1"); d.Completed {
		t.Error("an unmarked task must not change")
	}
	if len(h.m.marked) != 0 {
		t.Fatalf("marks should be cleared after running: %v", h.m.marked)
	}
	if !strings.Contains(h.footer(), "2 件を完了にしました") {
		t.Fatalf("result footer=%q", h.footer())
	}
}

func TestBulkCancelKeepsMarksAndWritesNothing(t *testing.T) {
	h := newHarness(t, 140, 30)
	h.markRows("t2", "t3")
	h.key("x", "n")
	if ops := h.ops(); len(ops) != 0 {
		t.Fatalf("n must not write: %+v", ops)
	}
	if len(h.m.marked) != 2 || h.m.bulkAsk != nil {
		t.Fatalf("cancel keeps the marks: %v", h.m.marked)
	}
	h.key("esc") // esc in the list unmarks
	if len(h.m.marked) != 0 {
		t.Fatalf("esc should clear the marks: %v", h.m.marked)
	}
}

func TestBulkReopenWhenAllCompleted(t *testing.T) {
	h := newHarness(t, 140, 30)
	h.chooseFilter(filterCompleted)
	h.markRows("t5")
	h.key("x")
	if !strings.Contains(h.footer(), "1 件を未完了に戻します") {
		t.Fatalf("footer=%q", h.footer())
	}
	h.key("y")
	if d, _ := h.m.deps.Store.GetTask(context.Background(), "t5"); d.Completed {
		t.Fatal("t5 should be reopened")
	}
}

// Marks of tasks that leave the list (search, filter, reload) are dropped, so nothing
// unseen is changed.
func TestBulkMarksOfHiddenTasksAreDropped(t *testing.T) {
	h := newHarness(t, 140, 30)
	h.markRows("t2", "t1")
	h.key("/")
	h.typeText("ソファ")
	h.key("enter")
	if !reflectKeys(h.m.marked, "t1") {
		t.Fatalf("only the task still shown keeps its mark: %v", h.m.marked)
	}
	h.key("x", "y")
	if d, _ := h.m.deps.Store.GetTask(context.Background(), "t2"); d.Completed {
		t.Fatal("a task hidden by the search must not be completed")
	}
	if d, _ := h.m.deps.Store.GetTask(context.Background(), "t1"); !d.Completed {
		t.Fatal("the marked task shown should be completed")
	}
}

func TestBulkMarksDroppedWhenGroupCollapsed(t *testing.T) {
	h := projectHarness(t, fixtureStore(t), nil)
	h.markRows("t1", "t2")
	h.m.selectInList("t1")
	h.key("-") // collapse 家具 (t1)
	if !reflectKeys(h.m.marked, "t2") {
		t.Fatalf("a collapsed task loses its mark: %v", h.m.marked)
	}
}

func reflectKeys(m map[string]bool, want ...string) bool {
	var got []string
	for k := range m {
		got = append(got, k)
	}
	slices.Sort(got)
	slices.Sort(want)
	return slices.Equal(got, want)
}

func TestBulkMarkAllToggles(t *testing.T) {
	h := newHarness(t, 140, 30)
	h.key("*")
	if n := len(h.m.markedTasks()); n != len(h.visibleGIDs()) || n == 0 {
		t.Fatalf("* marks every task shown: %d of %v", n, h.visibleGIDs())
	}
	h.key("*")
	if len(h.m.marked) != 0 {
		t.Fatalf("* again unmarks all: %v", h.m.marked)
	}
}

// One task that cannot be written does not stop the others; the result says how many and why.
func TestBulkMoveContinuesPastAFailure(t *testing.T) {
	h := projectHarness(t, fixtureStore(t), nil)
	h.markRows("t1", "t3", "t4")
	// t3 disappears from the cache behind the list's back
	if _, err := h.m.deps.Store.(*store.Store).DeleteGoneTasks(context.Background(), []string{"t3"}); err != nil {
		t.Fatal(err)
	}
	h.key("m")
	if s := h.screen(); !strings.Contains(s, "3 件の移動先のセクション") {
		t.Fatalf("section picker for the marked tasks:\n%s", s)
	}
	h.key("g", "enter") // 家具: t1 is already there
	if !strings.Contains(h.footer(), "2 件を「家具」に移動します") {
		t.Fatalf("tasks already in the section are left out, footer=%q", h.footer())
	}
	h.key("y")
	f := h.footer()
	if !strings.Contains(f, "1 件を「家具」に移動しました") || !strings.Contains(f, "1 件は変更できませんでした") {
		t.Fatalf("result footer=%q", f)
	}
	d, err := h.m.deps.Store.GetTask(context.Background(), "t4")
	if err != nil || d.Memberships[0].SectionGID != "s1" || !d.Pending {
		t.Fatalf("t4 should be moved: %+v %v", d.Memberships, err)
	}
	if ops := h.ops(); len(ops) != 1 {
		t.Fatalf("one move queued: %+v", ops)
	}
}

func TestBulkMoveNothingToDo(t *testing.T) {
	h := projectHarness(t, fixtureStore(t), nil)
	h.markRows("t2", "t3")
	h.key("m", "j", "enter") // 手続き: both are there
	if h.m.bulkAsk != nil || !strings.Contains(h.footer(), "すべてすでに「手続き」にあります") {
		t.Fatalf("footer=%q", h.footer())
	}
}

func TestBulkAssignee(t *testing.T) {
	h := newHarness(t, 140, 30)
	h.markRows("t2", "t3")
	h.key("@")
	p := h.m.picker
	if p == nil || p.bulk == nil || !strings.Contains(p.title, "2 件の担当者") {
		t.Fatalf("assignee picker for the marked tasks: %+v", p)
	}
	for i, it := range p.items {
		if it.value == "u2" {
			p.cursor = i
		}
	}
	h.key("enter")
	if !strings.Contains(h.footer(), "2 件の担当者を Partner にします") {
		t.Fatalf("footer=%q", h.footer())
	}
	h.key("y")
	for _, gid := range []string{"t2", "t3"} {
		d, _ := h.m.deps.Store.GetTask(context.Background(), gid)
		if d.Assignee == nil || d.Assignee.GID != "u2" {
			t.Errorf("%s assignee = %+v", gid, d.Assignee)
		}
	}
}

func TestBulkDuePickerAndPrompt(t *testing.T) {
	h := newHarness(t, 140, 30)
	h.markRows("t2", "t3")
	h.key(" ", "d")
	if p := h.m.picker; p == nil || p.bulk == nil {
		t.Fatalf("due picker for the marked tasks: %+v", p)
	}
	h.key("j", "enter") // 明日 (2026-11-14)
	h.key("y")
	for _, gid := range []string{"t2", "t3"} {
		if d, _ := h.m.deps.Store.GetTask(context.Background(), gid); d.DueOn != "2026-11-14" {
			t.Errorf("%s due = %q", gid, d.DueOn)
		}
	}

	h.markRows("t2", "t3")
	h.key(" ", "d", "G", "k", "enter") // 日付を入力…
	if h.m.promptMode != promptDue {
		t.Fatal("should prompt for the date")
	}
	h.typeText("2026-12-01")
	h.key("enter")
	if !strings.Contains(h.footer(), "2 件の期日を") {
		t.Fatalf("footer=%q", h.footer())
	}
	h.key("y")
	for _, gid := range []string{"t2", "t3"} {
		if d, _ := h.m.deps.Store.GetTask(context.Background(), gid); d.DueOn != "2026-12-01" {
			t.Errorf("%s due = %q", gid, d.DueOn)
		}
	}
	// a later single-task date prompt is not taken for the marked tasks
	if h.m.bulkDueTasks != nil {
		t.Fatal("the bulk date prompt should be done")
	}
}

// Without marks the keys act on the selected task as before; marks do not act in the board.
func TestBulkOnlyInList(t *testing.T) {
	h := projectHarness(t, fixtureStore(t), nil)
	h.markRows("t2", "t3")
	h.key("v") // board
	if len(h.m.markedTasks()) != 0 {
		t.Fatal("marks must not act on the board")
	}
	sel := h.selected()
	h.key("x")
	if h.m.bulkAsk != nil {
		t.Fatal("x on the board acts on the card, without asking")
	}
	if d, _ := h.m.deps.Store.GetTask(context.Background(), sel); !d.Completed {
		t.Fatalf("the selected card %s should be completed", sel)
	}
}
