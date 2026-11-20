package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	gosync "sync"
	"testing"

	"github.com/amasok/asagao/internal/auth"
	"github.com/amasok/asagao/internal/store"
)

// fakeTemplateAsana answers the template commands in the shapes of Asana's reference: a
// job that has already succeeded, naming what it made.
type fakeTemplateAsana struct {
	mu     gosync.Mutex
	posts  []string
	status int // for the POSTs, instead of the job
	name   string
}

func (f *fakeTemplateAsana) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, _ := io.ReadAll(r.Body)
	var body struct {
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(raw, &body)
	reply := func(v any) { _ = json.NewEncoder(w).Encode(map[string]any{"data": v}) }
	key := r.Method + " " + r.URL.Path
	if r.Method == http.MethodPost {
		f.posts = append(f.posts, key+" "+string(bytes.TrimSpace(raw)))
		f.name, _ = body.Data["name"].(string)
		if f.status != 0 {
			http.Error(w, `{"errors":[{"message":"fake"}]}`, f.status)
			return
		}
	}
	task := map[string]any{"gid": "t9", "name": f.name, "workspace": map[string]string{"gid": "w1"},
		"memberships": []any{map[string]any{"project": map[string]string{"gid": "p1"}, "section": map[string]string{"gid": "s1"}}}}
	project := map[string]any{"gid": "p9", "name": f.name, "workspace": map[string]string{"gid": "w1"}, "members": []any{map[string]string{"gid": "me"}}}
	switch key {
	case "GET /users/me":
		reply(map[string]string{"gid": "me", "name": "Me"})
	case "GET /task_templates":
		reply([]any{map[string]any{"gid": "tt1", "name": "週次レビュー", "project": map[string]string{"gid": r.URL.Query().Get("project")}}})
	case "GET /project_templates":
		reply([]any{map[string]any{"gid": "pt1", "name": "引っ越しテンプレ", "team": map[string]string{"gid": "team1", "name": "家族"},
			"requested_dates": []any{map[string]string{"gid": "d1", "name": "開始日"}},
			"requested_roles": []any{map[string]string{"gid": "r1", "name": "担当"}}}})
	case "POST /task_templates/tt1/instantiateTask":
		reply(map[string]any{"gid": "j1", "status": "succeeded", "new_task": map[string]any{"gid": "t9", "name": f.name}})
	case "POST /project_templates/pt1/instantiateProject", "POST /projects/p1/duplicate":
		reply(map[string]any{"gid": "j1", "status": "succeeded", "new_project": map[string]any{"gid": "p9", "name": f.name}})
	case "GET /tasks/t9":
		reply(task)
	case "GET /tasks/t9/subtasks", "GET /tasks/t9/stories", "GET /attachments", "GET /projects/p9/sections":
		reply([]any{})
	case "GET /tasks":
		reply([]any{})
	case "GET /projects/p9":
		reply(project)
	default:
		http.Error(w, `{"errors":[{"message":"not in the fake: `+key+`"}]}`, http.StatusNotFound)
	}
}

func templateCLIEnv(t *testing.T) (string, *fakeTemplateAsana, func(args ...string) (string, error)) {
	t.Helper()
	path := writeCLIEnv(t)
	t.Setenv(auth.EnvToken, "test-pat")
	fake := &fakeTemplateAsana{}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	run := func(args ...string) (string, error) {
		cmd := newTestRoot(srv.URL)
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		err := cmd.ExecuteContext(context.Background())
		return out.String(), err
	}
	return path, fake, run
}

