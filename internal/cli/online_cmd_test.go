package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	gosync "sync"
	"testing"

	"github.com/amasok/asagao/internal/auth"
	"github.com/amasok/asagao/internal/store"
)

// fakePushAsana is a fake Asana for the write commands of writeCLIEnv: it accepts their
// changes, keeps t1 and section s1 as they are after the change, and records each
// non-GET request ("PUT /tasks/t1 {...}").
type fakePushAsana struct {
	mu        gosync.Mutex
	calls     []string
	completed bool
	assignee  string // "" = unassigned
	section   string
	sname     string
	archived  bool
}

func (f *fakePushAsana) task() map[string]any {
	var assignee any
	if f.assignee != "" {
		assignee = map[string]string{"gid": f.assignee}
	}
	return map[string]any{"gid": "t1", "name": "ソファを買う", "completed": f.completed, "assignee": assignee,
		"workspace":   map[string]string{"gid": "w1"},
		"memberships": []any{map[string]any{"project": map[string]string{"gid": "p1"}, "section": map[string]string{"gid": f.section}}}}
}

func (f *fakePushAsana) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var body struct {
		Data map[string]any `json:"data"`
	}
	raw, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(raw, &body)
	if r.Method != http.MethodGet {
		f.calls = append(f.calls, r.Method+" "+r.URL.Path+" "+string(bytes.TrimSpace(raw)))
	}
	reply := func(v any) { _ = json.NewEncoder(w).Encode(map[string]any{"data": v}) }
	project := func() map[string]any {
		return map[string]any{"gid": "p1", "name": "新居", "archived": f.archived, "workspace": map[string]string{"gid": "w1"},
			"members": []any{map[string]string{"gid": "me", "name": "Me"}, map[string]string{"gid": "u2", "name": "Partner"}}}
	}
	switch p := r.Method + " " + r.URL.Path; p {
	case "GET /users/me":
		reply(map[string]string{"gid": "me", "name": "Me"})
	case "GET /tasks/t1":
		reply(f.task())
	case "PUT /tasks/t1":
		if v, ok := body.Data["completed"].(bool); ok {
			f.completed = v
		}
		if v, ok := body.Data["assignee"]; ok {
			f.assignee, _ = v.(string)
		}
		reply(f.task())
	case "POST /sections/s2/addTask":
		f.section = "s2"
		reply(map[string]any{})
	case "POST /tasks/t1/stories":
		reply(map[string]any{"gid": "story1", "type": "comment", "resource_subtype": "comment_added", "text": body.Data["text"],
			"created_by": map[string]string{"gid": "me", "name": "Me"}, "created_at": "2026-11-13T00:00:00Z"})
	case "POST /tasks/t1/addTag":
		reply(map[string]any{})
	case "GET /sections/s1":
		reply(map[string]string{"gid": "s1", "name": f.sname})
	case "PUT /sections/s1":
		f.sname, _ = body.Data["name"].(string)
		reply(map[string]string{"gid": "s1", "name": f.sname})
	case "POST /projects":
		reply(map[string]any{"gid": "p9", "name": body.Data["name"], "workspace": map[string]string{"gid": "w1"}})
	case "PUT /projects/p1":
		f.archived, _ = body.Data["archived"].(bool)
		reply(project())
	case "POST /projects/p1/addMembers":
		reply(project())
	default:
		http.Error(w, `{"errors":[{"message":"not in the fake: `+p+`"}]}`, http.StatusNotFound)
	}
}

func (f *fakePushAsana) took() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// onlineCLIEnv is writeCLIEnv with a token and a fake Asana that takes the changes.
func onlineCLIEnv(t *testing.T) (string, *fakePushAsana, func(args ...string) (stdout, stderr string, err error)) {
	t.Helper()
	path := writeCLIEnv(t)
	t.Setenv(auth.EnvToken, "test-pat")
	fake := &fakePushAsana{assignee: "me", section: "s1", sname: "家具"}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	run := func(args ...string) (string, string, error) {
		cmd := newRoot(&app{baseURL: srv.URL})
		var out, errOut bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&errOut)
		cmd.SetArgs(args)
		err := cmd.ExecuteContext(context.Background())
		return out.String(), errOut.String(), err
	}
	return path, fake, run
}

