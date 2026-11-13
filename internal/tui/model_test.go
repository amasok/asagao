package tui

import (
	"context"
	"path/filepath"
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
	case <-time.After(50 * time.Millisecond): // a timer; ignore
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