func TestTemplateCommands(t *testing.T) {
	path, fake, run := templateCLIEnv(t)
	out, err := run("template", "tasks", "新居")
	if err != nil || !strings.Contains(out, "週次レビュー  (tt1)") {
		t.Fatalf("tasks: %v\n%s", err, out)
	}
	out, err = run("template", "projects")
	if err != nil || !strings.Contains(out, "引っ越しテンプレ  (pt1)  チーム: 家族") || !strings.Contains(out, "日付: 開始日  (d1)") || !strings.Contains(out, "役割: 担当  (r1)") {
		t.Fatalf("projects: %v\n%s", err, out)
	}

	out, err = run("template", "new-task", "新居", "週次")
	if err != nil || !strings.Contains(out, "タスクを作りました: 週次レビュー (t9)") {
		t.Fatalf("new-task: %v\n%s", err, out)
	}
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if d, err := st.GetTask(context.Background(), "t9"); err != nil || d.Name != "週次レビュー" {
		t.Errorf("task not stored: %+v %v", d, err)
	}
	st.Close()

	out, err = run("template", "new-project", "引っ越し", "新しい", "家", "--date", "開始日=2026-12-01", "--role", "担当=me", "--public")
	if err != nil || !strings.Contains(out, "プロジェクトを作りました: 新しい 家 (p9)") {
		t.Fatalf("new-project: %v\n%s", err, out)
	}
	last := fake.posts[len(fake.posts)-1]
	for _, want := range []string{`"name":"新しい 家"`, `"public":true`, `"requested_dates":[{"gid":"d1","value":"2026-12-01"}]`, `"requested_roles":[{"gid":"r1","value":"me"}]`} {
		if !strings.Contains(last, want) {
			t.Errorf("new-project body %s lacks %s", last, want)
		}
	}

	out, err = run("project", "duplicate", "新居")
	if err != nil || !strings.Contains(out, "プロジェクトを作りました: 新居 のコピー (p9)") {
		t.Fatalf("duplicate: %v\n%s", err, out)
	}
	if last := fake.posts[len(fake.posts)-1]; !strings.HasPrefix(last, "POST /projects/p1/duplicate ") || !strings.Contains(last, `"name":"新居 のコピー"`) {
		t.Errorf("duplicate post = %s", last)
	}

}

// Bad input is refused before anything is sent; a dry run sends nothing.
func TestTemplateCommandsRefuseAndDryRun(t *testing.T) {
	_, fake, run := templateCLIEnv(t)
	for _, args := range [][]string{
		{"template", "new-project", "引っ越し", "x", "--date", "開始日=12/1"},
		{"template", "new-project", "引っ越し", "x", "--date", "無い日=2026-12-01"},
		{"template", "new-project", "引っ越し", "x", "--date", "開始日"},
		{"template", "new-project", "引っ越し", "x", "--date", "開始日=2026-12-01", "--date", "開始日=2026-12-02"},
		{"template", "new-project", "引っ越し", "x", "--role", "担当=none"},
		{"template", "new-project", "無いテンプレ", "x"},
		{"template", "new-task", "新居", "無いテンプレ"},
		{"template", "new-task", "無いプロジェクト", "週次"},
		{"project", "duplicate", "無いプロジェクト"},
		{"template", "new-task", "新居", "週次", "--dry-run"},
		{"template", "new-project", "引っ越し", "x", "--dry-run"},
		{"project", "duplicate", "新居", "--dry-run"},
	} {
		out, err := run(args...)
		dry := args[len(args)-1] == "--dry-run"
		if dry != (err == nil) {
			t.Errorf("%v: err = %v\n%s", args, err, out)
		}
	}
	if len(fake.posts) != 0 {
		t.Errorf("sent: %v", fake.posts)
	}
}

func TestTemplateCommandsOffline(t *testing.T) {
	writeCLIEnv(t)
	for _, args := range [][]string{
		{"template", "tasks", "新居"},
		{"template", "projects"},
		{"template", "new-task", "新居", "週次"},
		{"template", "new-project", "引っ越し", "x"},
		{"project", "duplicate", "新居"},
	} {
		out, err := runCLI(t, args...)
		if err == nil || !strings.Contains(err.Error(), "接続できません") {
			t.Errorf("%v: should fail as offline: %v\n%s", args, err, out)
		}
	}
}

// A 402 says the plan lacks templates.
func TestTemplateCommandPlan(t *testing.T) {
	_, fake, run := templateCLIEnv(t)
	fake.status = http.StatusPaymentRequired
	_, err := run("template", "new-task", "新居", "週次")
	if err == nil || !strings.Contains(err.Error(), "プランでは") {
		t.Fatalf("err = %v", err)
	}
}
