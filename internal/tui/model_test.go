package tui

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/amasok/asana-tui/internal/asana"
	"github.com/amasok/asana-tui/internal/store"
)

var testNow = time.Date(2026, 11, 13, 10, 0, 0, 0, time.Local)

func fixtureStore(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ref := func(gid, name string) *asana.Ref { return &asana.Ref{GID: gid, Name: name} }
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.SetSyncState(ctx, store.KeyMeGID, "me"))
	must(st.UpsertProjects(ctx, "w1", []asana.Project{{GID: "p1", Name: "新居プロジェクト", Members: []asana.Ref{{GID: "me"}}}}, "me"))
	must(st.ReplaceSections(ctx, "p1", []asana.Section{{GID: "s1", Name: "家具"}, {GID: "s2", Name: "手続き"}}))
	in := func(sec string) []asana.Membership {
		return []asana.Membership{{Project: ref("p1", ""), Section: ref(sec, "")}}
	}
	must(st.UpsertTasks(ctx, []asana.Task{
		{GID: "t1", Name: "ソファを買う", Notes: "リビング用", DueOn: "2026-11-20", Assignee: ref("me", "Me"), Memberships: in("s1")},
		{GID: "t2", Name: "転居届を出す", DueOn: "2026-11-10", Assignee: ref("me", "Me"), Memberships: in("s2")},
		{GID: "t3", Name: "電気の契約", DueOn: "2026-11-13", Assignee: ref("me", "Me"), Memberships: in("s2")},
		{GID: "t4", Name: "ガスの契約", Assignee: ref("u2", "Partner"), Memberships: in("s2")},
		{GID: "t5", Name: "年末調整", Completed: true, Assignee: ref("me", "Me")},
	}))
	return st
}

// harness drives the model synchronously: it runs returned commands and feeds their
// messages back, skipping timers (tea.Tick) so tests don't sleep.
type harness struct {
	t *testing.T
	m *Model
}

func newHarness(t *testing.T, w, h int) *harness {
	m := New(context.Background(), Deps{Store: fixtureStore(t), Now: func() time.Time { return testNow }})
	hs := &harness{t: t, m: m}
	hs.send(tea.WindowSizeMsg{Width: w, Height: h})
	hs.run(m.Init())
	return hs
}

func (h *harness) send(msg tea.Msg) {
	_, cmd := h.m.Update(msg)
	h.run(cmd)
}

func (h *harness) run(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		switch msg := msg.(type) {
		case tea.BatchMsg:
			for _, c := range msg {
				h.run(c)
			}
		case nil:
		default:
			h.send(msg)
		}
	case <-time.After(5 * time.Second):
		// Tests run without a syncer and without background sync, so no command should
		// be a timer; anything this slow is a hang.
		h.t.Fatalf("command did not return within 5s")
	}
}

func (h *harness) key(keys ...string) {
	for _, k := range keys {
		var msg tea.KeyPressMsg
		switch k {
		case "tab":
			msg = tea.KeyPressMsg{Code: tea.KeyTab}
		case "enter":
			msg = tea.KeyPressMsg{Code: tea.KeyEnter}
		case "esc":
			msg = tea.KeyPressMsg{Code: tea.KeyEscape}
		default:
			r := []rune(k)[0]
			msg = tea.KeyPressMsg{Code: r, Text: k}
		}
		h.send(msg)
	}
}

func (h *harness) screen() string { return ansi.Strip(h.m.render()) }

func (h *harness) selected() string {
	if t := h.m.selectedTask(); t != nil {
		return t.GID
	}
	return ""
}

func TestMyTasksGroupedByDue(t *testing.T) {
	h := newHarness(t, 140, 30)
	s := h.screen()
	for _, want := range []string{"My Tasks", "新居プロジェクト", "期限切れ", "転居届を出す", "今日", "電気の契約", "7日以内", "ソファを買う"} {
		if !strings.Contains(s, want) {
			t.Errorf("screen missing %q:\n%s", want, s)
		}
	}
	for _, unwanted := range []string{"ガスの契約", "年末調整"} { // someone else's / completed
		if strings.Contains(s, unwanted) {
			t.Errorf("screen should not contain %q", unwanted)
		}
	}
	if h.selected() != "t2" {
		t.Fatalf("first task should be selected, got %q", h.selected())
	}
	if !strings.Contains(s, "詳細") || !strings.Contains(s, "2026/11/10(火)") {
		t.Errorf("detail pane not rendered:\n%s", s)
	}
}

func TestNavigation(t *testing.T) {
	h := newHarness(t, 140, 30)
	h.key("j")
	if h.selected() != "t3" {
		t.Fatalf("after j: %q", h.selected())
	}
	h.key("G")
	if h.selected() != "t1" {
		t.Fatalf("after G: %q", h.selected())
	}
	if !strings.Contains(h.screen(), "リビング用") {
		t.Errorf("detail should show notes of t1")
	}
	h.key("g")
	if h.selected() != "t2" {
		t.Fatalf("after g: %q", h.selected())
	}

	// Switch to the project in the sidebar: tasks grouped by section, all assignees.
	h.key("h", "j", "j")
	s := h.screen()
	for _, want := range []string{"家具", "手続き", "ガスの契約"} {
		if !strings.Contains(s, want) {
			t.Errorf("project view missing %q:\n%s", want, s)
		}
	}
	h.key("enter")
	if h.m.focus != paneList {
		t.Fatalf("enter on sidebar should focus the list")
	}
	h.key("tab")
	if h.m.focus != paneDetail {
		t.Fatalf("tab should focus detail")
	}
}

func TestSearchAndFilter(t *testing.T) {
	h := newHarness(t, 140, 30)
	h.key("/", "契", "約")
	s := h.screen()
	if !strings.Contains(s, "検索結果 (2)") || !strings.Contains(s, "ガスの契約") {
		t.Fatalf("search across all tasks expected:\n%s", s)
	}
	h.key("enter")
	if h.m.searching || h.m.query != "契約" {
		t.Fatalf("enter should keep the query and leave the input")
	}
	h.key("esc")
	if h.m.query != "" || !strings.Contains(h.screen(), "期限切れ") {
		t.Fatalf("esc should clear the search")
	}

	h.chooseFilter(filterDueWeek)
	if s := h.screen(); strings.Contains(s, "ソファを買う") == false || !strings.Contains(s, "[7日以内が期限]") {
		t.Fatalf("due-within-week filter:\n%s", s)
	}
	h.chooseFilter(filterOverdue)
	if s := h.screen(); strings.Contains(s, "ソファを買う") || !strings.Contains(s, "転居届を出す") {
		t.Fatalf("overdue filter:\n%s", s)
	}
	h.chooseFilter(filterCompleted)
	if s := h.screen(); !strings.Contains(s, "年末調整") {
		t.Fatalf("completed filter:\n%s", s)
	}
}

