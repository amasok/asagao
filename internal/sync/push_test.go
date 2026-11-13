package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	stdsync "sync"
	"testing"
	"time"

	"github.com/amasok/asana-tui/internal/asana"
	"github.com/amasok/asana-tui/internal/config"
	"github.com/amasok/asana-tui/internal/store"
)

// serverTask is the fake server's state for one task.
type serverTask struct {
	Name      string `json:"name"`
	Notes     string `json:"notes"`
	Completed bool   `json:"completed"`
	DueOn     string `json:"due_on"`
	Section   string `json:"-"`
	Project   string `json:"-"`
}

type writeServer struct {
	t     *testing.T
	mu    stdsync.Mutex
	tasks map[string]*serverTask
	calls []string // "METHOD path body"
	next  int
}

func (w *writeServer) taskJSON(gid string) string {
	st := w.tasks[gid]
	ms := "[]"
	if st.Project != "" {
		ms = fmt.Sprintf(`[{"project":{"gid":%q},"section":{"gid":%q}}]`, st.Project, st.Section)
	}
	return fmt.Sprintf(`{"gid":%q,"name":%q,"notes":%q,"completed":%t,"due_on":%q,"memberships":%s,"modified_at":"2026-11-13T00:00:00Z"}`,
		gid, st.Name, st.Notes, st.Completed, st.DueOn, ms)
}

func (w *writeServer) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	w.mu.Lock()
	defer w.mu.Unlock()
	b, _ := io.ReadAll(r.Body)
	var body struct {
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(b, &body)
	if r.Method != http.MethodGet {
		w.calls = append(w.calls, r.Method+" "+r.URL.Path+" "+strings.TrimSpace(string(b)))
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/tasks":
		w.next++
		gid := fmt.Sprintf("new%d", w.next)
		st := &serverTask{}
		st.Name, _ = body.Data["name"].(string)
		st.Completed, _ = body.Data["completed"].(bool)
		if ps, ok := body.Data["projects"].([]any); ok && len(ps) > 0 {
			st.Project, _ = ps[0].(string)
		}
		w.tasks[gid] = st
		fmt.Fprintf(rw, `{"data":%s}`, w.taskJSON(gid))
	case len(parts) == 3 && parts[0] == "sections" && parts[2] == "addTask":
		gid, _ := body.Data["task"].(string)
		w.tasks[gid].Section = parts[1]
		fmt.Fprint(rw, `{"data":{}}`)
	case len(parts) == 3 && parts[0] == "tasks" && parts[2] == "stories":
		if _, ok := w.tasks[parts[1]]; !ok {
			rw.WriteHeader(http.StatusNotFound)
			fmt.Fprint(rw, `{"errors":[{"message":"not found"}]}`)
			return
		}
		fmt.Fprintf(rw, `{"data":{"gid":"story-%s","type":"comment","text":%q,"created_by":{"gid":"me","name":"Me"},"created_at":"2026-11-13T00:00:00Z"}}`,
			parts[1], body.Data["text"])
	case len(parts) == 2 && parts[0] == "tasks":
		st, ok := w.tasks[parts[1]]
		if !ok {
			rw.WriteHeader(http.StatusNotFound)
			fmt.Fprint(rw, `{"errors":[{"message":"task not found"}]}`)
			return
		}
		if r.Method == http.MethodPut {
			for k, v := range body.Data {
				switch k {
				case "name":
					st.Name, _ = v.(string)
				case "completed":
					st.Completed, _ = v.(bool)
				case "due_on":
					st.DueOn, _ = v.(string)
				}
			}
		}
		fmt.Fprintf(rw, `{"data":%s}`, w.taskJSON(parts[1]))
	default:
		w.t.Errorf("unexpected request %s %s", r.Method, r.URL)
		rw.WriteHeader(http.StatusTeapot)
	}
}

func setupPush(t *testing.T) (*writeServer, *httptest.Server, *Syncer, *store.Store) {
	t.Helper()
	ws := &writeServer{t: t, tasks: map[string]*serverTask{
		"t1": {Name: "A", Project: "p1", Section: "s1"},
	}}
	srv := httptest.NewServer(ws)
	t.Cleanup(srv.Close)
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	api := asana.NewClient(srv.Client())
	api.BaseURL = srv.URL
	api.MaxRetries = 0
	if err := st.UpsertProjects(ctx, "w1", []asana.Project{{GID: "p1", Name: "P"}}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "A", Workspace: &asana.Ref{GID: "w1"},
		Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}, Section: &asana.Ref{GID: "s1"}}}}}); err != nil {
		t.Fatal(err)
	}
	return ws, srv, &Syncer{API: api, Store: st, Cfg: config.SyncConfig{CompletedDays: 14}}, st
}

