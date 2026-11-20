package tui

import (
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/readmodel"
)

// A bulk date that does not parse ends the bulk date prompt (enter closes it): the marked
// tasks are not kept as its target, so a reload that hides them later does not report a
// cancelled change that was no longer waiting.
func TestBulkDatePromptParseErrorForgetsTargets(t *testing.T) {
	h := newHarness(t, 140, 30)
	h.markRows("t2", "t3")
	h.key(" ", "d", "G", "k", "enter") // 日付を入力…
	if h.m.prompt.mode != promptDue || len(h.m.prompt.bulkDue) != 2 {
		t.Fatalf("should prompt for the marked tasks: mode=%d bulk=%d", h.m.prompt.mode, len(h.m.prompt.bulkDue))
	}
	h.typeText("そのうち")
	h.key("enter")
	if h.m.prompt.mode != promptNone {
		t.Fatal("enter should close the prompt")
	}
	if h.m.prompt.bulkDue != nil {
		t.Errorf("the closed prompt still targets the marked tasks: %d", len(h.m.prompt.bulkDue))
	}
	h.m.status = ""
	h.dropFromList("t2")
	h.dropFromList("t3")
	if strings.Contains(h.m.status, "取り消しました") {
		t.Fatalf("a reload reported a cancelled bulk date that was not waiting: %q", h.m.status)
	}
}

// Opening a prompt forgets the targets of the one before.
func TestOpenPromptForgetsEarlierTargets(t *testing.T) {
	m := &Model{prompt: promptState{input: newInput()}}
	m.openPrompt(promptComment)
	m.prompt.commentGID = "t1"
	m.prompt.bulkDue = []readmodel.TaskSummary{{GID: "t2"}}
	m.openPrompt(promptCreate)
	if m.prompt.commentGID != "" || m.prompt.bulkDue != nil || m.prompt.mode != promptCreate {
		t.Fatalf("targets left over: %+v", m.prompt)
	}
}