func TestNarrowLayoutShowsOnePane(t *testing.T) {
	h := newHarness(t, 60, 20)
	s := h.screen()
	if strings.Count(s, "╭") != 1 {
		t.Fatalf("narrow layout should show only the focused pane:\n%s", s)
	}
	h.key("enter")
	if s := h.screen(); !strings.Contains(s, "詳細") {
		t.Fatalf("enter should open the detail pane:\n%s", s)
	}
	h.key("q")
	if h.m.focus != paneList {
		t.Fatalf("q in narrow mode should go back")
	}
}

func TestRenderFitsTerminal(t *testing.T) {
	for _, size := range [][2]int{{140, 30}, {100, 24}, {60, 15}} {
		h := newHarness(t, size[0], size[1])
		lines := strings.Split(h.m.render(), "\n")
		if len(lines) != size[1] {
			t.Errorf("%dx%d: rendered %d lines", size[0], size[1], len(lines))
		}
		for i, l := range lines {
			if w := ansi.StringWidth(l); w > size[0] {
				t.Errorf("%dx%d: line %d is %d wide", size[0], size[1], i, w)
			}
		}
	}
}

func TestShortDue(t *testing.T) {
	for in, want := range map[string]string{
		"": "", "2026-11-13": "今日", "2026-11-14": "明日", "2026-11-12": "昨日",
		"2026-11-17": "火曜", "2026-12-25": "12/25", "2027-01-05": "27/1/5",
	} {
		if got := shortDue(in, testNow); got != want {
			t.Errorf("shortDue(%q) = %q, want %q", in, got, want)
		}
	}
}