var now = time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)

func pendingCount(t *testing.T, st *store.Store) (int, int, int) {
	t.Helper()
	p, c, f, err := st.OutboxCounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return p, c, f
}

func TestLocalUpdateMergesAndReverts(t *testing.T) {
	_, _, _, st := setupPush(t)
	ctx := context.Background()
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"completed": true}, now); err != nil {
		t.Fatal(err)
	}
	d, _ := st.GetTask(ctx, "t1")
	if !d.Completed || !d.Pending {
		t.Fatalf("local change not applied: %+v", d.TaskSummary)
	}
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "B"}, now); err != nil {
		t.Fatal(err)
	}
	ops, _ := st.ListOps(ctx)
	if len(ops) != 1 || len(ops[0].Payload) != 2 {
		t.Fatalf("edits should merge into one entry: %+v", ops)
	}
	// Reverting both fields removes the entry.
	_ = st.UpdateTaskLocal(ctx, "t1", map[string]any{"completed": false}, now)
	_ = st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "A"}, now)
	if p, _, _ := pendingCount(t, st); p != 0 {
		t.Fatalf("reverted edits should leave no pending entry, got %d", p)
	}
}

func TestPullDoesNotOverwritePendingChange(t *testing.T) {
	_, _, _, st := setupPush(t)
	ctx := context.Background()
	_ = st.UpdateTaskLocal(ctx, "t1", map[string]any{"completed": true}, now)
	// A sync pulls the server's (old) version of the task.
	if err := st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "A (renamed on server)", Completed: false}}); err != nil {
		t.Fatal(err)
	}
	d, _ := st.GetTask(ctx, "t1")
	if !d.Completed {
		t.Fatal("pull overwrote the unsent completion")
	}
	if d.Name != "A (renamed on server)" {
		t.Fatalf("fields we did not change should follow the server, got %q", d.Name)
	}
}

func TestPushUpdate(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	_ = st.UpdateTaskLocal(ctx, "t1", map[string]any{"completed": true, "due_on": "2026-11-20"}, now)
	ws.tasks["t1"].Notes = "edited on server" // a different field: not a conflict

	res, err := s.Push(ctx)
	if err != nil || res.Sent != 1 || res.Conflicts != 0 {
		t.Fatalf("push = %+v, %v", res, err)
	}
	if !ws.tasks["t1"].Completed || ws.tasks["t1"].DueOn != "2026-11-20" {
		t.Fatalf("server not updated: %+v", ws.tasks["t1"])
	}
	if p, _, _ := pendingCount(t, st); p != 0 {
		t.Fatal("sent entry should be removed")
	}
	if d, _ := st.GetTask(ctx, "t1"); d.Pending || d.Notes != "edited on server" {
		t.Fatalf("store should reflect the server after push: %+v", d)
	}
}

func TestPushConflictThenKeepLocal(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	_ = st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "C (local)"}, now)
	ws.tasks["t1"].Name = "B (server)"

	res, err := s.Push(ctx)
	if err != nil || res.Conflicts != 1 || res.Sent != 0 {
		t.Fatalf("push = %+v, %v", res, err)
	}
	for _, c := range ws.calls {
		if strings.HasPrefix(c, "PUT") {
			t.Fatalf("conflicting change must not be sent: %v", ws.calls)
		}
	}
	ops, _ := st.ListOps(ctx)
	if ops[0].State != store.OpConflict || ops[0].Conflict["name"] != "B (server)" {
		t.Fatalf("op = %+v", ops[0])
	}
	if d, _ := st.GetTask(ctx, "t1"); d.Name != "C (local)" {
		t.Fatalf("local value should stay visible while in conflict, got %q", d.Name)
	}

	if err := st.ResolveKeepLocal(ctx, ops[0].ID); err != nil {
		t.Fatal(err)
	}
	if res, err := s.Push(ctx); err != nil || res.Sent != 1 {
		t.Fatalf("push after resolve = %+v, %v", res, err)
	}
	if ws.tasks["t1"].Name != "C (local)" {
		t.Fatalf("server name = %q", ws.tasks["t1"].Name)
	}
}