// Online, a write command sends its change right away and says so; nothing is left in the
// outbox.
func TestWriteCommandsOnlineSend(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string // the request that carries the change
	}{
		{"complete", []string{"task", "complete", "t1"}, `PUT /tasks/t1 {"data":{"completed":true}}`},
		{"assign", []string{"task", "assign", "t1", "Partner"}, `PUT /tasks/t1 {"data":{"assignee":"u2"}}`},
		{"move", []string{"task", "move", "t1", "--section", "完了"}, `POST /sections/s2/addTask {"data":{"task":"t1"}}`},
		{"comment", []string{"comment", "add", "t1", "届く"}, `POST /tasks/t1/stories`},
		{"tag", []string{"task", "tag", "t1", "--add", "急ぎ"}, `POST /tasks/t1/addTag {"data":{"tag":"g1"}}`},
		{"section rename", []string{"section", "rename", "家具", "大型家具", "--project", "新居"}, `PUT /sections/s1 {"data":{"name":"大型家具"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, fake, run := onlineCLIEnv(t)
			out, errOut, err := run(tc.args...)
			if err != nil {
				t.Fatalf("%v\n%s%s", err, out, errOut)
			}
			if !strings.Contains(out, "Asana に送信しました") {
				t.Errorf("should say it was sent:\n%s%s", out, errOut)
			}
			if ops := cliOps(t, path); len(ops) != 0 {
				t.Errorf("left in the outbox: %v", opKinds(ops))
			}
			calls := fake.took()
			if len(calls) != 1 || !strings.HasPrefix(calls[0], tc.want) {
				t.Errorf("requests %q, want one %q", calls, tc.want)
			}
		})
	}
}

// --json online: sent, and no local gid left for a task created in Asana.
func TestWriteCommandJSONOnline(t *testing.T) {
	_, _, run := onlineCLIEnv(t)
	stdout, _, err := run("--json", "task", "complete", "t1")
	if err != nil {
		t.Fatal(err)
	}
	var r writeResult
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("json %q: %v", stdout, err)
	}
	if !r.Sent || r.Error != "" || r.TaskGID != "t1" || r.Push == nil || r.Push.Sent != 1 {
		t.Fatalf("result %+v", r)
	}
}

// When Asana has the task assigned to someone else meanwhile, assigning it is a conflict:
// the command says it could not send it, and the change stays in the outbox.
func TestWriteCommandOnlineConflict(t *testing.T) {
	path, fake, run := onlineCLIEnv(t)
	fake.assignee = "someone-else"
	out, errOut, err := run("task", "assign", "t1", "Partner")
	if err != nil {
		t.Fatalf("%v\n%s%s", err, out, errOut)
	}
	if !strings.Contains(out, "outbox list") {
		t.Errorf("should point at outbox list:\n%s", out)
	}
	ops := cliOps(t, path)
	if len(ops) != 1 || ops[0].State != store.OpConflict {
		t.Fatalf("ops %+v", ops)
	}
	if calls := fake.took(); len(calls) != 0 {
		t.Errorf("a conflict must not overwrite Asana: %q", calls)
	}
	// keep local: sent now
	out, errOut, err = run("outbox", "resolve", fmt.Sprint(ops[0].ID), "--keep", "local")
	if err != nil {
		t.Fatalf("%v\n%s%s", err, out, errOut)
	}
	if calls := fake.took(); len(calls) != 1 || !strings.HasPrefix(calls[0], `PUT /tasks/t1 {"data":{"assignee":"u2"}}`) {
		t.Errorf("requests %q", calls)
	}
	if ops := cliOps(t, path); len(ops) != 0 {
		t.Errorf("left in the outbox: %v", opKinds(ops))
	}
}

// Project commands online: create, archive / --undo, add a member.
func TestProjectCommandsOnline(t *testing.T) {
	_, fake, run := onlineCLIEnv(t)
	out, errOut, err := run("project", "create", "引っ越し")
	if err != nil || !strings.Contains(out, "引っ越し (p9)") {
		t.Fatalf("create: %v\n%s%s", err, out, errOut)
	}
	if out, errOut, err := run("project", "archive", "新居"); err != nil || !strings.Contains(out, "アーカイブしました") || !fake.archived {
		t.Fatalf("archive: %v\n%s%s", err, out, errOut)
	}
	if out, errOut, err := run("project", "archive", "新居", "--undo"); err != nil || !strings.Contains(out, "解除しました") || fake.archived {
		t.Fatalf("archive --undo: %v\n%s%s", err, out, errOut)
	}
	out, errOut, err = run("project", "members", "新居", "--add", "Partner")
	if err != nil {
		t.Fatalf("members --add: %v\n%s%s", err, out, errOut)
	}
	if !strings.Contains(out, "Me") || !strings.Contains(out, "Partner") {
		t.Errorf("members after adding:\n%s", out)
	}
	calls := fake.took()
	if len(calls) != 4 || !strings.HasPrefix(calls[3], `POST /projects/p1/addMembers {"data":{"members":"u2"}}`) {
		t.Errorf("requests %q", calls)
	}
}
