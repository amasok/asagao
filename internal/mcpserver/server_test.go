package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/amasok/asana-tui/internal/asana"
	"github.com/amasok/asana-tui/internal/config"
	"github.com/amasok/asana-tui/internal/store"
	"github.com/amasok/asana-tui/internal/sync"
)

var testNow = time.Date(2026, 11, 13, 10, 0, 0, 0, time.Local)

func seed(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ref := func(gid string) *asana.Ref { return &asana.Ref{GID: gid, Name: map[string]string{"me": "Me"}[gid]} }
	in := func(sec string) []asana.Membership {
		return []asana.Membership{{Project: ref("p1"), Section: ref(sec)}}
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.UpsertUser(ctx, asana.User{GID: "me", Name: "Me"}))
	must(st.SetSyncState(ctx, store.KeyMeGID, "me"))
	must(st.UpsertWorkspaces(ctx, []asana.Workspace{{GID: "w1", Name: "WS"}}))
	must(st.UpsertProjects(ctx, "w1", []asana.Project{{GID: "p1", Name: "[家族]新居プロジェクト", Members: []asana.Ref{{GID: "me"}}}}, "me"))
	must(st.ReplaceSections(ctx, "p1", []asana.Section{{GID: "s1", Name: "家具"}, {GID: "s2", Name: "手続き"}}))
	must(st.UpsertTasks(ctx, []asana.Task{
		{GID: "t1", Name: "ソファを買う", DueOn: "2026-11-20", Assignee: ref("me"), Workspace: ref("w1"), Memberships: in("s1")},
		{GID: "t2", Name: "転居届を出す", DueOn: "2026-11-10", Assignee: ref("me"), Workspace: ref("w1"), Memberships: in("s2")},
		{GID: "t3", Name: "電気の契約", Assignee: ref("u2"), Workspace: ref("w1"), Memberships: in("s2")},
	}))
	return st
}

func connect(t *testing.T, s *Server) *mcp.ClientSession {
	t.Helper()
	s.Now = func() time.Time { return testNow }
	ctx := context.Background()
	ct, st := mcp.NewInMemoryTransports()
	go func() { _ = s.New().Run(ctx, st) }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

// call invokes a tool and decodes its structured output into out.
func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any, out any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if out != nil && !res.IsError {
		b, _ := json.Marshal(res.StructuredContent)
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("%s: decode %s: %v", name, b, err)
		}
	}
	return res
}

func toolError(res *mcp.CallToolResult) string {
	if !res.IsError || len(res.Content) == 0 {
		return ""
	}
	if tc, ok := res.Content[0].(*mcp.TextContent); ok {
		return tc.Text
	}
	return "error"
}

func names(ts []store.TaskSummary) []string {
	var out []string
	for _, t := range ts {
		out = append(out, t.Name)
	}
	return out
}

func TestToolsListedAndReadOnly(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		readOnly bool
		want     []string
	}{
		{false, []string{"add_comment", "create_task", "get_task", "list_projects", "list_tags", "list_tasks", "move_task", "search_tasks", "sync", "sync_status", "update_task"}},
		{true, []string{"get_task", "list_projects", "list_tags", "list_tasks", "search_tasks", "sync", "sync_status"}},
	} {
		cs := connect(t, &Server{Store: seed(t), ReadOnly: tc.readOnly})
		res, err := cs.ListTools(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, tool := range res.Tools {
			got = append(got, tool.Name)
			if tool.Description == "" || tool.InputSchema == nil {
				t.Errorf("%s: missing description or schema", tool.Name)
			}
		}
		slices.Sort(got)
		if !slices.Equal(got, tc.want) {
			t.Errorf("readOnly=%v: tools %v, want %v", tc.readOnly, got, tc.want)
		}
	}
}