func (h *harness) typeText(s string) {
	for _, r := range s {
		if r == ' ' {
			h.send(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
			continue
		}
		h.send(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func TestToggleCompleteOffline(t *testing.T) {
	h := newHarness(t, 140, 30)
	h.key("x") // complete 転居届を出す (t2)
	s := h.screen()
	for _, r := range h.m.rows {
		if r.task != nil && r.task.GID == "t2" {
			t.Fatalf("completed task should leave the open-tasks list:\n%s", s)
		}
	}
	if !strings.Contains(s, "↑ 未送信 1") {
		t.Fatalf("header should show the pending change:\n%s", s)
	}
	h.chooseFilter(filterCompleted)
	if !strings.Contains(h.screen(), "転居届を出す ↑") {
		t.Fatalf("completed task should be marked as unsent:\n%s", h.screen())
	}
	h.key("x") // reopen: reverts the change entirely
	if strings.Contains(h.screen(), "未送信") {
		t.Fatalf("reverting should clear the pending change:\n%s", h.screen())
	}
}

func TestCreateTaskInProject(t *testing.T) {
	h := newHarness(t, 140, 30)
	h.key("h", "j", "j", "l") // project 新居プロジェクト, focus list (first task is in 家具)
	h.key("a")
	h.typeText("カーテンを買う due:明日")
	h.key("enter")
	s := h.screen()
	if !strings.Contains(s, "カーテンを買う ↑") {
		t.Fatalf("created task should appear, marked unsent:\n%s", s)
	}
	if got := h.m.selectedTask(); got == nil || got.Name != "カーテンを買う" || got.DueOn != "2026-11-14" {
		t.Fatalf("new task should be selected with due date: %+v", got)
	}
	if ms := h.m.selectedTask().Memberships; len(ms) != 1 || ms[0].SectionName != "家具" {
		t.Fatalf("new task should go to the section of the selected task: %+v", ms)
	}
}

func TestCommentAndOutboxOverlay(t *testing.T) {
	h := newHarness(t, 140, 30)
	h.key("c")
	h.typeText("区役所は9時から")
	h.key("enter")
	if s := h.screen(); !strings.Contains(s, "区役所は9時から") || !strings.Contains(s, "（未送信）") {
		t.Fatalf("comment should show in detail as unsent:\n%s", s)
	}
	h.key("!")
	s := h.screen()
	if !strings.Contains(s, "未送信の変更") || !strings.Contains(s, "コメント: 転居届を出す「区役所は9時から」") {
		t.Fatalf("outbox overlay:\n%s", s)
	}
	h.key("d") // discard
	if s := h.screen(); !strings.Contains(s, "未送信の変更はありません") {
		t.Fatalf("discard should empty the outbox:\n%s", s)
	}
	h.key("esc")
	if s := h.screen(); strings.Contains(s, "区役所は9時から") {
		t.Fatalf("discarded comment should be gone:\n%s", s)
	}
}

func TestPromptEscCancels(t *testing.T) {
	h := newHarness(t, 140, 30)
	h.key("a")
	h.typeText("やめる")
	h.key("esc")
	if h.m.promptMode != promptNone || strings.Contains(h.screen(), "やめる") {
		t.Fatal("esc should cancel without creating")
	}
}

func TestParseCreateInput(t *testing.T) {
	for in, want := range map[string][2]string{
		"牛乳を買う":              {"牛乳を買う", ""},
		"牛乳を買う due:明日":       {"牛乳を買う", "2026-11-14"},
		"due:+3d 歯医者 予約":     {"歯医者 予約", "2026-11-16"},
		"年賀状 due:2026-12-20": {"年賀状", "2026-12-20"},
	} {
		name, due, err := parseCreateInput(in, testNow)
		if err != nil || name != want[0] || due != want[1] {
			t.Errorf("parseCreateInput(%q) = %q, %q, %v", in, name, due, err)
		}
	}
	if _, _, err := parseCreateInput("x due:来週", testNow); err == nil {
		t.Error("invalid due should fail")
	}
}

func addFixtureExtras(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	ref := func(gid, name string) *asana.Ref { return &asana.Ref{GID: gid, Name: name} }
	in := func(sec string) []asana.Membership {
		return []asana.Membership{{Project: ref("p1", ""), Section: ref(sec, "")}}
	}
	if err := st.UpsertTasks(ctx, []asana.Task{
		{GID: "t1", Name: "ソファを買う", Notes: "リビング用", DueOn: "2026-11-20", Assignee: ref("me", "Me"), Memberships: in("s1"),
			Tags: []asana.Ref{{GID: "g1", Name: "買い物"}}},
		{GID: "t4", Name: "ガスの契約", Assignee: ref("u2", "Partner"), Memberships: in("s2"), Tags: []asana.Ref{{GID: "g2", Name: "手続き系"}}},
	}); err != nil {
		t.Fatal(err)
	}
	var stories []asana.Story
	for i := 1; i <= 5; i++ {
		stories = append(stories, asana.Story{GID: fmt.Sprintf("c%d", i), Type: "comment", Text: fmt.Sprintf("コメント%d番", i),
			CreatedBy: ref("u2", "Partner"), CreatedAt: testNow.Add(time.Duration(i) * time.Minute)})
	}
	if err := st.ReplaceStories(ctx, "t1", stories, testNow); err != nil {
		t.Fatal(err)
	}
}

func newHarnessWithExtras(t *testing.T, w, h int) *harness {
	st := fixtureStore(t)
	addFixtureExtras(t, st)
	m := New(context.Background(), Deps{Store: st, Now: func() time.Time { return testNow }})
	hs := &harness{t: t, m: m}
	hs.send(tea.WindowSizeMsg{Width: w, Height: h})
	hs.run(m.Init())
	return hs
}

func TestBoardViewAndCardMoves(t *testing.T) {
	h := newHarnessWithExtras(t, 140, 30)
	h.key("v") // My Tasks: list -> calendar (no board outside projects)
	if h.m.boardActive() || !h.m.calendarActive() {
		t.Fatal("v in My Tasks should switch to the calendar")
	}
	h.key("v")                     // back to the list
	h.key("h", "j", "j", "l", "v") // open the project, show the board
	if !h.m.boardActive() {
		t.Fatal("board should be active")
	}
	s := h.screen()
	for _, want := range []string{"[ボード]", "家具 (1)", "手続き (3)", "ソファを買う", "ガスの契約"} {
		if !strings.Contains(s, want) {
			t.Fatalf("board missing %q:\n%s", want, s)
		}
	}
	if h.selected() != "t1" {
		t.Fatalf("selected %q", h.selected())
	}
	h.key("L") // move ソファ to 手続き
	if s := h.screen(); !strings.Contains(s, "家具 (0)") || !strings.Contains(s, "手続き (4)") || !strings.Contains(s, "↑ 未送信 1") {
		t.Fatalf("card should move to the next column:\n%s", s)
	}
	if h.selected() != "t1" || h.m.boardCol != 1 {
		t.Fatalf("cursor should follow the moved card: %q col %d", h.selected(), h.m.boardCol)
	}
	h.key("H") // and back: the move cancels out
	if s := h.screen(); !strings.Contains(s, "家具 (1)") || strings.Contains(s, "未送信") {
		t.Fatalf("moving back should cancel the move:\n%s", s)
	}
	h.key("l", "j")
	if h.selected() != "t3" { // 手続き: t2, t3, t4 by due date
		t.Fatalf("j in column should move down, got %q", h.selected())
	}
	h.key("enter")
	if !h.m.zoom || !strings.Contains(h.screen(), "詳細（z / esc で戻る）") {
		t.Fatalf("enter on a card should open the zoomed detail:\n%s", h.screen())
	}
	h.key("esc", "v", "v") // board -> calendar -> list
	if h.m.boardActive() || h.selected() != "t3" {
		t.Fatalf("v should go back to the list keeping the selection, got %q", h.selected())
	}
}

func TestZoomShowsAllComments(t *testing.T) {
	h := newHarnessWithExtras(t, 140, 40)
	h.key("G") // ソファを買う (t1)
	s := h.screen()
	if !strings.Contains(s, "古いコメント 2 件") || strings.Contains(s, "コメント1番") || !strings.Contains(s, "コメント5番") {
		t.Fatalf("side pane should show only the latest 3 comments:\n%s", s)
	}
	h.key("z")
	s = h.screen()
	if strings.Contains(s, "古いコメント") || !strings.Contains(s, "コメント1番") || strings.Contains(s, "★ My Tasks") {
		t.Fatalf("zoom should show every comment, full screen:\n%s", s)
	}
	h.key("z")
	if h.m.zoom || h.m.focus != paneList {
		t.Fatal("z should toggle the zoom off")
	}
}

func TestTagFilter(t *testing.T) {
	h := newHarnessWithExtras(t, 140, 30)
	h.key("T")
	s := h.screen()
	if !strings.Contains(s, "タグで絞り込む") || !strings.Contains(s, "#買い物") || !strings.Contains(s, "#手続き系") {
		t.Fatalf("tag picker:\n%s", s)
	}
	h.key("j", "enter") // #手続き系 sorts before #買い物? order is by name: 手続き系, 買い物
	tag := h.m.tagFilter
	if tag == "" {
		t.Fatal("tag filter should be set")
	}
	h.key("h", "j", "j", "l") // project view shows all assignees
	var gids []string
	for _, r := range h.m.rows {
		if r.task != nil {
			gids = append(gids, r.task.GID)
		}
	}
	want := map[string]string{"買い物": "t1", "手続き系": "t4"}[tag]
	if len(gids) != 1 || gids[0] != want || !strings.Contains(h.screen(), "#"+tag) {
		t.Fatalf("tag %q: rows %v", tag, gids)
	}
	h.key("T", "g", "enter") // "（すべてのタスク）"
	if h.m.tagFilter != "" {
		t.Fatal("first picker item should clear the filter")
	}
}

func TestMoveWithSectionPicker(t *testing.T) {
	h := newHarnessWithExtras(t, 140, 30)
	h.key("G") // ソファを買う in My Tasks (one project → picker allowed)
	h.key("m")
	if s := h.screen(); !strings.Contains(s, "移動先のセクション（新居プロジェクト）") || !strings.Contains(s, "現在") {
		t.Fatalf("section picker:\n%s", s)
	}
	h.key("j", "enter")
	d, err := h.m.deps.Store.GetTask(context.Background(), "t1")
	if err != nil || d.Memberships[0].SectionName != "手続き" || !d.Pending {
		t.Fatalf("task should move to 手続き: %+v %v", d.Memberships, err)
	}
}

func TestBoardAndZoomFitTerminal(t *testing.T) {
	for _, size := range [][2]int{{140, 30}, {90, 20}} {
		h := newHarnessWithExtras(t, size[0], size[1])
		h.key("h", "j", "j", "l", "v")
		for _, step := range []string{"board", "zoom"} {
			if step == "zoom" {
				h.key("enter")
			}
			lines := strings.Split(h.m.render(), "\n")
			if len(lines) != size[1] {
				t.Errorf("%s %dx%d: %d lines", step, size[0], size[1], len(lines))
			}
			for i, l := range lines {
				if w := ansi.StringWidth(l); w > size[0] {
					t.Errorf("%s %dx%d: line %d is %d wide", step, size[0], size[1], i, w)
				}
			}
		}
	}
}

func projectHarness(t *testing.T, st *store.Store, viewImages func([]string) tea.Cmd) *harness {
	m := New(context.Background(), Deps{Store: st, Now: func() time.Time { return testNow }, ViewImages: viewImages})
	hs := &harness{t: t, m: m}
	hs.send(tea.WindowSizeMsg{Width: 140, Height: 30})
	hs.run(m.Init())
	hs.key("h", "j", "j", "l") // open 新居プロジェクト, focus the list
	return hs
}

func (h *harness) visibleGIDs() []string {
	var out []string
	for _, r := range h.m.rows {
		if r.task != nil {
			out = append(out, r.task.GID)
		}
	}
	return out
}

func TestCollapseSections(t *testing.T) {
	st := fixtureStore(t)
	h := projectHarness(t, st, nil)
	if got := h.visibleGIDs(); len(got) != 4 { // 家具: t1, 手続き: t2 t3 t4
		t.Fatalf("visible %v", got)
	}
	h.key("-") // collapse 家具 (cursor on t1)
	s := h.screen()
	if !strings.Contains(s, "▸ 家具 (1)") || slices.Contains(h.visibleGIDs(), "t1") {
		t.Fatalf("家具 should be collapsed:\n%s", s)
	}
	if h.selected() != "" || h.m.rows[h.m.listCursor].key != "s1" {
		t.Fatal("cursor should sit on the collapsed header")
	}
	h.key("j")
	if h.selected() != "t2" {
		t.Fatalf("j should move past the collapsed group, got %q", h.selected())
	}

	// The collapsed state survives a restart (it is stored in the local store).
	h2 := projectHarness(t, st, nil)
	if got := h2.visibleGIDs(); len(got) != 3 {
		t.Fatalf("collapsed state not restored: %v", got)
	}
	h2.key("g", "enter") // expand with enter on the header
	if got := h2.visibleGIDs(); len(got) != 4 {
		t.Fatalf("enter should expand: %v", got)
	}

	h2.key("_")
	if got := h2.visibleGIDs(); len(got) != 0 || !strings.Contains(h2.screen(), "▸ 手続き (3)") {
		t.Fatalf("_ should collapse all:\n%s", h2.screen())
	}
	h2.key("=")
	if got := h2.visibleGIDs(); len(got) != 4 {
		t.Fatalf("= should expand all: %v", got)
	}
	if !strings.Contains(h2.screen(), "タスク (4)") {
		t.Fatal("title counts all tasks")
	}
}

func TestRenameSectionFromListAndBoard(t *testing.T) {
	st := fixtureStore(t)
	h := projectHarness(t, st, nil)
	h.key("j", "E") // cursor in 手続き
	if h.m.prompt.Value() != "手続き" {
		t.Fatalf("prompt should be prefilled, got %q", h.m.prompt.Value())
	}
	h.typeText("（役所）")
	h.key("enter")
	if s := h.screen(); !strings.Contains(s, "▾ 手続き（役所）") || !strings.Contains(s, "↑ 未送信 1") {
		t.Fatalf("renamed section:\n%s", s)
	}
	h.key("v", "E") // board opens on the selected task's column (手続き（役所）)
	if h.m.prompt.Value() != "手続き（役所）" {
		t.Fatalf("board rename should target the column, got %q", h.m.prompt.Value())
	}
	h.key("esc")
	h.key("!")
	if s := h.screen(); !strings.Contains(s, "セクション名の変更: 手続き → 手続き（役所）") {
		t.Fatalf("outbox:\n%s", s)
	}
}

func TestViewImages(t *testing.T) {
	st := fixtureStore(t)
	ctx := context.Background()
	dir := t.TempDir()
	cached := filepath.Join(dir, "a1-plan.png")
	if err := os.WriteFile(cached, []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = st.ReplaceAttachments(ctx, "t1", []asana.Attachment{{GID: "a1", Name: "plan.png", Size: 2048}, {GID: "a2", Name: "見積.pdf"}})
	_ = st.SetAttachmentPath(ctx, "a1", cached)
	_ = st.ReplaceStories(ctx, "t1", nil, testNow)

	var viewed []string
	h := projectHarness(t, st, func(paths []string) tea.Cmd { viewed = paths; return nil })
	s := h.screen()
	for _, want := range []string{"添付ファイル (2)", "画像 1 件（space i で全画面表示）", "[画像] plan.png", "2KB", "✓保存済み", "[ファイル] 見積.pdf"} {
		if !strings.Contains(s, want) {
			t.Fatalf("detail missing %q:\n%s", want, s)
		}
	}
	h.key("i") // offline: uses the cached file, skips the non-image
	if len(viewed) != 1 || viewed[0] != cached {
		t.Fatalf("viewed %v", viewed)
	}

	h.key("j", "i") // t2 has no attachments
	if !strings.Contains(h.screen(), "添付ファイルはまだ取得していません") && !strings.Contains(h.screen(), "画像はありません") {
		t.Fatalf("status for no images:\n%s", h.screen())
	}
}

func TestInlineImagesInNotes(t *testing.T) {
	st := fixtureStore(t)
	ctx := context.Background()
	notes := "めっちゃ見にくいな\n\nこれ\n\nhttps://app.asana.com/app/asana/-/get_asset?asset_id=1220507679870013\n\nこれ"
	if err := st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "ソファを買う", Notes: notes, DueOn: "2026-11-20",
		Assignee: &asana.Ref{GID: "me"}, Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}, Section: &asana.Ref{GID: "s1"}}}}}); err != nil {
		t.Fatal(err)
	}
	var viewed []string
	h := projectHarness(t, st, func(p []string) tea.Cmd { viewed = p; return nil })
	s := h.screen()
	if strings.Contains(s, "get_asset") || !strings.Contains(s, "[画像1]") || !strings.Contains(s, "画像 1 件（space i で全画面表示）") {
		t.Fatalf("inline image link should be shown as [画像1]:\n%s", s)
	}
	h.key("i") // offline and not cached: cannot fetch
	if len(viewed) != 0 || !strings.Contains(h.screen(), "オフラインのため画像を取得できません") {
		t.Fatalf("offline inline image:\n%s", h.screen())
	}
}