func TestDiscardRestoresBase(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	_ = st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "C"}, now)
	ws.tasks["t1"].Name = "B"
	_, _ = s.Push(ctx)
	ops, _ := st.ListOps(ctx)
	if err := st.DiscardOp(ctx, ops[0].ID); err != nil {
		t.Fatal(err)
	}
	if d, _ := st.GetTask(ctx, "t1"); d.Name != "A" || d.Pending {
		t.Fatalf("discard should restore the pre-edit value until the next sync: %+v", d.TaskSummary)
	}
}

func TestOfflineCreateCommentAndComplete(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	gid, err := st.CreateTaskLocal(ctx, store.NewTask{ProjectGID: "p1", SectionGID: "s2", Name: "牛乳を買う", AssigneeGID: "me"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !store.IsLocalGID(gid) {
		t.Fatalf("gid = %q", gid)
	}
	if _, err := st.AddCommentLocal(ctx, gid, "低脂肪のやつ", store.UserRef{GID: "me", Name: "Me"}, now); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateTaskLocal(ctx, gid, map[string]any{"completed": true}, now); err != nil {
		t.Fatal(err)
	}
	if p, _, _ := pendingCount(t, st); p != 2 {
		t.Fatalf("completion should fold into the create: %d pending", p)
	}
	// Visible locally, with the pending comment.
	d, err := st.GetTask(ctx, gid)
	if err != nil || !d.Completed || len(d.Stories) != 1 || !d.Stories[0].Pending {
		t.Fatalf("local task %+v, %v", d, err)
	}

	res, err := s.Push(ctx)
	if err != nil || res.Sent != 2 {
		t.Fatalf("push = %+v, %v (calls %v)", res, err, ws.calls)
	}
	created := ws.tasks["new1"]
	if created == nil || created.Name != "牛乳を買う" || !created.Completed || created.Section != "s2" {
		t.Fatalf("server task = %+v", created)
	}
	if !strings.Contains(ws.calls[len(ws.calls)-1], "/tasks/new1/stories") {
		t.Fatalf("comment should be posted to the real gid: %v", ws.calls)
	}
	if _, err := st.GetTask(ctx, gid); err == nil {
		t.Fatal("local gid should be replaced")
	}
	d, err = st.GetTask(ctx, "new1")
	if err != nil || d.Pending || len(d.Stories) != 1 || d.Stories[0].GID != "story-new1" {
		t.Fatalf("store after push %+v, %v", d, err)
	}
	if len(d.Memberships) != 1 || d.Memberships[0].SectionGID != "s2" {
		t.Fatalf("memberships %+v", d.Memberships)
	}
}

func TestPushOfflineKeepsQueue(t *testing.T) {
	_, srv, s, st := setupPush(t)
	ctx := context.Background()
	_ = st.UpdateTaskLocal(ctx, "t1", map[string]any{"completed": true}, now)
	srv.Close() // network down

	res, err := s.Push(ctx)
	if err == nil || res.Remaining != 1 {
		t.Fatalf("expected offline error, got %+v, %v", res, err)
	}
	if p, _, f := pendingCount(t, st); p != 1 || f != 0 {
		t.Fatalf("entry should stay pending: p=%d f=%d", p, f)
	}
}

func TestPushDeletedTaskFails(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	_ = st.UpdateTaskLocal(ctx, "t1", map[string]any{"completed": true}, now)
	delete(ws.tasks, "t1")
	res, err := s.Push(ctx)
	if err != nil || res.Failed != 1 {
		t.Fatalf("push = %+v, %v", res, err)
	}
	ops, _ := st.ListOps(ctx)
	if ops[0].State != store.OpFailed || !strings.Contains(ops[0].LastError, "404") {
		t.Fatalf("op = %+v", ops[0])
	}
}

func TestPruneKeepsLocalTasks(t *testing.T) {
	_, _, _, st := setupPush(t)
	ctx := context.Background()
	gid, _ := st.CreateTaskLocal(ctx, store.NewTask{ProjectGID: "p1", Name: "offline"}, now)
	_ = st.UpdateTaskLocal(ctx, "t1", map[string]any{"completed": true}, now)
	if n, err := st.PruneTasks(ctx, map[string]bool{}); err != nil || n != 0 {
		t.Fatalf("pruned %d, %v", n, err)
	}
	if _, err := st.GetTask(ctx, gid); err != nil {
		t.Fatal("offline task was pruned")
	}
}

func sectionOf(t *testing.T, st *store.Store, gid string) string {
	t.Helper()
	d, err := st.GetTask(context.Background(), gid)
	if err != nil || len(d.Memberships) == 0 {
		t.Fatalf("task %s: %+v %v", gid, d, err)
	}
	return d.Memberships[0].SectionGID
}

func TestMoveTask(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	if err := st.ReplaceSections(ctx, "p1", []asana.Section{{GID: "s1", Name: "Todo"}, {GID: "s2", Name: "Doing"}, {GID: "s3", Name: "Done"}}); err != nil {
		t.Fatal(err)
	}

	// Move and move back: nothing to send.
	_ = st.MoveTaskLocal(ctx, "t1", "p1", "s2", now)
	_ = st.MoveTaskLocal(ctx, "t1", "p1", "s1", now)
	if p, _, _ := pendingCount(t, st); p != 0 {
		t.Fatalf("moving back should cancel the move, %d pending", p)
	}

	// Two moves merge into one entry.
	_ = st.MoveTaskLocal(ctx, "t1", "p1", "s2", now)
	_ = st.MoveTaskLocal(ctx, "t1", "p1", "s3", now)
	ops, _ := st.ListOps(ctx)
	if len(ops) != 1 || ops[0].Payload["section"] != "s3" || ops[0].Payload["section_name"] != "Done" {
		t.Fatalf("ops = %+v", ops)
	}
	// A pull with the old section does not undo the local move.
	_ = st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "A", Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}, Section: &asana.Ref{GID: "s1"}}}}})
	if got := sectionOf(t, st, "t1"); got != "s3" {
		t.Fatalf("pull undid the move: %s", got)
	}

	res, err := s.Push(ctx)
	if err != nil || res.Sent != 1 {
		t.Fatalf("push = %+v, %v", res, err)
	}
	if ws.tasks["t1"].Section != "s3" {
		t.Fatalf("server section = %q (calls %v)", ws.tasks["t1"].Section, ws.calls)
	}
	if got := sectionOf(t, st, "t1"); got != "s3" {
		t.Fatalf("store section after push = %s", got)
	}

	// Discard restores the previous section.
	_ = st.MoveTaskLocal(ctx, "t1", "p1", "s1", now)
	ops, _ = st.ListOps(ctx)
	if err := st.DiscardOp(ctx, ops[0].ID); err != nil {
		t.Fatal(err)
	}
	if got := sectionOf(t, st, "t1"); got != "s3" {
		t.Fatalf("discard should restore s3, got %s", got)
	}

	// Errors: unknown section, task not in project.
	if err := st.MoveTaskLocal(ctx, "t1", "p1", "nope", now); err == nil {
		t.Error("unknown section should fail")
	}
}

