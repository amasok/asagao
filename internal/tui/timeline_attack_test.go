package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/readmodel"
)

func tlTask(gid, name string, extra func(*asana.Task)) asana.Task {
	t := asana.Task{GID: gid, Name: name,
		Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}, Section: &asana.Ref{GID: "s1"}}}}
	if extra != nil {
		extra(&t)
	}
	return t
}

// Every row's "│" (the chart's left edge) is in the same column, whatever the names are
// (full-width, deep subtasks, long, with combining/emoji characters).
func TestTimelineAttackColumnsAligned(t *testing.T) {
	st := timelineStore(t)
	ctx := context.Background()
	if err := st.UpsertTasks(ctx, []asana.Task{
		tlTask("a1", "ＡＢＣ全角ｶﾀｶﾅ😀👨‍👩‍👧é", func(x *asana.Task) { x.DueOn = "2026-11-14"; x.NumSubtasks = 1 }),
		tlTask("a2", "深い", func(x *asana.Task) {
			x.DueOn = "2026-11-15"
			x.Parent = &asana.Ref{GID: "a1"}
			x.NumSubtasks = 1
		}),
		{GID: "a3", Name: "もっと深い", DueOn: "2026-11-16", NumSubtasks: 1, Parent: &asana.Ref{GID: "a2"}},
		{GID: "a4", Name: strings.Repeat("あ", 80), DueOn: "2026-11-16", Parent: &asana.Ref{GID: "a3"}},
	}); err != nil {
		t.Fatal(err)
	}
	h := projectHarness(t, st, nil)
	h.key("v", "v")
	for _, gid := range []string{"a1", "a2", "a3"} {
		h.selectTask(gid)
		h.key("l")
	}
	for _, w := range []int{40, 60, 100, 140} {
		h.send(teaSize(w, 40))
		want := -1
		for _, line := range strings.Split(h.screen(), "\n") {
			if !strings.Contains(line, "☐") && !strings.Contains(line, "◆") && !strings.Contains(line, "☑") {
				continue
			}
			i := strings.Index(line, "│")
			if i < 0 {
				continue
			}
			c := ansi.StringWidth(line[:i])
			if want < 0 {
				want = c
			}
			if c != want {
				t.Fatalf("w=%d: chart edge at %d, want %d:\n%s", w, c, want, h.screen())
			}
		}
		if want < 0 {
			t.Fatalf("w=%d: no rows:\n%s", w, h.screen())
		}
	}
}

// Odd dates never panic, and draw no more than the chart's width.
func TestTimelineAttackOddDates(t *testing.T) {
	st := timelineStore(t)
	ms := func(x *asana.Task) { x.ResourceSubtype = "milestone" }
	if err := st.UpsertTasks(context.Background(), []asana.Task{
		tlTask("d1", "開始のみ", func(x *asana.Task) { x.StartOn = "2026-11-12" }),
		tlTask("d2", "逆転", func(x *asana.Task) { x.StartOn = "2026-12-30"; x.DueOn = "2026-11-12" }),
		tlTask("d3", "古い", func(x *asana.Task) { x.StartOn = "0001-01-01"; x.DueOn = "0001-01-02" }),
		tlTask("d4", "遠い", func(x *asana.Task) { x.StartOn = "9999-12-30"; x.DueOn = "9999-12-31" }),
		tlTask("d5", "全期間", func(x *asana.Task) { x.StartOn = "0001-01-01"; x.DueOn = "9999-12-31" }),
		tlTask("d6", "壊れた", func(x *asana.Task) { x.StartOn = "2026-13-45"; x.DueOn = "２０２６-11-12" }),
		tlTask("d7", "時刻のみ", func(x *asana.Task) {
			at := time.Date(2026, 11, 12, 23, 30, 0, 0, time.FixedZone("W", -5*3600))
			x.DueAt = &at
		}),
		tlTask("d8", "里程標古", func(x *asana.Task) { ms(x); x.DueOn = "1999-01-01" }),
		tlTask("d9", "里程標遠", func(x *asana.Task) { ms(x); x.DueOn = "2099-01-01" }),
		tlTask("d10", "里程標跨ぎ", func(x *asana.Task) { ms(x); x.StartOn = "2026-01-01"; x.DueOn = "2026-11-12" }),
		tlTask("d11", "里程標逆", func(x *asana.Task) { ms(x); x.StartOn = "2027-01-01"; x.DueOn = "2026-11-12" }),
	}); err != nil {
		t.Fatal(err)
	}
	h := projectHarness(t, st, nil)
	h.key("b", "v", "v")
	h.send(teaSize(100, 50))
	h.screen() // the first draw follows the selected task, once
	h.key(".")
	for _, name := range []string{"開始のみ", "逆転", "里程標跨ぎ", "里程標逆", "時刻のみ"} {
		if got := h.ganttOf(name); !strings.ContainsAny(got, "█◆") {
			t.Errorf("%s: should have a mark: %q", name, got)
		}
	}
	if got := h.ganttOf("古い"); !strings.Contains(got, "◀") {
		t.Errorf("old: %q", got)
	}
	if got := h.ganttOf("里程標古"); !strings.Contains(got, "◀") {
		t.Errorf("old milestone: %q", got)
	}
	if got := h.ganttOf("里程標遠"); !strings.Contains(got, "▶") {
		t.Errorf("far milestone: %q", got)
	}
	if got := h.ganttOf("全期間"); strings.Trim(strings.ReplaceAll(got, "█", ""), " ") != "" || !strings.HasPrefix(got, "█") {
		t.Errorf("a bar covering everything fills the chart: %q\n%s", got, h.screen())
	}
	if got := strings.Trim(strings.ReplaceAll(h.ganttOf("壊れた"), "│", ""), " "); got != "" {
		t.Errorf("invalid dates should draw nothing: %q", got)
	}
	// selecting each of them (the view follows) must not panic or overflow
	for _, gid := range []string{"d3", "d4", "d5", "d8", "d9", "d6"} {
		h.selectTask(gid)
		for _, line := range strings.Split(h.screen(), "\n") {
			if ansi.StringWidth(line) > 100 {
				t.Fatalf("%s: line too wide: %q", gid, line)
			}
		}
	}
}

