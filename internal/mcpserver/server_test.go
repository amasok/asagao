package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/store"
	"github.com/amasok/asagao/internal/sync"
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
		{false, []string{"add_comment", "create_project", "create_section", "create_status_update", "create_task", "delete_attachment", "delete_comment", "delete_section", "delete_tag", "delete_task", "duplicate_task", "get_attachment", "get_context", "get_project", "get_task", "list_inbox", "list_my_sections", "list_projects", "list_saved_searches", "list_tags", "list_tasks", "list_users", "mark_notifications", "move_section", "move_task", "rename_section", "resolve_outbox", "run_saved_search", "search_tasks", "set_parent", "sync", "sync_status", "update_comment", "update_project", "update_tag", "update_task", "upload_attachment"}},
		{true, []string{"get_attachment", "get_context", "get_project", "get_task", "list_inbox", "list_my_sections", "list_projects", "list_saved_searches", "list_tags", "list_tasks", "list_users", "run_saved_search", "search_tasks", "sync", "sync_status"}},
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
		case r.URL.Path == "/users/me": // Push checks the account first
			fmt.Fprint(w, `{"data":{"gid":"me","name":"Me"}}`)
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
		return &sync.Syncer{API: api, Store: st, Cfg: sync.Config{CompletedDays: 14}}, nil
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

func TestCreateSubtaskAndAssignByName(t *testing.T) {
	st := seed(t)
	_ = st.UpsertUsers(context.Background(), []asana.User{{GID: "u2", Name: "Partner"}})
	cs := connect(t, &Server{Store: st})
	var w WriteOutput
	call(t, cs, "create_task", map[string]any{"name": "採寸", "parent": "t1"}, &w)
	var task GetTaskOutput
	call(t, cs, "get_task", map[string]any{"gid": "t1", "offline": true}, &task)
	if len(task.Task.Subtasks) != 1 || task.Task.Subtasks[0].Name != "採寸" {
		t.Fatalf("subtask not created: %+v", task.Task.Subtasks)
	}
	call(t, cs, "update_task", map[string]any{"gid": "t1", "assignee": "Partner"}, &w)
	call(t, cs, "get_task", map[string]any{"gid": "t1", "offline": true}, &task)
	if task.Task.Assignee == nil || task.Task.Assignee.GID != "u2" {
		t.Fatalf("assignee by name: %+v", task.Task.Assignee)
	}
	var users UsersOutput
	call(t, cs, "list_users", nil, &users)
	if len(users.Users) < 2 || users.Users[0].GID != "me" {
		t.Fatalf("users (me first): %+v", users.Users)
	}
}

func TestMarkdownTagsAndComments(t *testing.T) {
	st := seed(t)
	ctx := context.Background()
	_ = st.UpsertTags(ctx, []asana.Tag{{GID: "g1", Name: "買い物"}})
	cs := connect(t, &Server{Store: st})
	var w WriteOutput
	call(t, cs, "update_task", map[string]any{"gid": "t1",
		"notes":    "## 候補\n- **ニトリ**\n- 無印\n\n![画像](asana:42)",
		"add_tags": []string{"買い物", "急ぎ"}}, &w)
	d, _ := st.GetTask(ctx, "t1")
	if !strings.Contains(d.HTMLNotes, "<h2>候補</h2>") || !strings.Contains(d.HTMLNotes, "<strong>ニトリ</strong>") || !strings.Contains(d.HTMLNotes, `<img data-asana-gid="42"/>`) {
		t.Fatalf("html_notes = %q", d.HTMLNotes)
	}
	if !slices.Equal(d.Tags, []string{"急ぎ", "買い物"}) {
		t.Fatalf("tags = %v", d.Tags)
	}
	call(t, cs, "update_task", map[string]any{"gid": "t1", "remove_tags": []string{"#急ぎ"}}, &w)
	call(t, cs, "add_comment", map[string]any{"gid": "t1", "text": "**明日**見に行く"}, &w)

	var task GetTaskOutput
	call(t, cs, "get_task", map[string]any{"gid": "t1", "offline": true}, &task)
	if !strings.Contains(task.DescriptionMarkdown, "## 候補") || !strings.Contains(task.DescriptionMarkdown, "![画像](asana:42)") {
		t.Fatalf("description_markdown = %q", task.DescriptionMarkdown)
	}
	if !slices.Equal(task.Task.Tags, []string{"買い物"}) {
		t.Fatalf("tags after remove = %v", task.Task.Tags)
	}
	if len(task.Comments) != 1 || task.Comments[0].Markdown != "**明日**見に行く" || !task.Comments[0].Pending {
		t.Fatalf("comments = %+v", task.Comments)
	}
	if msg := toolError(call(t, cs, "update_task", map[string]any{"gid": "t1", "remove_tags": []string{"ない"}}, nil)); !strings.Contains(msg, "no such tag") {
		t.Fatalf("unknown tag error = %q", msg)
	}
	call(t, cs, "create_task", map[string]any{"name": "タグ付き", "tags": []string{"買い物"}, "notes": "*メモ*"}, &w)
	if d, _ := st.GetTask(ctx, w.TaskGID); !slices.Equal(d.Tags, []string{"買い物"}) || !strings.Contains(d.HTMLNotes, "<em>メモ</em>") {
		t.Fatalf("created task: tags=%v html=%q", d.Tags, d.HTMLNotes)
	}
}

