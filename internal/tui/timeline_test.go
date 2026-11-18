package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/store"
)

// timelineStore adds to the fixture (today is 2026-11-13, a Friday):
//   - t6 a milestone due 11/25, t7 "荷造り" from 11/09 to 11/18 with a subtask t8,
//   - t9 far in the future (2027-03-01 to 03-05), t10 waits for t7.
func timelineStore(t *testing.T) *store.Store {
	t.Helper()
	st := fixtureStore(t)
	ctx := context.Background()
	ref := func(gid string) *asana.Ref { return &asana.Ref{GID: gid} }
	in := func(sec string) []asana.Membership {
		return []asana.Membership{{Project: ref("p1"), Section: ref(sec)}}
	}
	if err := st.UpsertTasks(ctx, []asana.Task{
		{GID: "t6", Name: "引っ越し完了", ResourceSubtype: "milestone", DueOn: "2026-11-25", Memberships: in("s2")},
		{GID: "t7", Name: "荷造り", StartOn: "2026-11-09", DueOn: "2026-11-18", NumSubtasks: 1, Memberships: in("s1")},
		{GID: "t8", Name: "段ボールを集める", DueOn: "2026-11-11", Parent: &asana.Ref{GID: "t7", Name: "荷造り"}},
		{GID: "t9", Name: "春の片付け", StartOn: "2027-03-01", DueOn: "2027-03-05", Memberships: in("s1")},
		{GID: "t10", Name: "家具を運ぶ", DueOn: "2026-11-21", Memberships: in("s1")},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceDependencies(ctx, "t10", []model.TaskRef{{GID: "t7", Name: "荷造り"}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceDependencies(ctx, "t7", nil, []model.TaskRef{{GID: "t10", Name: "家具を運ぶ"}}); err != nil {
		t.Fatal(err)
	}
	return st
}

// timelineHarness opens 新居プロジェクト in the timeline, without the sidebar (so that the
// chart's columns are easy to count).
func timelineHarness(t *testing.T) *harness {
	t.Helper()
	h := projectHarness(t, timelineStore(t), nil)
	h.key("b", "v", "v")
	if !h.m.timelineActive() {
		t.Fatalf("v v should open the timeline:\n%s", h.screen())
	}
	return h
}

// ganttOf returns the chart part of the screen line that shows name.
func (h *harness) ganttOf(name string) string {
	h.t.Helper()
	l := timelineLayout(h.m.width-4, h.m.tlDayWidth())
	start := 2 + l.nameW + tlGutterW + 1 // border and padding, names, 前/後, separator
	for _, line := range strings.Split(h.screen(), "\n") {
		if strings.Contains(line, name) {
			return ansi.Cut(line, start, start+l.ganttW)
		}
	}
	h.t.Fatalf("no line with %q:\n%s", name, h.screen())
	return ""
}

func (h *harness) gutterOf(name string) string {
	h.t.Helper()
	l := timelineLayout(h.m.width-4, h.m.tlDayWidth())
	start := 2 + l.nameW
	for _, line := range strings.Split(h.screen(), "\n") {
		if strings.Contains(line, name) {
			return ansi.Cut(line, start, start+tlGutterW)
		}
	}
	h.t.Fatalf("no line with %q:\n%s", name, h.screen())
	return ""
}

func (h *harness) selectTask(gid string) {
	h.t.Helper()
	for range len(h.m.rows) {
		if h.selected() == gid {
			return
		}
		h.key("j")
	}
	h.key("g")
	for range len(h.m.rows) {
		if h.selected() == gid {
			return
		}
		h.key("j")
	}
	h.t.Fatalf("could not select %s", gid)
}

func TestTimelineBars(t *testing.T) {
	h := timelineHarness(t)
	s := h.screen()
	// The default range starts a week before today: 11/06, 2 columns a day.
	for _, want := range []string{"[タイムライン]", "タイムライン 2026/11/6〜", "2026年11月", "12月", "▾ 家具", "▾ 手続き", "◆ 引っ越し完了"} {
		if !strings.Contains(s, want) {
			t.Fatalf("timeline missing %q:\n%s", want, s)
		}
	}
	if h.m.tlOrigin() != time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("origin = %v", h.m.tlOrigin())
	}
	col := func(day int) int { return (day - 6) * 2 } // November day -> column
	cases := []struct {
		name, want string
	}{
		// start 11/09 to due 11/18: 10 days
		{"荷造り", strings.Repeat(" ", col(9)) + strings.Repeat("█", 20)},
		// no start date: just the due date (overdue: still a bar)
		{"転居届を出す", strings.Repeat(" ", col(10)) + "██"},
		// a milestone is a ◆ on its due date
		{"引っ越し完了", strings.Repeat(" ", col(25)) + "◆"},
	}
	for _, c := range cases {
		got := h.ganttOf(c.name)
		if !strings.HasPrefix(strings.ReplaceAll(got, "│", " "), c.want) {
			t.Errorf("%s: chart = %q, want prefix %q", c.name, got, c.want)
		}
		if rest := strings.TrimPrefix(strings.ReplaceAll(got, "│", " "), c.want); strings.Trim(rest, " ") != "" {
			t.Errorf("%s: more than the bar: %q", c.name, got)
		}
	}
	// today's line, on rows without a bar there
	if got := h.ganttOf("ガスの契約"); ansi.Cut(got, col(13), col(13)+1) != "│" {
		t.Errorf("today line missing: %q", got)
	}
	// no dates: nothing but the today line
	if got := strings.Trim(strings.ReplaceAll(h.ganttOf("ガスの契約"), "│", ""), " "); got != "" {
		t.Errorf("a task without dates should have no bar: %q", got)
	}
	// beyond the range: an arrow on the right edge
	if got := strings.TrimRight(h.ganttOf("春の片付け"), " "); !strings.HasSuffix(got, "▶") {
		t.Errorf("a later task should show ▶: %q", got)
	}
}

func TestTimelineScrollAndScale(t *testing.T) {
	h := timelineHarness(t)
	origin := h.m.tlOrigin()
	h.key("]")
	if got := h.m.tlOrigin(); !got.Equal(origin.AddDate(0, 0, 7)) {
		t.Fatalf("] should scroll a week: %v", got)
	}
	// 荷造り (11/09-11/18) now starts before the range: cut at the left edge
	if got := h.ganttOf("荷造り"); !strings.HasPrefix(got, strings.Repeat("█", 12)) {
		t.Fatalf("a bar that started earlier should be cut: %q", got)
	}
	h.key("[", "[")
	if got := h.m.tlOrigin(); !got.Equal(origin.AddDate(0, 0, -7)) {
		t.Fatalf("[ should scroll back a week: %v", got)
	}
	h.key(".")
	if got := h.m.tlOrigin(); !got.Equal(origin) {
		t.Fatalf(". should go back to today: %v", got)
	}
	h.key("}")
	if h.m.tlDayWidth() != 3 || !strings.HasPrefix(h.ganttOf("荷造り"), strings.Repeat(" ", 9)+strings.Repeat("█", 30)) {
		t.Fatalf("} should widen a day to 3 columns: %d %q", h.m.tlDayWidth(), h.ganttOf("荷造り"))
	}
	h.key("}", "}", "}")
	if h.m.tlDayWidth() != tlMaxDayW {
		t.Fatalf("width should stop at %d: %d", tlMaxDayW, h.m.tlDayWidth())
	}
	h.key("{", "{", "{", "{", "{", "{")
	if h.m.tlDayWidth() != 1 || !strings.HasPrefix(h.ganttOf("荷造り"), "   "+strings.Repeat("█", 10)+" ") {
		t.Fatalf("{ should narrow a day to 1 column: %d %q", h.m.tlDayWidth(), h.ganttOf("荷造り"))
	}
}

func TestTimelineFollowsSelection(t *testing.T) {
	h := timelineHarness(t)
	h.selectTask("t9") // 2027-03-01: out of the range
	h.screen()
	if got := h.m.tlOrigin(); got.After(time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC)) || got.Before(time.Date(2027, 2, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("selecting a later task should scroll to it: %v", got)
	}
	if got := h.ganttOf("春の片付け"); !strings.Contains(got, strings.Repeat("█", 10)) {
		t.Fatalf("its bar should be shown: %q", got)
	}
	// scrolling away keeps the selection, and the view does not jump back
	h.key("[")
	before := h.m.tlOrigin()
	h.screen()
	if !h.m.tlOrigin().Equal(before) {
		t.Fatalf("the view jumped back to the selection: %v", h.m.tlOrigin())
	}
}

func TestTimelineDependencies(t *testing.T) {
	h := timelineHarness(t)
	h.selectTask("t1") // unrelated
	if got := h.gutterOf("家具を運ぶ"); got != "待" {
		t.Fatalf("a task waiting for an open task should be marked 待: %q", got)
	}
	h.selectTask("t7")
	if got := h.gutterOf("家具を運ぶ"); got != "後" {
		t.Fatalf("the task waiting for the selected one should be marked 後: %q", got)
	}
	h.selectTask("t10")
	if got := h.gutterOf("荷造り"); got != "前" {
		t.Fatalf("the task the selected one waits for should be marked 前: %q", got)
	}
	if got := h.gutterOf("ソファを買う"); strings.TrimSpace(got) != "" {
		t.Fatalf("an unrelated task should not be marked: %q", got)
	}
}

func TestTimelineSubtasksAndDetail(t *testing.T) {
	h := timelineHarness(t)
	h.selectTask("t7")
	h.key("l") // expand
	if !strings.Contains(h.screen(), "└ ☐ 段ボールを集める") {
		t.Fatalf("l should show the subtasks indented:\n%s", h.screen())
	}
	col := (11 - 6) * 2
	if got := h.ganttOf("段ボールを集める"); !strings.HasPrefix(got, strings.Repeat(" ", col)+"██") {
		t.Fatalf("subtask bar: %q", got)
	}
	h.key("j")
	if h.selected() != "t8" {
		t.Fatalf("j should select the subtask: %s", h.selected())
	}
	h.key("l") // no subtasks: the detail, full screen (there is no detail pane)
	if !h.m.zoom || h.m.detailGID() != "t8" {
		t.Fatalf("l on a task without subtasks should open its detail: zoom=%v %s", h.m.zoom, h.m.detailGID())
	}
	h.key("esc")
	if h.m.zoom || !h.m.timelineActive() {
		t.Fatal("esc should go back to the timeline")
	}
	h.key("enter")
	if !h.m.zoom {
		t.Fatal("enter should open the detail")
	}
	h.key("esc")
	h.key("tab")
	if h.m.focus != paneSidebar {
		t.Fatalf("tab should go to the sidebar, not the hidden detail: %v", h.m.focus)
	}
	h.key("tab")
	if h.m.focus != paneList {
		t.Fatalf("tab should come back to the timeline: %v", h.m.focus)
	}
}

func TestTimelineViewCycle(t *testing.T) {
	h := projectHarness(t, timelineStore(t), nil)
	steps := []struct {
		board, timeline, calendar bool
	}{{true, false, false}, {false, true, false}, {false, false, true}, {false, false, false}}
	for i, s := range steps {
		h.key("v")
		if h.m.boardActive() != s.board || h.m.timelineActive() != s.timeline || h.m.calendarActive() != s.calendar {
			t.Fatalf("v #%d: board=%v timeline=%v calendar=%v", i+1, h.m.boardActive(), h.m.timelineActive(), h.m.calendarActive())
		}
	}
	// My Tasks has no timeline: list -> calendar -> list
	h.key("h", "g", "l")
	h.key("v")
	if !h.m.calendarActive() || h.m.timelineActive() {
		t.Fatal("v in My Tasks should open the calendar")
	}
	h.key("v")
	if h.m.calendarActive() || h.m.timelineActive() || h.m.boardActive() {
		t.Fatal("v should go back to the list")
	}
}

// Every size draws lines no wider than the screen (below 8 columns the panes' borders
// alone are wider), with long full-width names cut, and the selection stays visible.
func TestTimelineSmallScreens(t *testing.T) {
	st := timelineStore(t)
	long := strings.Repeat("とても長い名前の", 10)
	if err := st.UpsertTasks(context.Background(), []asana.Task{{GID: "t11", Name: long, DueOn: "2026-11-14",
		Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}, Section: &asana.Ref{GID: "s1"}}}}}); err != nil {
		t.Fatal(err)
	}
	h := projectHarness(t, st, nil)
	h.key("v", "v")
	for _, w := range []int{1, 8, 20, 30, 45, 60, 79, 80, 100, 200} {
		for _, ht := range []int{3, 6, 10, 30} {
			h.send(teaSize(w, ht))
			for _, side := range []bool{false, true} {
				if side {
					h.key("tab")
				}
				for i, line := range strings.Split(h.screen(), "\n") {
					if lw := ansi.StringWidth(line); lw > max(w, 1) && w >= 8 {
						t.Fatalf("%dx%d line %d is %d wide:\n%s", w, ht, i, lw, h.screen())
					}
				}
			}
		}
	}
	h.send(teaSize(120, 30))
	if h.m.focus == paneSidebar {
		h.key("tab")
	}
	h.key("G")
	h.send(teaSize(120, 9)) // shorter window: the selected (last) row is still drawn
	if last := h.m.rows[len(h.m.rows)-1].task; last == nil || !strings.Contains(h.screen(), last.Name) {
		t.Fatalf("the selected row should stay visible:\n%s", h.screen())
	}
	h.send(teaSize(120, 30))
	if !strings.Contains(h.screen(), "とても長い名前の") || strings.Contains(h.screen(), long) {
		t.Fatalf("a long name should be cut:\n%s", h.screen())
	}
}

