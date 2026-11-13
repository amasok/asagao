package tui

import (
	"context"
	"fmt"
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
	h.key("h", "j")
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

	h.key("f") // 7日以内
	if s := h.screen(); strings.Contains(s, "ソファを買う") == false || !strings.Contains(s, "[7日以内が期限]") {
		t.Fatalf("due-within-week filter:\n%s", s)
	}
	h.key("f") // 期限切れ
	if s := h.screen(); strings.Contains(s, "ソファを買う") || !strings.Contains(s, "転居届を出す") {
		t.Fatalf("overdue filter:\n%s", s)
	}
	h.key("f") // 完了済み
	if s := h.screen(); !strings.Contains(s, "年末調整") {
		t.Fatalf("completed filter:\n%s", s)
	}
}

func TestNarrowLayoutShowsOnePane(t *testing.T) {
	h := newHarness(t, 60, 20)
	s := h.screen()
	if strings.Contains(s, "プロジェクト") && strings.Contains(s, "詳細") {
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
	h.key("f", "f", "f") // 完了済み
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
	h.key("h", "j", "l") // project 新居プロジェクト, focus list (first task is in 家具)
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
	h.key("v")
	if h.m.board && h.m.boardActive() {
		t.Fatal("board should not be available in My Tasks")
	}
	h.key("h", "j", "l", "v") // open the project, show the board
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
	h.key("esc", "v")
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
	h.key("t")
	s := h.screen()
	if !strings.Contains(s, "タグで絞り込む") || !strings.Contains(s, "#買い物") || !strings.Contains(s, "#手続き系") {
		t.Fatalf("tag picker:\n%s", s)
	}
	h.key("j", "enter") // #手続き系 sorts before #買い物? order is by name: 手続き系, 買い物
	tag := h.m.tagFilter
	if tag == "" {
		t.Fatal("tag filter should be set")
	}
	h.key("h", "j", "l") // project view shows all assignees
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
	h.key("t", "g", "enter") // "（すべてのタスク）"
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
		h.key("h", "j", "l", "v")
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
	hs.key("h", "j", "l") // open 新居プロジェクト, focus the list
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
	h.key("space") // collapse 家具 (cursor on t1)
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

	h2.key("-")
	if got := h2.visibleGIDs(); len(got) != 0 || !strings.Contains(h2.screen(), "▸ 手続き (3)") {
		t.Fatalf("- should collapse all:\n%s", h2.screen())
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
	for _, want := range []string{"添付ファイル (2)", "i で画像を表示", "[画像] plan.png", "2KB", "✓保存済み", "[ファイル] 見積.pdf"} {
		if !strings.Contains(s, want) {
			t.Fatalf("detail missing %q:\n%s", want, s)
		}
	}
	h.key("i") // offline: uses the cached file, skips the non-image
	if len(viewed) != 1 || viewed[0] != cached {
		t.Fatalf("viewed %v", viewed)
	}

	h.key("j", "i") // t2 has no attachments
	if !strings.Contains(h.screen(), "添付ファイルはまだ取得していません") && !strings.Contains(h.screen(), "画像の添付はありません") {
		t.Fatalf("status for no images:\n%s", h.screen())
	}
}