func TestMarkdownDescriptionAndComments(t *testing.T) {
	st := fixtureStore(t)
	ctx := context.Background()
	if err := st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "ソファを買う", Notes: "plain", DueOn: "2026-11-20",
		HTMLNotes: "<body><h1>候補</h1><strong>予算</strong>は10万円\n<ul><li>ニトリ</li><li>無印</li></ul></body>",
		Assignee:  &asana.Ref{GID: "me"}, Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}, Section: &asana.Ref{GID: "s1"}}}}}); err != nil {
		t.Fatal(err)
	}
	_ = st.ReplaceStories(ctx, "t1", []asana.Story{{GID: "c1", Type: "comment", Text: "x",
		HTMLText: "<body><em>見に行った</em></body>", CreatedBy: &asana.Ref{GID: "u2", Name: "Partner"}}}, testNow)
	h := projectHarness(t, st, nil)
	s := h.screen()
	for _, want := range []string{"候補", "予算は10万円", "• ニトリ", "• 無印", "見に行った"} {
		if !strings.Contains(s, want) {
			t.Fatalf("markdown detail missing %q:\n%s", want, s)
		}
	}
	for _, unwanted := range []string{"<strong>", "**予算**", "<li>", "plain"} {
		if strings.Contains(s, unwanted) {
			t.Fatalf("raw markup %q should not be shown:\n%s", unwanted, s)
		}
	}
}