func TestTaskSpan(t *testing.T) {
	tokyo := time.FixedZone("JST", 9*3600)
	day := func(s string) time.Time { d, _ := time.Parse(time.DateOnly, s); return d }
	cases := []struct {
		t          readmodel.TaskSummary
		start, end string
		ok         bool
	}{
		{readmodel.TaskSummary{StartOn: "2026-11-01", DueOn: "2026-11-05"}, "2026-11-01", "2026-11-05", true},
		{readmodel.TaskSummary{DueOn: "2026-11-05"}, "2026-11-05", "2026-11-05", true},
		{readmodel.TaskSummary{StartOn: "2026-11-05"}, "2026-11-05", "2026-11-05", true},
		// start after due (should not happen): a one-day bar on the due date
		{readmodel.TaskSummary{StartOn: "2026-11-09", DueOn: "2026-11-05"}, "2026-11-05", "2026-11-05", true},
		// due at a time: the day in the local time zone (16:00 UTC is the next day in Tokyo)
		{readmodel.TaskSummary{DueAt: "2026-11-05T16:00:00Z"}, "2026-11-06", "2026-11-06", true},
		{readmodel.TaskSummary{DueOn: "bad"}, "", "", false},
		{readmodel.TaskSummary{}, "", "", false},
	}
	for i, c := range cases {
		s, e, ok := taskSpan(&c.t, tokyo)
		if ok != c.ok || (ok && (!s.Equal(day(c.start)) || !e.Equal(day(c.end)))) {
			t.Errorf("#%d: %v %v %v, want %s %s %v", i, s, e, ok, c.start, c.end, c.ok)
		}
	}
}

// Days are counted on calendar dates, so a daylight saving change does not shift bars.
func TestTimelineDaysAcrossDST(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip(err)
	}
	a := dayOf(time.Date(2026, 3, 7, 23, 30, 0, 0, ny))
	b := dayOf(time.Date(2026, 3, 9, 0, 30, 0, 0, ny))
	if n := daysBetween(a, b); n != 2 {
		t.Fatalf("days across DST = %d", n)
	}
}

func teaSize(w, h int) tea.WindowSizeMsg { return tea.WindowSizeMsg{Width: w, Height: h} }

// V marks tasks for a bulk action in the timeline as in the list, so the mark is shown.
func TestTimelineMarks(t *testing.T) {
	h := timelineHarness(t)
	h.selectTask("t1")
	h.key("V")
	if !h.m.marked["t1"] || !strings.Contains(h.screen(), "● ☐ ソファを買う") {
		t.Fatalf("a marked task should show ●:\n%s", h.screen())
	}
	if got := h.ganttOf("ソファを買う"); !strings.Contains(got, "██") {
		t.Fatalf("the chart should not move with the mark: %q", got)
	}
}
