package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/store"
	"github.com/amasok/asagao/internal/sync"
)

// overviewStore is the fixture with p1's fields and progress.
func overviewStore(t *testing.T) *store.Store {
	t.Helper()
	st := fixtureStore(t)
	ctx := context.Background()
	if err := st.UpsertProjects(ctx, "w1", []asana.Project{{GID: "p1", Name: "新居プロジェクト", Members: []asana.Ref{{GID: "me"}},
		Owner: &asana.Ref{GID: "me", Name: "Me"}, DueOn: "2026-12-31", Color: "dark-green",
		Notes: "年内に引っ越す", HTMLNotes: "<body><strong>年内</strong>に引っ越す</body>"}}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveProjectOverview(ctx, "p1", &asana.TaskCounts{NumTasks: 4, NumCompletedTasks: 1}, store.BriefUnavailable, nil, testNow); err != nil {
		t.Fatal(err)
	}
	return st
}

func overviewHarness(t *testing.T, st *store.Store, edit func(string) string) *harness {
	t.Helper()
	m := New(context.Background(), Deps{Store: st, Now: func() time.Time { return testNow }, RunEditor: fakeEditor(t, edit)})
	h := &harness{t: t, m: m}
	h.send(tea.WindowSizeMsg{Width: 140, Height: 30})
	h.run(m.Init())
	h.key("h", "j", "j", "l") // open 新居プロジェクト
	return h
}

// #39: O shows the overview; e edits the fields in the editor and queues them.
func TestProjectOverviewAndEdit(t *testing.T) {
	st := overviewStore(t)
	ctx := context.Background()
	var seen string
	h := overviewHarness(t, st, func(s string) string {
		seen = s
		s = strings.Replace(s, "\nstart_on: \n", "\nstart_on: 2026-12-01\n", 1)
		s = strings.Replace(s, "\ncolor: dark-green\n", "\ncolor: light-blue\n", 1)
		s = strings.Replace(s, "\nowner: me\n", "\nowner: Partner\n", 1)
		return s + "\n- 荷造り\n"
	})
	h.key(" ", "O")
	screen := h.screen()
	for _, want := range []string{"プロジェクトの概要", "オーナー: Me", "期間: 〜 2026-12-31", "色: dark-green", "1 / 4 件完了（25%）", "年内に引っ越す"} {
		if !strings.Contains(screen, want) {
			t.Fatalf("overview lacks %q:\n%s", want, screen)
		}
	}
	if strings.Contains(screen, "ブリーフ") {
		t.Fatalf("an unavailable brief is shown:\n%s", screen)
	}

	h.key("e")
	for _, want := range []string{"owner: me\n", "due_on: 2026-12-31\n", "color: dark-green\n", "**年内**に引っ越す"} {
		if !strings.Contains(seen, want) {
			t.Fatalf("editor file lacks %q:\n%s", want, seen)
		}
	}
	o, _ := st.ProjectOverview(ctx, "p1")
	if o.StartOn != "2026-12-01" || o.Color != "light-blue" || o.Owner == nil || o.Owner.GID != "u2" ||
		!strings.Contains(o.HTMLNotes, "<li>荷造り</li>") || !o.Pending {
		t.Fatalf("after edit: %+v", o)
	}
	ops, _ := st.ListOps(ctx)
	if len(ops) != 1 || ops[0].Kind != store.OpUpdateProject {
		t.Fatalf("ops = %+v", ops)
	}
	if _, due := ops[0].Payload["due_on"]; due {
		t.Fatalf("an unchanged field was queued: %v", ops[0].Payload)
	}
	if screen := h.screen(); !strings.Contains(screen, "色: light-blue") || !strings.Contains(screen, "未送信") {
		t.Fatalf("the overview was not reloaded:\n%s", screen)
	}
	h.key("esc")
	if h.m.overviewView {
		t.Fatal("esc did not close the overview")
	}
}

// Closing the editor without changes queues nothing (the description is not re-sent
// because of how its HTML round-trips).
func TestProjectEditUnchanged(t *testing.T) {
	st := overviewStore(t)
	h := overviewHarness(t, st, func(s string) string { return s })
	h.key(" ", "O", "e")
	if ops, _ := st.ListOps(context.Background()); len(ops) != 0 {
		t.Fatalf("queued without a change: %+v", ops)
	}
	if !strings.Contains(h.m.status, "変更はありません") {
		t.Fatalf("status = %q", h.m.status)
	}
}

// An invalid value is refused, the file kept, nothing queued.
func TestProjectEditInvalid(t *testing.T) {
	for name, edit := range map[string]func(string) string{
		"color": func(s string) string { return strings.Replace(s, "\ncolor: dark-green", "\ncolor: rainbow", 1) },
		"start": func(s string) string { return strings.Replace(s, "\nstart_on: ", "\nstart_on: 2027-02-01", 1) },
		"owner": func(s string) string { return strings.Replace(s, "\nowner: me\n", "\nowner: 誰か\n", 1) },
		"key":   func(s string) string { return strings.Replace(s, "\ncolor:", "\ncolour:", 1) },
	} {
		t.Run(name, func(t *testing.T) {
			st := overviewStore(t)
			h := overviewHarness(t, st, edit)
			h.key(" ", "O", "e")
			if ops, _ := st.ListOps(context.Background()); len(ops) != 0 {
				t.Fatalf("queued: %+v", ops)
			}
			if !strings.Contains(h.m.status, "asagao-project-") {
				t.Fatalf("status should keep the file: %q", h.m.status)
			}
		})
	}
}

// #38: the sidebar lists my favorites, then each team, under headings; the cursor skips
// the headings.
func TestSidebarGroupsByFavoriteAndTeam(t *testing.T) {
	st := fixtureStore(t)
	ctx := context.Background()
	me := []asana.Ref{{GID: "me"}}
	if err := st.UpsertProjects(ctx, "w1", []asana.Project{
		{GID: "p1", Name: "新居プロジェクト", Members: me, Team: &asana.Ref{GID: "tm1", Name: "家族"}},
		{GID: "p2", Name: "仕事", Members: me, Team: &asana.Ref{GID: "tm2", Name: "会社"}},
		{GID: "p3", Name: "趣味", Members: me},
	}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceFavoriteProjects(ctx, "w1", []asana.Ref{{GID: "p3"}}); err != nil {
		t.Fatal(err)
	}
	m := New(ctx, Deps{Store: st, Now: func() time.Time { return testNow }})
	h := &harness{t: t, m: m}
	h.send(tea.WindowSizeMsg{Width: 140, Height: 30})
	h.run(m.Init())
	screen := h.screen()
	order := []string{"★ お気に入り", "趣味", "会社", "仕事", "家族", "新居プロジェクト"}
	idx := 0
	for _, w := range order {
		i := strings.Index(screen[idx:], w)
		if i < 0 {
			t.Fatalf("%q missing or out of order:\n%s", w, screen)
		}
		idx += i + len(w)
	}
	h.key("h", "j", "j")
	if sc := h.m.currentScope(); sc.project != "p3" {
		t.Fatalf("cursor on %+v, want the favorite p3", sc)
	}
	h.key("j")
	if sc := h.m.currentScope(); sc.project != "p2" {
		t.Fatalf("cursor on %+v, want p2 past the heading", sc)
	}
}

// One team: no headings (the list looks as it did).
func TestSidebarOneGroupHasNoHeading(t *testing.T) {
	h := newHarness(t, 140, 30)
	if strings.Contains(h.screen(), "チームなし") {
		t.Fatalf("a heading for the only group:\n%s", h.screen())
	}
}

// A new project: with several teams, a picker asks which; the choice goes to Asana.
func TestNewProjectAsksForTheTeam(t *testing.T) {
	st := fixtureStore(t)
	ctx := context.Background()
	if err := st.UpsertWorkspaces(ctx, []asana.Workspace{{GID: "w1", Name: "会社", IsOrganization: true}}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceTeams(ctx, "w1", []asana.Team{{GID: "tm1", Name: "営業"}, {GID: "tm2", Name: "開発"}}); err != nil {
		t.Fatal(err)
	}
	var created map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "POST /projects":
			var body struct{ Data map[string]any }
			_ = json.NewDecoder(r.Body).Decode(&body)
			created = body.Data
			fmt.Fprint(w, `{"data":{"gid":"p9","name":"新規","workspace":{"gid":"w1"},"members":[{"gid":"me"}]}}`)
		case "GET /projects/p9/sections", "GET /project_templates", "GET /task_templates": // the menu looks for templates (templates.go)
			fmt.Fprint(w, `{"data":[]}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
		}
	}))
	defer srv.Close()
	api := asana.NewClient(srv.Client())
	api.BaseURL, api.MaxRetries = srv.URL, 0
	m := New(ctx, Deps{Store: st, Now: func() time.Time { return testNow }})
	h := &harness{t: t, m: m}
	h.send(tea.WindowSizeMsg{Width: 140, Height: 30})
	h.run(m.Init())
	m.syncer = &sync.Syncer{API: api, Store: st}
	h.key(" ", "N")
	h.typeText("新規")
	h.key("enter")
	if m.picker == nil || m.picker.kind != pickTeam || len(m.picker.items) != 2 {
		t.Fatalf("picker = %+v", m.picker)
	}
	h.key("j", "enter")
	if created["team"] != "tm2" || created["name"] != "新規" {
		t.Fatalf("created with %v", created)
	}
}