func TestResolveOutbox(t *testing.T) {
	st := seed(t)
	ctx := context.Background()
	cs := connect(t, &Server{Store: st})
	_ = st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "ローカル"}, testNow)
	ops, _ := st.ListOps(ctx)
	_ = st.MarkOpConflict(ctx, ops[0].ID, map[string]any{"name": "サーバー"})

	var w WriteOutput
	call(t, cs, "resolve_outbox", map[string]any{"id": ops[0].ID, "action": "keep_local"}, &w)
	if op, _ := st.GetOp(ctx, ops[0].ID); op.State != store.OpPending || op.Base["name"] != "サーバー" {
		t.Fatalf("keep_local should requeue with the server value as base: %+v", op)
	}
	call(t, cs, "resolve_outbox", map[string]any{"id": ops[0].ID, "action": "discard"}, &w)
	if ops, _ := st.ListOps(ctx); len(ops) != 0 {
		t.Fatal("discard should drop the entry")
	}
	// keep_local made the server's value the base, so discarding shows the server's name.
	if d, _ := st.GetTask(ctx, "t1"); d.Name != "サーバー" {
		t.Fatalf("discard should restore the last known server value, got %q", d.Name)
	}
	if msg := toolError(call(t, cs, "resolve_outbox", map[string]any{"id": 999, "action": "discard"}, nil)); msg == "" {
		t.Fatal("unknown id should be an error")
	}
}

func TestGetAttachmentReturnsImage(t *testing.T) {
	st := seed(t)
	ctx := context.Background()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	path := filepath.Join(t.TempDir(), "attachments", "42-plan.png")
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	_ = os.WriteFile(path, buf.Bytes(), 0o600)
	_ = st.UpsertAttachment(ctx, "t1", asana.Attachment{GID: "42", Name: "plan.png"})
	_ = st.SetAttachmentPath(ctx, "42", path)
	cs := connect(t, &Server{Store: st})
	res := call(t, cs, "get_attachment", map[string]any{"gid": "42", "task_gid": "t1"}, nil)
	if res.IsError || len(res.Content) == 0 {
		t.Fatalf("result = %+v", res)
	}
	ic, ok := res.Content[0].(*mcp.ImageContent)
	if !ok || ic.MIMEType != "image/png" || !bytes.Equal(ic.Data, buf.Bytes()) {
		t.Fatalf("first content should be the PNG image: %T", res.Content[0])
	}
}

func TestListRecentlyModified(t *testing.T) {
	st := seed(t)
	ctx := context.Background()
	_ = st.UpsertTasks(ctx, []asana.Task{
		{GID: "t1", Name: "ソファを買う", Assignee: &asana.Ref{GID: "me"}, ModifiedAt: testNow.Add(-2 * time.Hour)},
		{GID: "t2", Name: "転居届を出す", Completed: true, Assignee: &asana.Ref{GID: "me"}, ModifiedAt: testNow.Add(-time.Hour)},
		{GID: "t3", Name: "電気の契約", ModifiedAt: testNow.AddDate(0, 0, -30)},
	})
	cs := connect(t, &Server{Store: st})
	var tasks TasksOutput
	call(t, cs, "list_tasks", map[string]any{"assignee": "any", "modified_within_days": 7}, &tasks)
	if got := names(tasks.Tasks); !slices.Equal(got, []string{"転居届を出す", "ソファを買う"}) {
		t.Fatalf("recent = %v", got)
	}
}