func TestReadTools(t *testing.T) {
	cs := connect(t, &Server{Store: seed(t)})

	var tasks TasksOutput
	call(t, cs, "list_tasks", nil, &tasks)
	if got := names(tasks.Tasks); !slices.Equal(got, []string{"転居届を出す", "ソファを買う"}) {
		t.Errorf("my tasks = %v", got)
	}
	call(t, cs, "list_tasks", map[string]any{"project": "新居", "section": "手続き"}, &tasks)
	if got := names(tasks.Tasks); !slices.Equal(got, []string{"転居届を出す", "電気の契約"}) {
		t.Errorf("project/section = %v", got)
	}
	call(t, cs, "list_tasks", map[string]any{"overdue": true}, &tasks)
	if got := names(tasks.Tasks); !slices.Equal(got, []string{"転居届を出す"}) {
		t.Errorf("overdue = %v", got)
	}
	call(t, cs, "list_tasks", map[string]any{"due_before": "+6d"}, &tasks) // 11/19: excludes 11/20
	if got := names(tasks.Tasks); !slices.Equal(got, []string{"転居届を出す"}) {
		t.Errorf("due_before +6d = %v", got)
	}

	call(t, cs, "search_tasks", map[string]any{"query": "電気"}, &tasks)
	if tasks.Count != 1 || tasks.Tasks[0].GID != "t3" {
		t.Errorf("search = %+v", tasks)
	}

	var task GetTaskOutput
	call(t, cs, "get_task", map[string]any{"gid": "t1"}, &task)
	if task.Task == nil || task.Task.Name != "ソファを買う" || task.Task.Memberships[0].SectionName != "家具" {
		t.Errorf("get_task = %+v", task)
	}
	if !strings.Contains(task.Note, "local cache") {
		t.Errorf("offline get_task should say it used the cache: %q", task.Note)
	}

	var projects ProjectsOutput
	call(t, cs, "list_projects", nil, &projects)
	if len(projects.Projects) != 1 || len(projects.Projects[0].Sections) != 2 {
		t.Errorf("projects = %+v", projects)
	}

	// Errors come back as tool errors the model can read.
	if msg := toolError(call(t, cs, "list_tasks", map[string]any{"project": "存在しない"}, nil)); !strings.Contains(msg, "not found") {
		t.Errorf("unknown project error = %q", msg)
	}
	if msg := toolError(call(t, cs, "list_tasks", map[string]any{"due_before": "来週"}, nil)); !strings.Contains(msg, "due_before") {
		t.Errorf("bad date error = %q", msg)
	}
}

func TestWritesOfflineAreQueued(t *testing.T) {
	st := seed(t)
	cs := connect(t, &Server{Store: st})

	var w WriteOutput
	call(t, cs, "update_task", map[string]any{"gid": "t2", "completed": true}, &w)
	if w.Sent || !strings.Contains(w.Note, "next sync") {
		t.Errorf("offline update = %+v", w)
	}
	call(t, cs, "create_task", map[string]any{"name": "牛乳を買う", "due_on": "tomorrow"}, &w)
	if !store.IsLocalGID(w.TaskGID) {
		t.Errorf("offline create should return a local gid: %+v", w)
	}
	call(t, cs, "add_comment", map[string]any{"gid": "t1", "text": "ニトリで見る"}, &w)

	var status SyncStatusOutput
	call(t, cs, "sync_status", nil, &status)
	if status.Pending != 3 || len(status.Outbox) != 3 {
		t.Errorf("status = %+v", status)
	}
	var tasks TasksOutput
	call(t, cs, "list_tasks", nil, &tasks)
	if got := names(tasks.Tasks); !slices.Equal(got, []string{"牛乳を買う", "ソファを買う"}) {
		t.Errorf("local changes should be visible at once: %v", got)
	}
	if !tasks.Tasks[0].Pending || tasks.Tasks[0].DueOn != "2026-11-14" || tasks.Tasks[0].Assignee == nil {
		t.Errorf("created task = %+v", tasks.Tasks[0])
	}
	if msg := toolError(call(t, cs, "update_task", map[string]any{"gid": "t1"}, nil)); !strings.Contains(msg, "nothing to update") {
		t.Errorf("empty update error = %q", msg)
	}
}

// fakeAsana accepts writes for any task.
func fakeAsana(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body struct {
			Data map[string]any `json:"data"`
		}
		_ = json.Unmarshal(b, &body)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/tasks":
			fmt.Fprintf(w, `{"data":{"gid":"999","name":%q,"assignee":{"gid":"me"}}}`, body.Data["name"])
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/tasks/"):
			fmt.Fprintf(w, `{"data":{"gid":%q,"name":"転居届を出す","due_on":"2026-11-10"}}`, strings.TrimPrefix(r.URL.Path, "/tasks/"))
		case r.Method == http.MethodPut:
			fmt.Fprint(w, `{"data":{"gid":"t2","name":"転居届を出す","completed":true}}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestWritesOnlineAreSent(t *testing.T) {
	st := seed(t)
	srv := fakeAsana(t)
	cs := connect(t, &Server{Store: st, NewSyncer: func(context.Context) (*sync.Syncer, error) {
		api := asana.NewClient(srv.Client())
		api.BaseURL = srv.URL
		return &sync.Syncer{API: api, Store: st, Cfg: config.SyncConfig{CompletedDays: 14}}, nil
	}})

	var w WriteOutput
	call(t, cs, "update_task", map[string]any{"gid": "t2", "completed": true}, &w)
	if !w.Sent {
		t.Errorf("update should be sent: %+v", w)
	}
	call(t, cs, "create_task", map[string]any{"name": "牛乳を買う"}, &w)
	if !w.Sent || w.TaskGID != "999" {
		t.Errorf("create should return the real gid: %+v", w)
	}
	var status SyncStatusOutput
	call(t, cs, "sync_status", nil, &status)
	if status.Pending != 0 {
		t.Errorf("outbox should be empty: %+v", status)
	}
}
