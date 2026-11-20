package tui

import "testing"

// The view mode is one value: a board left for My Tasks (which shows only a list or a
// calendar) is the board again on coming back to the project, and v in My Tasks goes
// from it to the calendar, not to a board and a calendar at once.
func TestViewModeKeptAcrossScopes(t *testing.T) {
	h := projectHarness(t, timelineStore(t), nil)
	project := h.m.scopeCursor
	h.key("v")
	if h.m.mode != viewBoard || !h.m.boardActive() {
		t.Fatalf("v did not open the board: mode=%d", h.m.mode)
	}
	h.key("h", "g", "l") // My Tasks
	if h.m.boardActive() || h.m.mode != viewBoard {
		t.Fatalf("My Tasks: board shown=%v mode=%d", h.m.boardActive(), h.m.mode)
	}
	h.run(h.m.moveScope(project - h.m.scopeCursor))
	if !h.m.boardActive() {
		t.Fatalf("back in the project: the board is gone (mode=%d)", h.m.mode)
	}
	h.key("h", "g", "l")
	h.key("v")
	if h.m.mode != viewCalendar || !h.m.calendarActive() || h.m.boardActive() || h.m.timelineActive() {
		t.Fatalf("v in My Tasks from the board: mode=%d", h.m.mode)
	}
}
