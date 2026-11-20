package tui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/store"
	"github.com/amasok/asagao/internal/sync"
)

// Searching Asana from the TUI, and quick add with a name the cache lacks (Issue #36).

type tuiFindAsana struct {
	searchStatus int
	typeaheads   []string
}

func (f *tuiFindAsana) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	switch r.Method + " " + r.URL.Path {
	case "GET /workspaces/w1/tasks/search":
		if f.searchStatus != 0 {
			w.WriteHeader(f.searchStatus)
			fmt.Fprint(w, `{"errors":[{"message":"premium only"}]}`)
			return
		}
		fmt.Fprint(w, `{"data":[{"gid":"t9"}]}`)
	case "GET /workspaces/w1/typeahead":
		f.typeaheads = append(f.typeaheads, q.Get("resource_type")+":"+q.Get("query"))
		switch q.Get("resource_type") {
		case "task":
			fmt.Fprint(w, `{"data":[{"gid":"t9","name":"古い見積もり"}]}`)
		case "user":
			fmt.Fprint(w, `{"data":[{"gid":"u7","name":"Hanako Yamada","email":"hanako@example.com"}]}`)
		default:
			fmt.Fprint(w, `{"data":[]}`)
		}
	case "GET /tasks/t9":
		fmt.Fprint(w, `{"data":{"gid":"t9","name":"古い見積もり","notes":"本文に請求の語","workspace":{"gid":"w1"},
			"memberships":[{"project":{"gid":"p9","name":"よそのプロジェクト"}}]}}`)
	default:
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"errors":[{"message":"not in the fake"}]}`)
	}
}

// findHarness is newHarness with a syncer that talks to f.
func findHarness(t *testing.T, f *tuiFindAsana) (*harness, *store.Store) {
	t.Helper()
	st := fixtureStore(t)
	if err := st.UpsertWorkspaces(context.Background(), []asana.Workspace{{GID: "w1", Name: "WS"}}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	api := asana.NewClient(srv.Client())
	api.BaseURL, api.MaxRetries = srv.URL, 0
	m := New(context.Background(), Deps{Store: st, Now: func() time.Time { return testNow }})
	m.syncer = &sync.Syncer{API: api, Store: st}
	h := &harness{t: t, m: m}
	h.send(tea.WindowSizeMsg{Width: 160, Height: 30})
	h.run(m.Init())
	return h, st
}

func (h *harness) menu(key string) {
	h.send(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	h.key(key)
}

func TestSearchAsanaFromTheTUI(t *testing.T) {
	f := &tuiFindAsana{}
	h, _ := findHarness(t, f)
	h.key("/")
	h.typeText("見積")
	h.key("enter")
	if s := h.screen(); strings.Contains(s, "古い見積もり") {
		t.Fatalf("not in the cache yet:\n%s", s)
	}
	h.menu("g")
	// (the status line then shows the selected task's detail fetch, which the fake refuses)
	s := h.screen()
	if !strings.Contains(s, "古い見積もり") || !strings.Contains(s, "Asana も") || h.m.search.remote.query != "見積" || h.m.search.remote.byName {
		t.Fatalf("found task should be listed (remote %+v):\n%s", h.m.search.remote, s)
	}
	// another search: what Asana found for the old one is not mixed in
	h.key("/")
	h.typeText("ソファ")
	if s := h.screen(); strings.Contains(s, "古い見積もり") || strings.Contains(s, "Asana も") {
		t.Fatalf("old remote results shown for a new search:\n%s", s)
	}
}

func TestSearchAsanaByNameWithoutTheSearchAPI(t *testing.T) {
	f := &tuiFindAsana{searchStatus: http.StatusPaymentRequired}
	h, _ := findHarness(t, f)
	h.key("/")
	h.typeText("古い")
	h.key("enter")
	h.menu("g")
	if s := h.screen(); !strings.Contains(s, "古い見積もり") || !h.m.search.remote.byName {
		t.Fatalf("typeahead result (remote %+v):\n%s", h.m.search.remote, s)
	}
}

func TestSearchAsanaStatus(t *testing.T) {
	h := newHarness(t, 160, 30)
	h.m.search.query = "x"
	for _, tc := range []struct {
		msg  remoteSearchMsg
		want string
	}{
		{remoteSearchMsg{query: "x", res: readmodel.RemoteSearchResult{GIDs: []string{"a", "b"}}}, "Asana で 2 件見つかりました"},
		{remoteSearchMsg{query: "x", res: readmodel.RemoteSearchResult{ByName: true}}, "Asana で 0 件見つかりました（プランに検索 API がないため、タスク名の先頭で探しました）"},
		{remoteSearchMsg{query: "x", err: errors.New("boom\x1b[2J")}, "Asana では探せませんでした: boom[2J"},
	} {
		h.run(h.m.remoteSearchDone(tc.msg))
		if h.m.status != tc.want {
			t.Errorf("status %q, want %q", h.m.status, tc.want)
		}
	}
}

func TestSearchAsanaNeedsAQueryAndAConnection(t *testing.T) {
	h := newHarness(t, 160, 30) // no syncer, no NewSyncer
	h.menu("g")
	if s := h.screen(); !strings.Contains(s, "先に / で検索する語を入れてください") {
		t.Fatalf("no query:\n%s", s)
	}
	h.key("/")
	h.typeText("契約")
	h.key("enter")
	h.menu("g")
	if s := h.screen(); !strings.Contains(s, "オフラインのため Asana では探せません") || !strings.Contains(s, "ガスの契約") {
		t.Fatalf("offline:\n%s", s)
	}
}

func TestQuickAddLooksUpAnAssigneeTheCacheLacks(t *testing.T) {
	f := &tuiFindAsana{}
	h, st := findHarness(t, f)
	h.key("a")
	h.typeText("見積もりを送る @hanako")
	h.key("enter")
	if len(f.typeaheads) != 1 || f.typeaheads[0] != "user:hanako" {
		t.Fatalf("typeahead %v", f.typeaheads)
	}
	tasks, err := st.ListTasks(context.Background(), store.TaskFilter{Query: "見積もりを送る"})
	if err != nil || len(tasks) != 1 || tasks[0].Assignee == nil || tasks[0].Assignee.GID != "u7" {
		t.Fatalf("created %+v %v\n%s", tasks, err, h.screen())
	}
}

// An answer for an earlier search that arrives late changes neither what Asana found for
// the current search nor the status line.
func TestSearchAsanaLateAnswerIsDropped(t *testing.T) {
	h := newHarness(t, 160, 30)
	h.m.search.query = "B"
	h.run(h.m.remoteSearchDone(remoteSearchMsg{query: "B", res: readmodel.RemoteSearchResult{GIDs: []string{"t1"}}}))
	for _, late := range []remoteSearchMsg{
		{query: "A", res: readmodel.RemoteSearchResult{GIDs: []string{"t2", "t3"}}},
		{query: "A", err: errors.New("offline")},
	} {
		h.run(h.m.remoteSearchDone(late))
		if h.m.search.remote.query != "B" || len(h.m.search.remote.gids) != 1 || h.m.status != "Asana で 1 件見つかりました" {
			t.Fatalf("late answer %+v: remote %+v status %q", late, h.m.search.remote, h.m.status)
		}
	}
}