func TestDetailWidthAndSidebar(t *testing.T) {
	st := fixtureStore(t)
	h := projectHarness(t, st, nil)
	_, _, before := h.m.paneWidths()
	h.key(">", ">")
	_, lw, after := h.m.paneWidths()
	if after <= before || h.m.detailRatio != 60 {
		t.Fatalf("> should widen the detail: %d -> %d (ratio %d)", before, after, h.m.detailRatio)
	}
	h.key(">", ">", ">", ">")
	if _, lw, _ = h.m.paneWidths(); lw < minListWidth {
		t.Fatalf("list must stay usable, got %d", lw)
	}
	h.key("b")
	if sw, _, _ := h.m.paneWidths(); sw != 0 || strings.Contains(h.screen(), "★ My Tasks") {
		t.Fatal("b should hide the sidebar")
	}
	// Layout is remembered across restarts.
	m2 := New(context.Background(), Deps{Store: st, Now: func() time.Time { return testNow }})
	h2 := &harness{t: t, m: m2}
	h2.send(tea.WindowSizeMsg{Width: 140, Height: 30})
	h2.run(m2.Init())
	if !h2.m.hideSidebar || h2.m.detailRatio != 80 {
		t.Fatalf("layout not restored: hide=%v ratio=%d", h2.m.hideSidebar, h2.m.detailRatio)
	}
	h2.key("h") // from the list, h brings the sidebar back
	if h2.m.hideSidebar || h2.m.focus != paneSidebar {
		t.Fatal("h should show the sidebar again")
	}
	for _, size := range [][2]int{{140, 30}, {100, 24}} {
		h.send(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		for i, l := range strings.Split(h.m.render(), "\n") {
			if w := ansi.StringWidth(l); w > size[0] {
				t.Errorf("%v line %d is %d wide", size, i, w)
			}
		}
	}
}

func TestInlineImagePlaceholders(t *testing.T) {
	st := fixtureStore(t)
	ctx := context.Background()
	dir := t.TempDir()
	img := image.NewRGBA(image.Rect(0, 0, 200, 100))
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	path := filepath.Join(dir, "a9-photo.png")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "ソファを買う", DueOn: "2026-11-20",
		HTMLNotes: `<body>上<img data-asana-gid="9">下</body>`,
		Assignee:  &asana.Ref{GID: "me"}, Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}, Section: &asana.Ref{GID: "s1"}}}}}); err != nil {
		t.Fatal(err)
	}
	_ = st.UpsertAttachment(ctx, "t1", asana.Attachment{GID: "9", Name: "photo.png"})
	_ = st.SetAttachmentPath(ctx, "9", path)

	m := New(context.Background(), Deps{Store: st, Now: func() time.Time { return testNow }, InlineImages: true})
	m.cellW, m.cellH = 10, 20
	h := &harness{t: t, m: m}
	h.send(tea.WindowSizeMsg{Width: 140, Height: 30})
	h.run(m.Init())
	h.key("h", "j", "j", "l")

	out := h.m.render()
	if !strings.Contains(out, "\U0010EEEE") {
		t.Fatalf("detail should contain kitty placeholders:\n%s", ansi.Strip(out))
	}
	plain := ansi.Strip(out)
	if strings.Index(plain, "上") > strings.Index(plain, "\U0010EEEE") || strings.Index(plain, "\U0010EEEE") > strings.Index(plain, "下") {
		t.Fatal("image should be drawn between 上 and 下")
	}
	got := h.m.inline["9"]
	if got == nil || got.sentCols != 20 || got.sentRows != 5 { // 200x100 px with 10x20 cells
		t.Fatalf("placement = %+v", got)
	}
	for i, l := range strings.Split(out, "\n") {
		if w := ansi.StringWidth(l); w > 140 {
			t.Errorf("line %d is %d wide", i, w)
		}
	}
}

func addSubtasks(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	ref := func(gid, name string) *asana.Ref { return &asana.Ref{GID: gid, Name: name} }
	if err := st.UpsertTasks(ctx, []asana.Task{
		{GID: "t1", Name: "ソファを買う", DueOn: "2026-11-20", Assignee: ref("me", "Me"), NumSubtasks: 2,
			Memberships: []asana.Membership{{Project: ref("p1", ""), Section: ref("s1", "")}}},
		{GID: "t1a", Name: "サイズを測る", Parent: ref("t1", ""), NumSubtasks: 1},
		{GID: "t1b", Name: "予算を決める", Parent: ref("t1", ""), Assignee: ref("me", "Me")},
		{GID: "t1a1", Name: "メジャーを買う", Parent: ref("t1a", "")},
	}); err != nil {
		t.Fatal(err)
	}
	_ = st.UpsertUsers(ctx, []asana.User{{GID: "me", Name: "Me"}, {GID: "u2", Name: "Partner"}})
}

func TestSubtaskTree(t *testing.T) {
	st := fixtureStore(t)
	addSubtasks(t, st)
	h := projectHarness(t, st, nil) // cursor on ソファを買う (t1)
	if !strings.Contains(h.screen(), "ソファを買う ↳2") {
		t.Fatalf("subtask count:\n%s", h.screen())
	}
	h.key("l") // expand
	s := h.screen()
	for _, want := range []string{"ソファを買う ▾2", "└ ☐ サイズを測る ↳1", "└ ☐ 予算を決める"} {
		if !strings.Contains(s, want) {
			t.Fatalf("expanded tree missing %q:\n%s", want, s)
		}
	}
	if !strings.Contains(s, "タスク (4)") {
		t.Fatal("expanded subtasks should not change the task count")
	}
	h.key("j", "l") // into サイズを測る, expand it
	if !strings.Contains(h.screen(), "  └ ☐ メジャーを買う") {
		t.Fatalf("nested subtask:\n%s", h.screen())
	}
	h.key("j", "h") // on メジャーを買う: h jumps to its parent
	if h.selected() != "t1a" {
		t.Fatalf("h should move to the parent, got %q", h.selected())
	}
	h.key("h") // collapse t1a
	if slices.Contains(h.visibleGIDs(), "t1a1") {
		t.Fatal("h on an expanded task should collapse it")
	}
	h.key("x") // complete a subtask from the tree
	if d, _ := st.GetTask(context.Background(), "t1a"); !d.Completed || !d.Pending {
		t.Fatalf("subtask should be completed locally: %+v", d.TaskSummary)
	}
	h.key("v") // board: subtasks are not cards
	if strings.Contains(h.screen(), "予算を決める") {
		t.Fatalf("board should not show subtasks as cards:\n%s", h.screen())
	}
}

func TestSubtaskShownWithParentInMyTasks(t *testing.T) {
	st := fixtureStore(t)
	addSubtasks(t, st)
	m := New(context.Background(), Deps{Store: st, Now: func() time.Time { return testNow }})
	h := &harness{t: t, m: m}
	h.send(tea.WindowSizeMsg{Width: 160, Height: 30})
	h.run(m.Init())
	if !strings.Contains(h.screen(), "予算を決める  ⊂ ソファを買う") {
		t.Fatalf("subtask in My Tasks should show its parent:\n%s", h.screen())
	}
}

func TestCreateSubtask(t *testing.T) {
	st := fixtureStore(t)
	addSubtasks(t, st)
	h := projectHarness(t, st, nil)
	h.key("S")
	if !strings.Contains(h.screen(), "サブタスク（ソファを買う）") {
		t.Fatalf("prompt:\n%s", h.screen())
	}
	h.typeText("配送日を決める due:明日")
	h.key("enter")
	s := h.screen()
	if !strings.Contains(s, "ソファを買う ▾3") || !strings.Contains(s, "└ ☐ 配送日を決める ↑") {
		t.Fatalf("new subtask should appear under its expanded parent:\n%s", s)
	}
	if got := h.m.selectedTask(); got == nil || got.Name != "配送日を決める" || got.DueOn != "2026-11-14" || got.ParentGID != "t1" {
		t.Fatalf("new subtask should be selected: %+v", got)
	}
}

