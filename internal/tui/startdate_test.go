package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/readmodel"
)

// #57: the start date is chosen in the detail like the due date, and not offered where a
// recent 402 says the plan lacks it.

func TestDetailStartDate(t *testing.T) {
	st := fixtureStore(t)
	addSubtasks(t, st)
	ctx := context.Background()
	h := projectHarness(t, st, nil) // ソファを買う, due 11/20; today is 11/13
	h.key("enter")
	reach := func(kind itemKind) bool {
		t.Helper()
		h.key("g")
		for range 30 {
			if it := h.m.currentItem(); it != nil && it.kind == kind {
				return true
			}
			h.key("j")
		}
		return false
	}
	if !reach(itemStart) {
		t.Fatalf("no start date item:\n%s", h.screen())
	}
	h.key("enter")
	if s := h.screen(); !strings.Contains(s, "開始日（") || !strings.Contains(s, "日付を入力…") || !strings.Contains(s, "開始日なし") {
		t.Fatalf("start chooser:\n%s", s)
	}
	h.key("g", "j", "enter") // 明日
	if d, _ := st.GetTask(ctx, "t1"); d.StartOn != "2026-11-14" {
		t.Fatalf("start = %q", d.StartOn)
	}
	if !strings.Contains(h.screen(), "開始日を") {
		t.Errorf("status:\n%s", h.screen())
	}

	// Typed: after the due date is refused, and says why.
	reach(itemStart)
	h.key("enter", "G", "k", "enter") // 日付を入力…
	if h.m.promptMode != promptStart || h.m.prompt.Value() != "2026-11-14" {
		t.Fatalf("prompt %v %q", h.m.promptMode, h.m.prompt.Value())
	}
	h.m.prompt.SetValue("")
	h.typeText("2026-11-21")
	h.key("enter")
	if d, _ := st.GetTask(ctx, "t1"); d.StartOn != "2026-11-14" {
		t.Fatalf("start after the due date was set: %q", d.StartOn)
	}
	if !strings.Contains(h.screen(), "より後です") {
		t.Errorf("no reason shown:\n%s", h.screen())
	}
	reach(itemStart)
	h.key("enter", "G", "enter") // 開始日なし
	if d, _ := st.GetTask(ctx, "t1"); d.StartOn != "" {
		t.Fatalf("start should be cleared: %q", d.StartOn)
	}
}

func TestDetailStartDateHiddenByPlan(t *testing.T) {
	st := fixtureStore(t)
	addSubtasks(t, st)
	ctx := context.Background()
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"start_on": "2026-11-10"}, testNow); err != nil {
		t.Fatal(err)
	}
	ws, err := st.TaskWorkspace(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSyncState(ctx, readmodel.PlanKey(readmodel.PlanStartOn, ws), testNow.Add(-time.Hour).UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	h := projectHarness(t, st, nil)
	h.key("enter")
	h.key("g")
	for range 30 {
		if it := h.m.currentItem(); it != nil && it.kind == itemStart {
			t.Fatalf("start date offered under a 402:\n%s", h.screen())
		}
		h.key("j")
	}
	h.key("g")
	if !strings.Contains(h.screen(), "2026-11-10") {
		t.Errorf("the start date is not shown:\n%s", h.screen())
	}
}

// The start date set from the timeline (its detail) moves the start of the task's bar.
func TestTimelineStartDate(t *testing.T) {
	h := timelineHarness(t)
	col := func(day int) int { return (day - 6) * 2 } // November day -> column
	h.selectTask("t10")                                // 家具を運ぶ: due 11/21, no start date
	if got := h.ganttOf("家具を運ぶ"); !strings.HasPrefix(strings.ReplaceAll(got, "│", " "), strings.Repeat(" ", col(21))+"██") {
		t.Fatalf("before: %q", got)
	}
	h.key("enter")
	h.key("g")
	for range 30 {
		if it := h.m.currentItem(); it != nil && it.kind == itemStart {
			break
		}
		h.key("j")
	}
	h.key("enter", "G", "k", "enter") // 日付を入力…
	h.m.prompt.SetValue("")
	h.typeText("2026-11-16")
	h.key("enter")
	h.key("esc")
	if !h.m.timelineActive() {
		t.Fatalf("not back in the timeline:\n%s", h.screen())
	}
	want := strings.Repeat(" ", col(16)) + strings.Repeat("█", 12) // 11/16 to 11/21
	got := strings.ReplaceAll(h.ganttOf("家具を運ぶ"), "│", " ")
	if !strings.HasPrefix(got, want) || strings.Trim(strings.TrimPrefix(got, want), " ") != "" {
		t.Fatalf("after: %q, want %q", got, want)
	}
}