func TestTaskExtras(t *testing.T) {
	st := seed(t)
	ctx := context.Background()
	_ = st.UpsertUsers(ctx, []asana.User{{GID: "u2", Name: "Partner"}})
	_ = st.UpsertProjects(ctx, "w1", []asana.Project{{GID: "p2", Name: "家計"}}, "me")
	_ = st.ReplaceSections(ctx, "p2", []asana.Section{{GID: "s9", Name: "今月"}})
	uploads := t.TempDir()
	cs := connect(t, &Server{Store: st, CacheDir: t.TempDir(), Upload: UploadPolicy{Allowed: []string{uploads}}})
	var w WriteOutput
	call(t, cs, "update_task", map[string]any{"gid": "t1", "liked": true, "add_followers": []string{"Partner", "me"}, "add_projects": []string{"家計"}}, &w)
	d, _ := st.GetTask(ctx, "t1")
	if !d.Liked || d.NumLikes != 1 || len(d.Followers) != 2 || len(d.Memberships) != 2 {
		t.Fatalf("task after update: liked=%v likes=%d followers=%v memberships=%v", d.Liked, d.NumLikes, d.Followers, d.Memberships)
	}
	call(t, cs, "update_task", map[string]any{"gid": "t1", "remove_followers": []string{"me"}, "remove_projects": []string{"家計"}}, &w)
	d, _ = st.GetTask(ctx, "t1")
	if len(d.Followers) != 1 || d.Followers[0].GID != "u2" || len(d.Memberships) != 1 {
		t.Fatalf("after removals: followers=%v memberships=%v", d.Followers, d.Memberships)
	}
	call(t, cs, "duplicate_task", map[string]any{"gid": "t1"}, &w)
	if c, err := st.GetTask(ctx, w.TaskGID); err != nil || c.Name != "ソファを買う のコピー" || len(c.Memberships) != 1 {
		t.Fatalf("copy = %+v %v", c, err)
	}
	file := filepath.Join(uploads, "見積 書.pdf")
	_ = os.WriteFile(file, []byte("pdf"), 0o600)
	call(t, cs, "upload_attachment", map[string]any{"task_gid": "t1", "path": file}, &w)
	d, _ = st.GetTask(ctx, "t1")
	if len(d.Attachments) != 1 || d.Attachments[0].Name != "見積 書.pdf" || strings.Contains(filepath.Base(d.Attachments[0].LocalPath), " ") {
		t.Fatalf("attachments = %+v", d.Attachments)
	}
}

func TestProjectStatusAndOnlineOnly(t *testing.T) {
	st := seed(t)
	cs := connect(t, &Server{Store: st}) // offline
	var w WriteOutput
	call(t, cs, "create_status_update", map[string]any{"project": "新居", "status": "at_risk", "title": "遅れ気味", "text": "**家具**の到着が遅れる"}, &w)
	var p ProjectOutput
	call(t, cs, "get_project", map[string]any{"project": "新居"}, &p)
	if len(p.Statuses) != 1 || p.Statuses[0].Status != "at_risk" || p.Statuses[0].Markdown != "**家具**の到着が遅れる" || !p.Statuses[0].Pending {
		t.Fatalf("statuses = %+v", p.Statuses)
	}
	if p.Project.Status != "at_risk" || len(p.Project.Sections) != 2 {
		t.Fatalf("project = %+v", p.Project)
	}
	if msg := toolError(call(t, cs, "create_status_update", map[string]any{"project": "新居", "status": "great", "text": "x"}, nil)); !strings.Contains(msg, "on_track") {
		t.Fatalf("bad status error = %q", msg)
	}
	if msg := toolError(call(t, cs, "create_project", map[string]any{"name": "新規"}, nil)); !strings.Contains(msg, "接続できない") {
		t.Fatalf("offline create_project should explain: %q", msg)
	}
}