func TestChangeAssignee(t *testing.T) {
	st := fixtureStore(t)
	addSubtasks(t, st)
	h := projectHarness(t, st, nil) // ソファを買う, assigned to Me
	h.key("@")
	s := h.screen()
	for _, want := range []string{"担当者（ソファを買う）", "Me（自分）  現在", "Partner", "（未割り当て）"} {
		if !strings.Contains(s, want) {
			t.Fatalf("assignee picker missing %q:\n%s", want, s)
		}
	}
	h.key("j", "enter") // Partner
	d, _ := st.GetTask(context.Background(), "t1")
	if d.Assignee == nil || d.Assignee.GID != "u2" || !d.Pending {
		t.Fatalf("assignee should be Partner: %+v", d.Assignee)
	}
	h.key("@", "G", "enter") // unassign
	if d, _ := st.GetTask(context.Background(), "t1"); d.Assignee != nil {
		t.Fatalf("should be unassigned: %+v", d.Assignee)
	}
}

func TestDetailSubtaskNavigation(t *testing.T) {
	st := fixtureStore(t)
	addSubtasks(t, st)
	h := projectHarness(t, st, nil) // ソファを買う
	h.key("enter")
	if !h.m.zoom || !strings.Contains(h.screen(), "＋ サブタスクを追加") {
		t.Fatalf("enter in the list should open the detail full screen:\n%s", h.screen())
	}
	h.cursorToSubtask("t1b") // 予算を決める
	h.key("x")               // completes the selected subtask, not the parent
	if d, _ := st.GetTask(context.Background(), "t1b"); !d.Completed {
		t.Fatal("x should complete the selected subtask")
	}
	if d, _ := st.GetTask(context.Background(), "t1"); d.Completed {
		t.Fatal("parent must stay open")
	}
	h.cursorToSubtask("t1a")
	h.key("enter") // open サイズを測る
	s := h.screen()
	if !strings.Contains(s, "← backspace で戻る") || !strings.Contains(s, "サイズを測る") || !strings.Contains(s, "メジャーを買う") {
		t.Fatalf("subtask detail:\n%s", s)
	}
	h.key("S")
	h.typeText("巻き尺を借りる")
	h.key("enter")
	d, _ := st.GetTask(context.Background(), "t1a")
	if len(d.Subtasks) != 2 || !strings.Contains(h.screen(), "巻き尺を借りる") {
		t.Fatalf("S in the detail should add a subtask to the shown task:\n%s", h.screen())
	}
	if h.m.detailGID() != "t1a" {
		t.Fatalf("detail should stay on the subtask, got %q", h.m.detailGID())
	}
	h.key("backspace")
	if h.m.detailGID() != "t1" || h.m.selectedSubtask() == nil || h.m.selectedSubtask().GID != "t1a" {
		t.Fatalf("backspace should return to the parent with the subtask selected: %q %+v", h.m.detailGID(), h.m.selectedSubtask())
	}
	h.key("esc")
	if h.m.zoom {
		t.Fatal("esc should close the full screen detail")
	}
}

// chooseFilter picks a filter from the f chooser.
func (h *harness) chooseFilter(f filterMode) {
	h.key("f", "g")
	for range int(f) {
		h.key("j")
	}
	h.key("enter")
}

// cursorToSubtask moves the detail cursor onto a subtask.
func (h *harness) cursorToSubtask(gid string) {
	h.t.Helper()
	h.key("g")
	for range 40 {
		if sub := h.m.selectedSubtask(); sub != nil && sub.GID == gid {
			return
		}
		h.key("j")
	}
	h.t.Fatalf("subtask %s not reachable in the detail", gid)
}

// fakeEditor replaces the file content when "run".
func fakeEditor(t *testing.T, edit func(string) string) func(string, func(error) tea.Msg) tea.Cmd {
	return func(path string, done func(error) tea.Msg) tea.Cmd {
		return func() tea.Msg {
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(edit(string(b))), 0o600); err != nil {
				t.Fatal(err)
			}
			return done(nil)
		}
	}
}

func TestEditTaskInEditor(t *testing.T) {
	st := fixtureStore(t)
	addSubtasks(t, st)
	ctx := context.Background()
	_ = st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "ソファを買う", DueOn: "2026-11-20", NumSubtasks: 2,
		HTMLNotes: "<body>候補は<strong>ニトリ</strong></body>", Assignee: &asana.Ref{GID: "me", Name: "Me"},
		Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}, Section: &asana.Ref{GID: "s1"}}}}})
	var seen string
	m := New(ctx, Deps{Store: st, Now: func() time.Time { return testNow }, RunEditor: fakeEditor(t, func(s string) string {
		seen = s
		s = strings.Replace(s, "name: ソファを買う", "name: ソファを買う（3人掛け）", 1)
		s = strings.Replace(s, "due: 2026-11-20", "due: +7d", 1)
		s = strings.Replace(s, "\nassignee: me\n", "\nassignee: Partner\n", 1)
		s = strings.Replace(s, "section: 家具", "section: 手続き", 1)
		return s + "\n## 予算\n- 10万円まで\n"
	})})
	h := &harness{t: t, m: m}
	h.send(tea.WindowSizeMsg{Width: 140, Height: 30})
	h.run(m.Init())
	h.key("h", "j", "j", "l", "e")

	for _, want := range []string{"name: ソファを買う\n", "due: 2026-11-20\n", "assignee: me\n", "completed: false\n", "section: 家具\n", "候補は**ニトリ**"} {
		if !strings.Contains(seen, want) {
			t.Fatalf("editor file missing %q:\n%s", want, seen)
		}
	}
	d, _ := st.GetTask(ctx, "t1")
	// due: +7d from testNow (11/13) is 11/20, the same date, so the due date is unchanged.
	if d.DueOn != "2026-11-20" {
		t.Fatalf("due = %q", d.DueOn)
	}
	if d.Name != "ソファを買う（3人掛け）" || d.Assignee == nil || d.Assignee.GID != "u2" || d.Memberships[0].SectionName != "手続き" {
		t.Fatalf("task after edit: %+v assignee=%+v", d.TaskSummary, d.Assignee)
	}
	if !strings.Contains(d.HTMLNotes, "<strong>ニトリ</strong>") || !strings.Contains(d.HTMLNotes, "<h2>予算</h2>") || !strings.Contains(d.HTMLNotes, "<li>10万円まで</li>") {
		t.Fatalf("html_notes = %q", d.HTMLNotes)
	}
	ops, _ := st.ListOps(ctx)
	var kinds []string
	for _, op := range ops {
		kinds = append(kinds, op.Kind)
		if op.Kind == store.OpUpdateTask {
			if _, ok := op.Payload["html_notes"]; !ok {
				t.Fatalf("update should carry html_notes: %+v", op.Payload)
			}
		}
	}
	if !slices.Contains(kinds, store.OpUpdateTask) || !slices.Contains(kinds, store.OpMoveTask) {
		t.Fatalf("ops = %v", kinds)
	}
	if s := h.screen(); !strings.Contains(s, "予算") || !strings.Contains(s, "ソファを買う（3人掛け）") {
		t.Fatalf("detail should show the edited task:\n%s", s)
	}
}