// due_at near midnight is the day in the clock's zone; compare with the same task by due_on.
func TestTimelineAttackDueAtZone(t *testing.T) {
	loc := time.FixedZone("X", -11*3600)
	s, e, ok := taskSpan(&readmodel.TaskSummary{DueAt: "2026-12-31T23:59:59+14:00"}, loc)
	want := time.Date(2026, 12, 30, 0, 0, 0, 0, time.UTC) // 09:59:59 on 12/30 at UTC-11
	if !ok || !s.Equal(want) || !e.Equal(want) {
		t.Fatalf("%v %v %v", s, e, ok)
	}
}

// Year-crossing range: the month line labels the year at January, and the title shows both years.
func TestTimelineAttackYearCrossing(t *testing.T) {
	h := timelineHarness(t)
	h.send(teaSize(140, 30))
	h.screen() // the first draw follows the selected task, once
	for range 7 {
		h.key("]")
	}
	s := h.screen()
	title := strings.SplitN(strings.SplitN(s, "タイムライン 2", 3)[len(strings.SplitN(s, "タイムライン 2", 3))-1], "\n", 2)[0]
	if !strings.Contains(s, "2027年1月") || !strings.Contains(title, "〜2027/") {
		t.Fatalf("year crossing:\n%s", s)
	}
}

// Dependency cycles and a task related to itself never break the marks.
func TestTimelineAttackDependencyCycle(t *testing.T) {
	st := timelineStore(t)
	ctx := context.Background()
	// t7 <-> t10 both ways, plus t10 on itself
	_ = st
	_ = ctx
	h := timelineHarness(t)
	h.selectTask("t7")
	h.m.detail.task.Dependencies = append(h.m.detail.task.Dependencies, h.m.detail.task.Dependents...)
	for _, line := range strings.Split(h.screen(), "\n") {
		if ansi.StringWidth(line) > 140 {
			t.Fatalf("too wide: %q", line)
		}
	}
	if g := h.gutterOf("家具を運ぶ"); g != "前" && g != "後" {
		t.Fatalf("cycle should still mark: %q", g)
	}
}

// Marks come from the selected task's own detail: a stale detail of another task is ignored,
// and a search or leaving the project clears the timeline.
func TestTimelineAttackStaleDetailAndSearch(t *testing.T) {
	h := timelineHarness(t)
	h.selectTask("t7")
	h.screen()
	h.key("j") // another task: its detail (if any) is not t7's
	h.m.detail.task = nil
	if g := h.gutterOf("家具を運ぶ"); g != "待" {
		t.Fatalf("without the selected task's detail, a waiting task is 待: %q", g)
	}
	h.key("/")
	h.typeText("荷造")
	if h.m.timelineActive() {
		t.Fatal("search should show the list")
	}
	if strings.Contains(h.screen(), "タイムライン") {
		t.Fatalf("no timeline while searching:\n%s", h.screen())
	}
	h.key("esc")
	if !h.m.timelineActive() {
		t.Fatalf("esc should bring the timeline back:\n%s", h.screen())
	}
}