func TestRenameSection(t *testing.T) {
	_, _, _, st := setupPush(t)
	ctx := context.Background()
	if err := st.ReplaceSections(ctx, "p1", []asana.Section{{GID: "s1", Name: "In Pregoress"}}); err != nil {
		t.Fatal(err)
	}
	_ = st.RenameSectionLocal(ctx, "s1", "In Progress", now)
	// A sync that brings the old name back keeps the unsent rename.
	_ = st.ReplaceSections(ctx, "p1", []asana.Section{{GID: "s1", Name: "In Pregoress"}})
	if name, _ := st.SectionName(ctx, "s1"); name != "In Progress" {
		t.Fatalf("rename lost after sync: %q", name)
	}
	ops, _ := st.ListOps(ctx)
	if len(ops) != 1 || ops[0].Kind != store.OpRenameSection || ops[0].TaskName != "In Progress" {
		t.Fatalf("ops = %+v", ops)
	}
	_ = st.RenameSectionLocal(ctx, "s1", "In Pregoress", now) // back to the original
	if p, _, _ := pendingCount(t, st); p != 0 {
		t.Fatal("renaming back should cancel")
	}
	if err := st.RenameSectionLocal(ctx, "s1", "  ", now); err == nil {
		t.Fatal("empty name should fail")
	}
}