func TestEditTaskErrorsAndNoChange(t *testing.T) {
	st := fixtureStore(t)
	editFn := func(s string) string { return s }
	m := New(context.Background(), Deps{Store: st, Now: func() time.Time { return testNow },
		RunEditor: func(path string, done func(error) tea.Msg) tea.Cmd { return fakeEditor(t, editFn)(path, done) }})
	h := &harness{t: t, m: m}
	h.send(tea.WindowSizeMsg{Width: 140, Height: 30})
	h.run(m.Init())
	h.key("e")
	if !strings.Contains(h.screen(), "変更はありません") {
		t.Fatalf("unchanged file:\n%s", h.screen())
	}
	editFn = func(s string) string { return strings.Replace(s, "name: 転居届を出す", "name:", 1) }
	h.key("e")
	if s := h.screen(); !strings.Contains(s, "編集内容を読めませんでした") || !strings.Contains(s, "name（タスク名）が空です") {
		t.Fatalf("parse error should be reported:\n%s", s)
	}
}

func TestCommentInEditor(t *testing.T) {
	st := fixtureStore(t)
	m := New(context.Background(), Deps{Store: st, Now: func() time.Time { return testNow },
		RunEditor: fakeEditor(t, func(s string) string { return s + "**明日**行きます\n- 印鑑\n- 免許証\n" })})
	h := &harness{t: t, m: m}
	h.send(tea.WindowSizeMsg{Width: 140, Height: 30})
	h.run(m.Init())
	h.key("C")
	d, _ := st.GetTask(context.Background(), "t2")
	if len(d.Stories) != 1 || !strings.Contains(d.Stories[0].HTMLText, "<strong>明日</strong>") || !strings.Contains(d.Stories[0].Text, "明日行きます") {
		t.Fatalf("comment = %+v", d.Stories)
	}
	if s := h.screen(); !strings.Contains(s, "明日行きます") || !strings.Contains(s, "• 印鑑") {
		t.Fatalf("comment should render as markdown:\n%s", s)
	}
}

func TestWhichKeyMenu(t *testing.T) {
	st := fixtureStore(t)
	addSubtasks(t, st)
	h := projectHarness(t, st, nil)
	h.key("space")
	s := h.screen()
	for _, want := range []string{"メニュー", "タスク", "表示", "その他", "期日を変更", "担当者を変更", "タグを付ける / 外す", "表示を切り替え", "今すぐ同期"} {
		if !strings.Contains(s, want) {
			t.Fatalf("menu missing %q:\n%s", want, s)
		}
	}
	h.key("d") // space d: due date chooser
	if h.m.picker == nil || h.m.picker.kind != pickDue {
		t.Fatalf("space d should open the due chooser:\n%s", h.screen())
	}
	h.key("esc", "space", "esc")
	if h.m.leader || h.m.picker != nil {
		t.Fatal("esc should close the menu")
	}

	// In the sidebar there is no task: task actions are not offered.
	h.key("h", "space")
	if s := h.screen(); strings.Contains(s, "期日を変更") || !strings.Contains(s, "絞り込み") {
		t.Fatalf("sidebar menu should only offer what applies there:\n%s", s)
	}
	h.key("Z")
	if !strings.Contains(h.screen(), "「Z」はここでは使えません") {
		t.Fatal("unknown menu key should be reported")
	}

	h.key("?")
	if s := h.screen(); !strings.Contains(s, "ヘルプ") || !strings.Contains(s, "移動（この画面）") {
		t.Fatalf("help:\n%s", s)
	}
}

func TestDetailFieldsAreChosenFromLists(t *testing.T) {
	st := fixtureStore(t)
	addSubtasks(t, st)
	ctx := context.Background()
	h := projectHarness(t, st, nil) // ソファを買う, due 11/20, assignee Me
	h.key("enter")
	cursorTo := func(kind itemKind) {
		t.Helper()
		h.key("g")
		for range 20 {
			if it := h.m.currentItem(); it != nil && it.kind == kind {
				return
			}
			h.key("j")
		}
		t.Fatalf("item %d not reachable", kind)
	}

	cursorTo(itemDue)
	h.key("enter")
	if s := h.screen(); !strings.Contains(s, "明日") || !strings.Contains(s, "日付を入力…") || !strings.Contains(s, "期日なし") {
		t.Fatalf("due chooser:\n%s", s)
	}
	h.key("g", "j", "enter") // 明日
	if d, _ := st.GetTask(ctx, "t1"); d.DueOn != "2026-11-14" {
		t.Fatalf("due = %q", d.DueOn)
	}
	cursorTo(itemDue)
	h.key("enter", "G", "k", "enter") // 日付を入力…
	h.m.prompt.SetValue("")
	h.typeText("2026-12-24")
	h.key("enter")
	if d, _ := st.GetTask(ctx, "t1"); d.DueOn != "2026-12-24" {
		t.Fatalf("typed due = %q", d.DueOn)
	}
	cursorTo(itemDue)
	h.key("enter", "G", "enter") // 期日なし
	if d, _ := st.GetTask(ctx, "t1"); d.DueOn != "" {
		t.Fatalf("due should be cleared: %q", d.DueOn)
	}

	cursorTo(itemAssignee)
	h.key("enter")
	if h.m.picker == nil || h.m.picker.kind != pickAssignee {
		t.Fatal("enter on 担当 should open the member chooser")
	}
	h.key("esc")

	cursorTo(itemCompleted)
	h.key("enter")
	if d, _ := st.GetTask(ctx, "t1"); !d.Completed {
		t.Fatal("enter on 状態 should toggle completion")
	}
}

func TestTagEditor(t *testing.T) {
	st := fixtureStore(t)
	addSubtasks(t, st)
	ctx := context.Background()
	_ = st.UpsertWorkspaces(ctx, []asana.Workspace{{GID: "w1"}})
	_ = st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "ソファを買う", DueOn: "2026-11-20", NumSubtasks: 2, Workspace: &asana.Ref{GID: "w1"},
		Assignee: &asana.Ref{GID: "me"}, Tags: []asana.Ref{{GID: "g1", Name: "買い物"}},
		Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}, Section: &asana.Ref{GID: "s1"}}}}})
	_ = st.UpsertTags(ctx, []asana.Ref{{GID: "g2", Name: "急ぎ"}}) // a workspace tag no task uses yet
	h := projectHarness(t, st, nil)
	h.key("space", "t")
	s := h.screen()
	if !strings.Contains(s, "[✓] #買い物") || !strings.Contains(s, "[ ] #急ぎ") || !strings.Contains(s, "＋ 新しいタグを作る…") {
		t.Fatalf("tag chooser:\n%s", s)
	}
	h.key("g", "enter") // 急ぎ (sorted: 急ぎ, 買い物) — toggles on, chooser stays open
	if h.m.picker == nil || !strings.Contains(h.screen(), "[✓] #急ぎ") {
		t.Fatalf("toggle should keep the chooser open with the new state:\n%s", h.screen())
	}
	h.key("j", "space") // 買い物 off
	h.key("G", "enter")
	h.typeText("家具")
	h.key("enter")
	tags, _ := st.TaskTags(ctx, "t1")
	var names []string
	for _, tg := range tags {
		names = append(names, tg.Name)
	}
	if !slices.Equal(names, []string{"家具", "急ぎ"}) {
		t.Fatalf("tags = %v", names)
	}
	ops, _ := st.ListOps(ctx)
	var kinds []string
	for _, op := range ops {
		kinds = append(kinds, op.Kind)
	}
	if !slices.Equal(kinds, []string{store.OpAddTag, store.OpRemoveTag, store.OpAddTag}) {
		t.Fatalf("ops = %v", kinds)
	}
	// Toggling back cancels instead of queueing another change.
	h.key("space", "t", "g", "enter")
	if ops, _ := st.ListOps(ctx); len(ops) != 2 {
		t.Fatalf("removing a just-added tag should cancel it: %d ops", len(ops))
	}
}

