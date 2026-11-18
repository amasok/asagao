package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	gosync "sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/store"
	"github.com/amasok/asagao/internal/sync"
)

// templateFake answers the template requests in the shapes of Asana's reference; a job has
// already succeeded when the POST answers. empty: no templates; status402: the plan lacks
// them.
type templateFake struct {
	mu        gosync.Mutex
	posts     []string
	gets      map[string]int
	empty     bool
	status402 bool
	status500 bool
}

func (f *templateFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, _ := io.ReadAll(r.Body)
	var body struct {
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(raw, &body)
	name, _ := body.Data["name"].(string)
	key := r.Method + " " + r.URL.Path
	if r.Method == http.MethodPost {
		f.posts = append(f.posts, key+" "+string(raw))
	} else {
		f.gets[key]++
	}
	reply := func(v any) { _ = json.NewEncoder(w).Encode(map[string]any{"data": v}) }
	if f.status500 && strings.HasSuffix(r.URL.Path, "_templates") {
		http.Error(w, `{"errors":[{"message":"down"}]}`, http.StatusInternalServerError)
		return
	}
	if f.status402 && strings.HasSuffix(r.URL.Path, "_templates") {
		http.Error(w, `{"errors":[{"message":"upgrade"}]}`, http.StatusPaymentRequired)
		return
	}
	switch key {
	case "GET /task_templates":
		if f.empty {
			reply([]any{})
			return
		}
		reply([]any{map[string]any{"gid": "tt1", "name": "週次レビュー", "project": map[string]string{"gid": "p1"}}})
	case "GET /project_templates":
		if f.empty {
			reply([]any{})
			return
		}
		reply([]any{map[string]any{"gid": "pt1", "name": "新居テンプレ", "requested_dates": []any{map[string]string{"gid": "d1", "name": "開始日"}}}})
	case "POST /task_templates/tt1/instantiateTask":
		reply(map[string]any{"gid": "j1", "status": "succeeded", "new_task": map[string]any{"gid": "t9", "name": name}})
	case "POST /project_templates/pt1/instantiateProject", "POST /projects/p1/duplicate":
		reply(map[string]any{"gid": "j1", "status": "succeeded", "new_project": map[string]any{"gid": "p9", "name": name}})
	case "GET /tasks/t9":
		reply(map[string]any{"gid": "t9", "name": "週次レビュー", "workspace": map[string]string{"gid": "w1"},
			"memberships": []any{map[string]any{"project": map[string]string{"gid": "p1"}, "section": map[string]string{"gid": "s1"}}}})
	case "GET /tasks/t9/subtasks", "GET /tasks/t9/stories", "GET /attachments", "GET /projects/p9/sections", "GET /tasks":
		reply([]any{})
	case "GET /projects/p9":
		reply(map[string]any{"gid": "p9", "name": "新しい家", "workspace": map[string]string{"gid": "w1"}, "members": []any{map[string]string{"gid": "me"}}})
	default:
		http.Error(w, fmt.Sprintf(`{"errors":[{"message":"not in the fake: %s"}]}`, key), http.StatusNotFound)
	}
}

func (f *templateFake) lastPost() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.posts) == 0 {
		return ""
	}
	return f.posts[len(f.posts)-1]
}

