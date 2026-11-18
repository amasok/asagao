package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/readmodel"
)

// An answer of an older load (a detail that was left with esc) must not come back on screen.
func TestAttackGoalsStaleAnswerDropped(t *testing.T) {
	h := goalsHarness(t, goalsStore(t), func() time.Time { return testNow })
	m := h.m
	h.key(" ", "Q")
	list := m.goals.list
	h.key("enter") // detail of g1; loads (cache + fetch)
	staleSeq := m.goals.seq
	d, err := m.goalUseCases().Get(context.Background(), "g1")
	if err != nil {
		t.Fatal(err)
	}
	h.key("esc") // back to the list: a newer load
	if m.goals.seq == staleSeq {
		t.Fatal("esc did not start a newer load")
	}
	h.send(goalDetailMsg{seq: staleSeq, fetched: true, goal: d})
	if m.goals.goal != nil || m.goals.list == nil || len(m.goals.list.Goals) != len(list.Goals) {
		t.Fatalf("a stale detail replaced the list:\n%s", h.screen())
	}
	// A cached answer arriving after the fetched one of the same load is dropped too.
	seq := m.goals.seq
	h.send(goalListMsg{seq: seq, fetched: true, list: &readmodel.GoalList{Goals: []readmodel.GoalView{{GID: "n1", Name: "新しい"}}}})
	h.send(goalListMsg{seq: seq, fetched: false, list: list})
	if len(m.goals.list.Goals) != 1 || m.goals.list.Goals[0].GID != "n1" {
		t.Fatalf("cached answer after the fetched one won: %+v", m.goals.list.Goals)
	}
}

// The cursor stays inside the list when the list shrinks (a goal left Asana).
func TestAttackGoalsCursorClampsWhenListShrinks(t *testing.T) {
	h := goalsHarness(t, goalsStore(t), func() time.Time { return testNow })
	m := h.m
	h.key(" ", "Q", "G")
	if m.goals.cursor != 2 {
		t.Fatalf("cursor = %d", m.goals.cursor)
	}
	h.send(goalListMsg{seq: m.goals.seq, fetched: true, list: &readmodel.GoalList{Goals: []readmodel.GoalView{{GID: "g1", Name: "一つだけ"}}}})
	if m.goals.cursor != 0 {
		t.Fatalf("cursor = %d after shrinking", m.goals.cursor)
	}
	h.send(goalListMsg{seq: m.goals.seq, fetched: true, list: &readmodel.GoalList{}})
	if m.goals.cursor != 0 {
		t.Fatalf("cursor = %d on empty", m.goals.cursor)
	}
	h.key("enter", "j", "k", "G", "ctrl+d", "pgdown", "pgup", "l", "r") // nothing to open: must not panic
	_ = h.screen()
}

// Every key on an empty screen, an empty portfolio list and a detail with no rows is safe.
func TestAttackGoalsEmptyEverywhere(t *testing.T) {
	st := overviewStore(t)
	h := goalsHarness(t, st, func() time.Time { return testNow })
	for _, ks := range [][]string{{" ", "Q"}, {" ", "l"}} {
		h.key(ks...)
		for _, k := range []string{"enter", "j", "k", "g", "G", "ctrl+d", "ctrl+u", "tab", "c", "l", "h", "tab", "c", "r"} {
			h.key(k)
			_ = h.screen()
		}
		h.key("esc", "esc")
	}
}

// A detail whose load failed says so, and esc still goes back to the list.
func TestAttackGoalsFailedDetailEscapes(t *testing.T) {
	h := goalsHarness(t, goalsStore(t), func() time.Time { return testNow })
	m := h.m
	h.key(" ", "Q", "enter")
	h.send(goalDetailMsg{seq: m.goals.seq, fetched: true, err: context.DeadlineExceeded})
	mustShow(t, h.screen(), "deadline")
	h.key("esc")
	if m.goals.goal != nil || len(m.goals.stack) != 0 || !m.goals.open {
		t.Fatalf("not back at the list: goal=%v stack=%v open=%v", m.goals.goal, m.goals.stack, m.goals.open)
	}
	mustShow(t, h.screen(), "売上を伸ばす")
}

// Control characters in names reach the screen only as text; tiny windows do not panic.
func TestAttackGoalsTermsafeAndTinyWindow(t *testing.T) {
	st := goalsStore(t)
	esc := "\x1b]0;pwn\x07"
	if err := st.ReplaceGoals(context.Background(), "w1", []model.Goal{{GID: "gx", Name: "悪い" + esc, Status: "green"}}); err != nil {
		t.Fatal(err)
	}
	h := goalsHarness(t, st, func() time.Time { return testNow })
	h.key(" ", "Q")
	if raw := h.m.render(); strings.Contains(raw, "pwn\x07") || strings.Contains(raw, "\x1b]0") {
		t.Fatalf("control sequence on screen: %q", raw)
	}
	for _, sz := range [][2]int{{1, 1}, {10, 3}, {20, 2}, {200, 1}} {
		h.send(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
		h.key("enter")
		_ = h.m.render()
		h.key("esc")
		_ = h.m.render()
	}
}