func TestEditFileUsesLocalImagePaths(t *testing.T) {
	st := fixtureStore(t)
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "attachments", "55-plan.png")
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	_ = os.WriteFile(path, []byte("png"), 0o600)
	_ = st.UpsertTasks(ctx, []asana.Task{{GID: "t2", Name: "転居届を出す", DueOn: "2026-11-10", Assignee: &asana.Ref{GID: "me"},
		HTMLNotes: `<body>図<img data-asana-gid="55"></body>`}})
	_ = st.UpsertAttachment(ctx, "t2", asana.Attachment{GID: "55", Name: "plan.png"})
	_ = st.SetAttachmentPath(ctx, "55", path)
	var seen string
	m := New(ctx, Deps{Store: st, Now: func() time.Time { return testNow },
		RunEditor: fakeEditor(t, func(s string) string { seen = s; return s })})
	h := &harness{t: t, m: m}
	h.send(tea.WindowSizeMsg{Width: 140, Height: 30})
	h.run(m.Init())
	h.key("e")
	if !strings.Contains(seen, "![画像]("+path+")") {
		t.Fatalf("editor file should reference the local image file:\n%s", seen)
	}
}

func TestCalendarView(t *testing.T) {
	st := fixtureStore(t) // my tasks: t2 due 11/10, t3 due 11/13 (today), t1 due 11/20
	_ = st.UpsertWorkspaces(context.Background(), []asana.Workspace{{GID: "w1", Name: "WS"}})
	h := projectHarness(t, st, nil)
	h.key("h", "g", "l") // back to My Tasks
	h.key("v")
	s := h.screen()
	for _, want := range []string{"[カレンダー]", "カレンダー 2026年11月", "13 今日", "電気の契約", "転居届を出す", "ソファを買う"} {
		if !strings.Contains(s, want) {
			t.Fatalf("calendar missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "ガスの契約") {
		t.Fatal("calendar should show only my tasks with a due date")
	}
	h.key("enter") // today: 電気の契約
	if h.m.picker == nil || h.m.picker.kind != pickDayTask || !strings.Contains(h.screen(), "電気の契約") {
		t.Fatalf("enter should list the day's tasks:\n%s", h.screen())
	}
	h.key("enter")
	if !h.m.zoom || h.m.detailGID() != "t3" || !strings.Contains(h.screen(), "電気の契約") {
		t.Fatalf("choosing a task should open its detail:\n%s", h.screen())
	}
	h.key("esc")
	if h.m.zoom || !h.m.calendarActive() {
		t.Fatal("esc should go back to the calendar")
	}
	h.key("j") // +7 days: 11/20
	if h.m.calDay.Format(time.DateOnly) != "2026-11-20" {
		t.Fatalf("j should move a week, got %s", h.m.calDay.Format(time.DateOnly))
	}
	h.key("a")
	h.typeText("内見")
	h.key("enter")
	if s := h.screen(); !strings.Contains(s, "内見") {
		t.Fatalf("a in the calendar should add a task on the selected day:\n%s", s)
	}
	h.key("n") // next month
	if s := h.screen(); !strings.Contains(s, "2026年12月") || strings.Contains(s, "内見") {
		t.Fatalf("n should show December:\n%s", s)
	}
	h.key(".")
	if !strings.Contains(h.screen(), "2026年11月") {
		t.Fatal(". should return to today")
	}
	h.key("v")
	if h.m.calendarActive() {
		t.Fatal("v should go back to the list")
	}
}

func TestRecentUpdates(t *testing.T) {
	st := fixtureStore(t)
	ctx := context.Background()
	ref := func(gid string) *asana.Ref { return &asana.Ref{GID: gid} }
	_ = st.UpsertTasks(ctx, []asana.Task{
		{GID: "t2", Name: "転居届を出す", Assignee: ref("me"), ModifiedAt: testNow.Add(-time.Hour)},
		{GID: "t5", Name: "年末調整", Completed: true, Assignee: ref("me"), ModifiedAt: testNow.Add(-26 * time.Hour)},
		{GID: "t3", Name: "電気の契約", Assignee: ref("u2"), ModifiedAt: testNow.AddDate(0, 0, -10)},
	})
	m := New(ctx, Deps{Store: st, Now: func() time.Time { return testNow }})
	h := &harness{t: t, m: m}
	h.send(tea.WindowSizeMsg{Width: 140, Height: 30})
	h.run(m.Init())
	h.key("h", "j", "l") // 最近の更新
	s := h.screen()
	for _, want := range []string{"最近の更新", "▾ 今日", "転居届を出す", "▾ 昨日", "年末調整"} {
		if !strings.Contains(s, want) {
			t.Fatalf("recent updates missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "電気の契約") {
		t.Fatal("tasks modified more than 7 days ago should not be listed")
	}
	if got := h.visibleGIDs(); len(got) != 2 || got[0] != "t2" {
		t.Fatalf("newest first: %v", got)
	}
}

func TestKeyBindingsAndTheme(t *testing.T) {
	st := fixtureStore(t)
	m := New(context.Background(), Deps{Store: st, Now: func() time.Time { return testNow },
		KeyBindings: map[string][]string{"complete": {"d"}}, Theme: "light", Accent: "#123456"})
	h := &harness{t: t, m: m}
	h.send(tea.WindowSizeMsg{Width: 140, Height: 30})
	h.run(m.Init())
	h.key("x")
	if d, _ := st.GetTask(context.Background(), "t2"); d.Completed {
		t.Fatal("x should no longer complete after rebinding")
	}
	h.key("d")
	if d, _ := st.GetTask(context.Background(), "t2"); !d.Completed {
		t.Fatal("d should complete after rebinding complete = d")
	}
	h.key("?")
	if !strings.Contains(h.screen(), "d / space x") {
		t.Fatalf("help should show the rebound key:\n%s", h.screen())
	}
	h.send(tea.BackgroundColorMsg{})
	if m.dark {
		t.Fatal("theme = light must not follow the terminal background")
	}
	if fmt.Sprint(m.st.accent) == fmt.Sprint(newStyles(false, "").accent) {
		t.Fatal("accent override not applied")
	}
	if !slices.Contains(ActionIDs(), "complete") || !slices.Contains(ActionIDs(), "due") {
		t.Fatalf("ActionIDs = %v", ActionIDs())
	}
}