// templateHarness opens project p1 with a syncer on a templateFake. now can be moved.
func templateHarness(t *testing.T, online bool) (*harness, *templateFake, *store.Store, *time.Time) {
	t.Helper()
	st := fixtureStore(t)
	f := &templateFake{gets: map[string]int{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	// The syncer stamps a 402 with the real clock, so the TUI's clock starts there too.
	now := time.Now()
	if err := st.UpsertWorkspaces(context.Background(), []asana.Workspace{{GID: "w1", Name: "家"}}); err != nil {
		t.Fatal(err)
	}
	m := New(context.Background(), Deps{Store: st, Now: func() time.Time { return now }})
	h := &harness{t: t, m: m}
	h.send(tea.WindowSizeMsg{Width: 160, Height: 50})
	h.run(m.Init())
	if online {
		api := asana.NewClient(srv.Client())
		api.BaseURL, api.MaxRetries = srv.URL, 0
		m.syncer = &sync.Syncer{API: api, Store: st}
	}
	h.key("h", "j", "j", "l") // project p1
	if m.currentProject() != "p1" {
		t.Fatalf("not in p1: %q", m.currentProject())
	}
	return h, f, st, &now
}

func TestTemplatesFromTheMenu(t *testing.T) {
	h, f, st, _ := templateHarness(t, true)
	h.key("space")
	s := h.screen()
	for _, want := range []string{"テンプレートからタスクを作る", "テンプレートからプロジェクトを作る", "このプロジェクトを複製"} {
		if !strings.Contains(s, want) {
			t.Fatalf("menu lacks %s:\n%s", want, s)
		}
	}
	h.key("G")
	if p := h.m.picker; p == nil || p.kind != pickTaskTemplate || len(p.items) != 1 || p.items[0].label != "週次レビュー" {
		t.Fatalf("picker = %+v", h.m.picker)
	}
	h.key("enter")
	if !strings.Contains(f.lastPost(), "POST /task_templates/tt1/instantiateTask") || !strings.Contains(h.m.status, "タスクを作りました: 週次レビュー") {
		t.Fatalf("post %s, status %q", f.lastPost(), h.m.status)
	}
	if d, err := st.GetTask(context.Background(), "t9"); err != nil || d.Name != "週次レビュー" {
		t.Errorf("task not stored: %+v %v", d, err)
	}

	h.key("space", "B")
	if p := h.m.picker; p == nil || p.kind != pickProjectTemplate || !strings.Contains(p.items[0].hint, "今日") {
		t.Fatalf("picker = %+v", h.m.picker)
	}
	h.key("enter")
	if h.m.promptMode != promptTemplateProject || h.m.prompt.Value() != "新居テンプレ" {
		t.Fatalf("prompt %v %q", h.m.promptMode, h.m.prompt.Value())
	}
	h.typeText(" 2")
	h.key("enter")
	if last := f.lastPost(); !strings.Contains(last, "instantiateProject") || !strings.Contains(last, `"name":"新居テンプレ 2"`) || !strings.Contains(last, `"gid":"d1","value":"2026-`) {
		t.Fatalf("post %s", last)
	}
	if !strings.Contains(h.m.status, "プロジェクトを作りました") {
		t.Errorf("status %q", h.m.status)
	}

	h.key("h", "j", "j", "l")
	for h.m.currentProject() != "p1" { // the sidebar now has the new project too
		h.key("h", "k", "l")
	}
	h.key("space", "S")
	if h.m.promptMode != promptDuplicateProject || h.m.prompt.Value() != "新居プロジェクト のコピー" {
		t.Fatalf("prompt %v %q", h.m.promptMode, h.m.prompt.Value())
	}
	h.key("enter")
	if last := f.lastPost(); !strings.HasPrefix(last, "POST /projects/p1/duplicate ") || !strings.Contains(last, `"name":"新居プロジェクト のコピー"`) {
		t.Fatalf("post %s", last)
	}
}

// No templates, or a 402: "from a template" is not offered (duplicating still is).
func TestTemplatesHiddenWithoutTemplates(t *testing.T) {
	for _, tc := range []string{"empty", "402"} {
		h, f, st, now := templateHarness(t, true)
		f.empty, f.status402 = tc == "empty", tc == "402"
		h.key("space")
		s := h.screen()
		if strings.Contains(s, "テンプレートから") || !strings.Contains(s, "このプロジェクトを複製") {
			t.Fatalf("%s: menu:\n%s", tc, s)
		}
		h.key("G") // the key does nothing either
		if h.m.picker != nil || len(f.posts) != 0 {
			t.Fatalf("%s: G opened %+v / sent %v", tc, h.m.picker, f.posts)
		}
		gets := f.gets["GET /task_templates"]
		h.key("space", "esc") // known: not fetched again within templateTTL
		if f.gets["GET /task_templates"] != gets {
			t.Errorf("%s: fetched again at once", tc)
		}
		if tc == "402" {
			if v, _ := st.SyncState(context.Background(), readmodel.PlanKey(readmodel.PlanTaskTemplates, "w1")); v == "" {
				t.Error("402 did not mark the plan")
			}
			// The mark survives loading the plan again, and is dropped a day later.
			h.run(h.m.loadPlan())
			if h.m.planOK(readmodel.PlanTaskTemplates) {
				t.Error("plan reloaded as available")
			}
			*now = now.Add(25 * time.Hour)
			h.run(h.m.loadPlan())
			if !h.m.planOK(readmodel.PlanTaskTemplates) {
				t.Error("still hidden a day later")
			}
			continue
		}
		// After templateTTL they are looked for again, and offered once there are some.
		f.empty = false
		*now = now.Add(templateTTL)
		h.key("space")
		if !strings.Contains(h.screen(), "テンプレートからタスクを作る") {
			t.Errorf("not offered after templates appeared:\n%s", h.screen())
		}
	}
}

func TestTemplatesOffline(t *testing.T) {
	h, f, _, _ := templateHarness(t, false)
	h.key("space")
	if s := h.screen(); strings.Contains(s, "テンプレートから") {
		t.Fatalf("offered offline:\n%s", s)
	}
	h.key("S")
	if !strings.Contains(h.screen(), "オフラインのためできません") || h.m.promptMode != promptNone {
		t.Fatalf("duplicate offline: %q", h.m.status)
	}
	if len(f.posts)+len(f.gets) != 0 {
		t.Errorf("reached Asana offline: %v %v", f.posts, f.gets)
	}
}

// A failed fetch is not tried again at every menu, only after templateTTL.
func TestTemplatesFailedFetchWaits(t *testing.T) {
	h, f, _, now := templateHarness(t, true)
	f.status500 = true
	h.key("space", "esc")
	tasks, projects := f.gets["GET /task_templates"], f.gets["GET /project_templates"]
	if tasks != 1 || projects != 1 {
		t.Fatalf("fetched %d / %d", tasks, projects)
	}
	h.key("space", "esc", "space", "esc")
	if f.gets["GET /task_templates"] != tasks || f.gets["GET /project_templates"] != projects {
		t.Errorf("fetched again at once: %v", f.gets)
	}
	f.status500 = false
	*now = now.Add(templateTTL)
	h.key("space")
	if !strings.Contains(h.screen(), "テンプレートからタスクを作る") {
		t.Errorf("not offered after the fetch worked:\n%s", h.screen())
	}
}
