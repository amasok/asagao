package tui

import (
	"context"
	"strings"
	"testing"
)

// dropFromList simulates a reload that no longer lists gid.
func (h *harness) dropFromList(gid string) {
	h.t.Helper()
	var rows []row
	for _, r := range h.m.allRows {
		if r.task != nil && r.task.GID == gid {
			continue
		}
		rows = append(rows, r)
	}
	h.m.setRows(rows)
	if h.m.listed(gid) {
		h.t.Fatalf("%s should be gone", gid)
	}
}

func (h *harness) completed(gid string) bool {
	h.t.Helper()
	d, err := h.m.deps.Store.GetTask(context.Background(), gid)
	if err != nil {
		h.t.Fatal(err)
	}
	return d.Completed
}

// A reload that hides a task while "N 件を完了にします (y/n)" is waiting must not let y change it.
func TestBulkAttackReloadWhileAskingDropsHiddenTask(t *testing.T) {
	h := newHarness(t, 140, 30)
	h.markRows("t2", "t3")
	h.key("x")
	if h.m.bulkAsk == nil {
		t.Fatal("should be asking")
	}
	h.dropFromList("t3")
	h.key("y")
	if h.completed("t3") {
		t.Fatal("t3 was hidden by a reload after the question, but was completed")
	}
	if ops := h.ops(); len(ops) > 1 {
		t.Fatalf("only the still-shown task may be written: %+v", ops)
	}
}

// Same while a section / assignee / due picker for the marked tasks is open.
func TestBulkAttackReloadWhilePickerOpenDropsHiddenTask(t *testing.T) {
	h := newHarness(t, 140, 30)
	h.markRows("t2", "t3")
	h.key("@")
	h.dropFromList("t3")
	p := h.m.picker
	if p == nil {
		t.Skip("picker closed by reload")
	}
	for i, it := range p.items {
		if it.value == "u2" {
			p.cursor = i
		}
	}
	h.key("enter", "y")
	if d, _ := h.m.deps.Store.GetTask(context.Background(), "t3"); d.Assignee != nil && d.Assignee.GID == "u2" {
		t.Fatal("hidden t3 got the assignee change")
	}
}

// Same for the free-form date prompt.
func TestBulkAttackReloadWhileDatePromptDropsHiddenTask(t *testing.T) {
	h := newHarness(t, 140, 30)
	h.markRows("t2", "t3")
	h.key(" ", "d", "G", "k", "enter")
	if h.m.promptMode != promptDue {
		t.Fatal("should prompt")
	}
	h.dropFromList("t3")
	h.typeText("2026-12-05")
	h.key("enter", "y")
	if d, _ := h.m.deps.Store.GetTask(context.Background(), "t3"); d.DueOn == "2026-12-05" {
		t.Fatal("hidden t3 got the due date")
	}
}

// Cancelling the bulk date prompt must not leave the marked tasks to be taken for a later single date entry.
func TestBulkAttackCancelledDatePromptDoesNotLeakIntoSingleDue(t *testing.T) {
	h := newHarness(t, 140, 30)
	h.markRows("t2", "t3")
	h.key(" ", "d", "G", "k", "enter")
	if h.m.promptMode != promptDue {
		t.Fatal("should prompt")
	}
	h.key("esc")
	h.key("esc") // clear marks
	h.m.selectInList("t1")
	h.key(" ", "d", "G", "k", "enter") // single-task date entry
	if h.m.promptMode != promptDue {
		t.Fatal("should prompt")
	}
	h.m.prompt.SetValue("")
	h.typeText("2026-12-09")
	h.key("enter")
	if h.m.bulkAsk != nil {
		t.Fatalf("single date entry must not ask about marked tasks: %q", h.footer())
	}
	if d, _ := h.m.deps.Store.GetTask(context.Background(), "t1"); d.DueOn != "2026-12-09" {
		t.Fatalf("t1 due = %q", d.DueOn)
	}
}

// Non-answer keys keep asking; q / esc / n cancel (and q does not quit).
func TestBulkAttackAskingKeys(t *testing.T) {
	h := newHarness(t, 140, 30)
	h.markRows("t2")
	h.key("x")
	for _, k := range []string{"x", "enter", "m", "V", "*", "j", "/"} {
		h.key(k)
		if h.m.bulkAsk == nil || !strings.Contains(h.footer(), "(y/n)") {
			t.Fatalf("key %q dropped the question", k)
		}
	}
	if ops := h.ops(); len(ops) != 0 {
		t.Fatalf("nothing written: %+v", ops)
	}
	h.key("q")
	if h.m.bulkAsk != nil {
		t.Fatal("q cancels")
	}
}

// Completed + open mixed: only open ones are completed; offline (no sync) just queues.
func TestBulkAttackMixedCompletionCountsOnlyChanged(t *testing.T) {
	h := newHarness(t, 140, 30)
	h.chooseFilter(filterAll)
	h.markRows("t2", "t5")
	h.key("x")
	f := h.footer()
	if !strings.Contains(f, "1 件を完了にします") {
		t.Fatalf("already-completed task must not be counted: %q", f)
	}
}

// Tasks that already have the chosen due date are not counted; none left says so, no question.
func TestBulkAttackDueSkipsUnchanged(t *testing.T) {
	h := newHarness(t, 140, 30)
	d, _ := h.m.deps.Store.GetTask(context.Background(), "t1")
	if d.DueOn == "" {
		t.Skip("t1 has no due date in the fixture")
	}
	h.markRows("t1", "t2")
	h.key(" ", "d", "G", "k", "enter")
	h.m.prompt.SetValue("")
	h.typeText(d.DueOn)
	h.key("enter")
	if f := h.footer(); !strings.Contains(f, "1 件の期日を") {
		t.Fatalf("t1 already has the date: %q", f)
	}
	h.key("n", "esc")
	h.markRows("t1")
	h.key(" ", "d", "G", "k", "enter")
	h.m.prompt.SetValue("")
	h.typeText(d.DueOn)
	h.key("enter")
	if h.m.bulkAsk != nil || !strings.Contains(h.footer(), "すべてすでに期日が") {
		t.Fatalf("footer=%q", h.footer())
	}
}