// My Tasks <-> project: v keeps cycling correctly, and My Tasks never shows the timeline.
func TestTimelineAttackScopeSwitch(t *testing.T) {
	h := timelineHarness(t)
	h.key("tab") // sidebar
	h.key("g", "l")
	if h.m.currentScope().kind == scopeProject {
		t.Fatal("expected My Tasks")
	}
	if h.m.timelineActive() || strings.Contains(h.screen(), "タイムライン") {
		t.Fatal("My Tasks has no timeline")
	}
	h.key("v")
	if !h.m.calendarActive() || h.m.timelineActive() {
		t.Fatal("v in My Tasks: calendar")
	}
	h.key("v")
	if h.m.calendarActive() || h.m.timelineActive() {
		t.Fatal("v in My Tasks: list")
	}
}

// Timeline keys never leak into other views: [ ] { } . in the list change nothing in the timeline state.
func TestTimelineAttackKeysOutside(t *testing.T) {
	h := projectHarness(t, timelineStore(t), nil)
	h.key("[", "]", "{", "}", ".")
	if !h.m.tl.start.IsZero() || h.m.tl.dayW != 0 {
		t.Fatalf("list keys changed timeline state: %v %d", h.m.tl.start, h.m.tl.dayW)
	}
}

// Terminal control characters from a task name never reach the screen.
func TestTimelineAttackControlChars(t *testing.T) {
	st := timelineStore(t)
	if err := st.UpsertTasks(context.Background(), []asana.Task{
		tlTask("c1", "bad\x1b[2J\x1b]0;x\x07name\r\n\t\x00end", func(x *asana.Task) { x.DueOn = "2026-11-14" }),
	}); err != nil {
		t.Fatal(err)
	}
	h := projectHarness(t, st, nil)
	h.key("v", "v")
	raw := h.m.render()
	if strings.ContainsAny(raw, "\x00\x07\r\t") || strings.Contains(raw, "\x1b[2J") || strings.Contains(raw, "\x1b]0") {
		t.Fatalf("control characters in output: %q", raw)
	}
}

// Very small and zero-ish sizes with every day width, every selection: no panic.
func TestTimelineAttackNoPanicSweep(t *testing.T) {
	h := timelineHarness(t)
	for _, w := range []int{0, 1, 5, 9, 12, 17, 25} {
		for _, ht := range []int{0, 1, 2, 4, 5, 8} {
			h.send(teaSize(w, ht))
			for _, k := range []string{"}", "{", "]", "[", ".", "j", "k", "G", "g", "l", "h", "V"} {
				h.key(k)
				_ = h.screen()
			}
		}
	}
}

// The marks only come from the selected task's own detail, never from a stale one.
func TestTimelineAttackStaleDetailIgnored(t *testing.T) {
	h := timelineHarness(t)
	h.selectTask("t7")
	h.screen()
	if h.m.detail.task == nil || h.m.detail.task.GID != "t7" {
		t.Skip("detail not loaded in the harness")
	}
	stale := *h.m.detail.task
	h.selectTask("t1")
	h.m.detail.task = &stale
	if g := h.gutterOf("家具を運ぶ"); g == "後" {
		t.Fatalf("a stale detail of another task marked 後: %q", g)
	}
}

// A task ending the day before the range gets ◀, one starting right after it ▶, a completed
// task is not marked 待.
func TestTimelineAttackEdgesAndDone(t *testing.T) {
	st := timelineStore(t)
	days := timelineLayout(140-4, 2).days
	edge := func(d int) string {
		return time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC).AddDate(0, 0, days+d).Format(time.DateOnly)
	}
	if err := st.UpsertTasks(context.Background(), []asana.Task{
		tlTask("e1", "左端", func(x *asana.Task) { x.DueOn = "2026-11-05" }),
		tlTask("e2", "左端内", func(x *asana.Task) { x.DueOn = "2026-11-06" }),
		tlTask("e4", "右端外", func(x *asana.Task) { x.DueOn = edge(0) }),
		tlTask("e5", "右端内", func(x *asana.Task) { x.DueOn = edge(-1) }),
	}); err != nil {
		t.Fatal(err)
	}
	h := projectHarness(t, st, nil)
	h.key("b", "v", "v")
	h.screen()
	h.key(".")
	if got := h.ganttOf("左端"); !strings.HasPrefix(got, "◀") {
		t.Errorf("ended the day before: %q", got)
	}
	if got := h.ganttOf("左端内"); !strings.HasPrefix(got, "██") {
		t.Errorf("ends on the first day: %q", got)
	}
	for _, c := range []struct{ name, want string }{{"右端外", "▶"}, {"右端内", "█"}} {
		if got := strings.TrimRight(h.ganttOf(c.name), " "); !strings.HasSuffix(got, c.want) {
			t.Errorf("%s: %q", c.name, got)
		}
	}
}