func TestPushRenameSection(t *testing.T) {
	ctx := context.Background()
	serverName := "In Pregoress"
	var put string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			var body struct{ Data map[string]string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			put, serverName = body.Data["name"], body.Data["name"]
		}
		fmt.Fprintf(w, `{"data":{"gid":"s1","name":%q}}`, serverName)
	}))
	defer srv.Close()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	api := asana.NewClient(srv.Client())
	api.BaseURL = srv.URL
	s := &Syncer{API: api, Store: st}
	_ = st.ReplaceSections(ctx, "p1", []asana.Section{{GID: "s1", Name: "In Pregoress"}})

	_ = st.RenameSectionLocal(ctx, "s1", "In Progress", now)
	if res, err := s.Push(ctx); err != nil || res.Sent != 1 || put != "In Progress" {
		t.Fatalf("push = %+v %v put=%q", res, err, put)
	}

	// Conflict: renamed on the server to something else meanwhile.
	_ = st.RenameSectionLocal(ctx, "s1", "Doing", now)
	serverName = "WIP"
	if res, err := s.Push(ctx); err != nil || res.Conflicts != 1 {
		t.Fatalf("expected conflict: %+v %v", res, err)
	}
}

func TestDownloadAttachment(t *testing.T) {
	ctx := context.Background()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		fmt.Fprint(w, `{"data":{"gid":"a1","name":"receipt.png","host":"asana","download_url":"https://files.example/a1?sig=x"}}`)
	}))
	defer srv.Close()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_ = st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "T"}})
	_ = st.ReplaceAttachments(ctx, "t1", []asana.Attachment{{GID: "a1", Name: "receipt.png"}, {GID: "a2", Name: "memo.pdf"}})
	api := asana.NewClient(srv.Client())
	api.BaseURL = srv.URL
	var downloaded []string
	s := &Syncer{API: api, Store: st, CacheDir: t.TempDir(), Download: func(_ context.Context, url string) ([]byte, error) {
		downloaded = append(downloaded, url)
		return []byte("PNGDATA"), nil
	}}

	path, err := s.DownloadAttachment(ctx, "t1", "a1")
	if err != nil || !strings.HasSuffix(path, "a1-receipt.png") {
		t.Fatalf("path %q, %v", path, err)
	}
	if b, _ := os.ReadFile(path); string(b) != "PNGDATA" || len(downloaded) != 1 || downloaded[0] != "https://files.example/a1?sig=x" {
		t.Fatalf("download: %q %v", b, downloaded)
	}
	// Cached: no second API call or download, also when offline.
	offline := &Syncer{Store: st}
	if p2, err := offline.DownloadAttachment(ctx, "t1", "a1"); err != nil || p2 != path || calls != 1 {
		t.Fatalf("cached path %q %v calls=%d", p2, err, calls)
	}
	// A sync of the attachment list keeps the downloaded path.
	_ = st.ReplaceAttachments(ctx, "t1", []asana.Attachment{{GID: "a1", Name: "receipt.png"}})
	d, _ := st.GetTask(ctx, "t1")
	if len(d.Attachments) != 1 || d.Attachments[0].LocalPath != path || !d.Attachments[0].IsImage {
		t.Fatalf("attachments %+v", d.Attachments)
	}
	if _, err := offline.DownloadAttachment(ctx, "t1", "a2"); err == nil {
		t.Fatal("offline download of an uncached file should fail")
	}
}

func TestDownloadInlineImageByGID(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/attachments/1220507679870013" {
			t.Errorf("unexpected %s", r.URL)
		}
		fmt.Fprint(w, `{"data":{"gid":"1220507679870013","name":"image.png","host":"asana","download_url":"https://files.example/x"}}`)
	}))
	defer srv.Close()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_ = st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "T"}})
	api := asana.NewClient(srv.Client())
	api.BaseURL = srv.URL
	s := &Syncer{API: api, Store: st, CacheDir: t.TempDir(),
		Download: func(context.Context, string) ([]byte, error) { return []byte("img"), nil }}
	path, err := s.DownloadAttachment(ctx, "t1", "1220507679870013")
	if err != nil || !strings.HasSuffix(path, "1220507679870013-image.png") {
		t.Fatalf("path %q %v", path, err)
	}
	d, _ := st.GetTask(ctx, "t1")
	if len(d.Attachments) != 1 || d.Attachments[0].LocalPath != path {
		t.Fatalf("inline image should be recorded as an attachment: %+v", d.Attachments)
	}
}

func TestFetchDetailsAttachmentsOptional(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/attachments":
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"errors":[{"message":"forbidden scope attachments:read"}]}`)
		case strings.HasSuffix(r.URL.Path, "/stories"):
			fmt.Fprint(w, `{"data":[{"gid":"c1","type":"comment","text":"hi"}]}`)
		case strings.HasSuffix(r.URL.Path, "/subtasks"):
			fmt.Fprint(w, `{"data":[]}`)
		default:
			fmt.Fprint(w, `{"data":{"gid":"t1","name":"T"}}`)
		}
	}))
	defer srv.Close()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	api := asana.NewClient(srv.Client())
	api.BaseURL = srv.URL
	err = (&Syncer{API: api, Store: st}).FetchDetails(ctx, "t1")
	var attErr *AttachmentsError
	if !errors.As(err, &attErr) {
		t.Fatalf("err = %v", err)
	}
	if d, _ := st.GetTask(ctx, "t1"); len(d.Stories) != 1 || d.DetailsFetchedAt == "" {
		t.Fatalf("comments should still be stored: %+v", d)
	}
}

func TestPushSubtaskOfOfflineParent(t *testing.T) {
	ctx := context.Background()
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		calls = append(calls, r.Method+" "+r.URL.Path+" "+string(b))
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/tasks":
			fmt.Fprint(w, `{"data":{"gid":"P1","name":"親"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/tasks/P1/subtasks":
			fmt.Fprint(w, `{"data":{"gid":"C1","name":"子","parent":{"gid":"P1","name":"親"}}}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
		}
	}))
	defer srv.Close()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_ = st.UpsertWorkspaces(ctx, []asana.Workspace{{GID: "w1"}})
	parent, _ := st.CreateTaskLocal(ctx, store.NewTask{WorkspaceGID: "w1", Name: "親"}, now)
	child, err := st.CreateTaskLocal(ctx, store.NewTask{ParentGID: parent, Name: "子"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := st.GetTask(ctx, parent); d.NumSubtasks != 1 || len(d.Subtasks) != 1 || d.Subtasks[0].GID != child {
		t.Fatalf("local parent should list the subtask: %+v", d)
	}
	api := asana.NewClient(srv.Client())
	api.BaseURL = srv.URL
	res, err := (&Syncer{API: api, Store: st}).Push(ctx)
	if err != nil || res.Sent != 2 {
		t.Fatalf("push = %+v %v (%v)", res, err, calls)
	}
	if !strings.Contains(calls[1], "POST /tasks/P1/subtasks") || strings.Contains(calls[1], "workspace") {
		t.Fatalf("subtask should be created under the real parent gid: %v", calls)
	}
	if d, err := st.GetTask(ctx, "C1"); err != nil || d.ParentGID != "P1" || d.ParentName != "親" {
		t.Fatalf("subtask after push: %+v %v", d, err)
	}
}

func TestPushRichNotesAndComment(t *testing.T) {
	ctx := context.Background()
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodGet {
			bodies = append(bodies, r.Method+" "+r.URL.Path+" "+string(b))
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/stories"):
			fmt.Fprint(w, `{"data":{"gid":"s1","type":"comment","text":"hi","html_text":"<body><strong>hi</strong></body>"}}`)
		default:
			fmt.Fprint(w, `{"data":{"gid":"t1","name":"T","notes":"old","html_notes":"<body>old</body>"}}`)
		}
	}))
	defer srv.Close()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_ = st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "T", Notes: "old", HTMLNotes: "<body>old</body>"}})
	_ = st.UpdateTaskLocal(ctx, "t1", map[string]any{"notes": "new", "html_notes": "<body><strong>new</strong></body>"}, now)
	if d, _ := st.GetTask(ctx, "t1"); d.HTMLNotes != "<body><strong>new</strong></body>" || d.Notes != "new" {
		t.Fatalf("local rich notes: %q / %q", d.HTMLNotes, d.Notes)
	}
	_, _ = st.AddRichCommentLocal(ctx, "t1", "hi", "<body><strong>hi</strong></body>", store.UserRef{GID: "me"}, now)
	api := asana.NewClient(srv.Client())
	api.BaseURL = srv.URL
	res, err := (&Syncer{API: api, Store: st}).Push(ctx)
	if err != nil || res.Sent != 2 {
		t.Fatalf("push = %+v %v", res, err)
	}
	if !strings.Contains(bodies[0], `"html_notes":"\u003cbody\u003e\u003cstrong\u003enew`) || strings.Contains(bodies[0], `"notes"`) {
		t.Fatalf("update should send html_notes only: %s", bodies[0])
	}
	if !strings.Contains(bodies[1], `"html_text"`) || strings.Contains(bodies[1], `"text":"hi"`) {
		t.Fatalf("comment should send html_text: %s", bodies[1])
	}
}
