package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/amasok/asagao/internal/wire"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	gosync "sync"
	"testing"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/sync"
)

// templateFake answers the template tools in the shapes of Asana's reference; a job has
// already succeeded when the POST answers. empty: no templates. status: for the POSTs.
type templateFake struct {
	mu     gosync.Mutex
	posts  []string
	empty  bool
	status int
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
		if f.status != 0 {
			http.Error(w, `{"errors":[{"message":"fake"}]}`, f.status)
			return
		}
	}
	reply := func(v any) { _ = json.NewEncoder(w).Encode(map[string]any{"data": v}) }
	switch key {
	case "GET /task_templates":
		if f.empty {
			reply([]any{})
			return
		}
		reply([]any{map[string]any{"gid": "tt1", "name": "週次\x1bレビュー", "project": map[string]string{"gid": "p1"}}})
	case "GET /project_templates":
		if f.empty {
			reply([]any{})
			return
		}
		reply([]any{map[string]any{"gid": "pt1", "name": "引っ越し", "requested_dates": []any{map[string]string{"gid": "d1", "name": "開始日"}}}})
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
		reply(map[string]any{"gid": "p9", "name": "コピー", "workspace": map[string]string{"gid": "w1"}})
	default:
		http.Error(w, fmt.Sprintf(`{"errors":[{"message":"not in the fake: %s"}]}`, key), http.StatusNotFound)
	}
}

func templateServer(t *testing.T, readOnly bool) (*templateFake, func(name string, args map[string]any, out any) string) {
	t.Helper()
	st := seed(t)
	f := &templateFake{}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	cs := connect(t, &Server{App: wire.UseCases(st), ReadOnly: readOnly, NewSyncer: func(context.Context) (*sync.Syncer, error) {
		api := asana.NewClient(srv.Client())
		api.BaseURL, api.MaxRetries = srv.URL, 0
		return &sync.Syncer{API: api, Store: st}, nil
	}})
	return f, func(name string, args map[string]any, out any) string {
		return toolError(call(t, cs, name, args, out))
	}
}

func TestTemplateTools(t *testing.T) {
	f, call := templateServer(t, false)
	var tt TaskTemplatesOutput
	if msg := call("list_task_templates", map[string]any{"project": "新居"}, &tt); msg != "" {
		t.Fatal(msg)
	}
	if len(tt.Templates) != 1 || tt.Templates[0].Name != "週次レビュー" || tt.Note != "" {
		t.Fatalf("task templates = %+v", tt)
	}
	var pt ProjectTemplatesOutput
	if msg := call("list_project_templates", map[string]any{}, &pt); msg != "" || len(pt.Templates) != 1 || len(pt.Templates[0].RequestedDates) != 1 {
		t.Fatalf("project templates = %+v %s", pt, msg)
	}
	var made MadeOutput
	if msg := call("create_task_from_template", map[string]any{"project": "新居", "template": "週次"}, &made); msg != "" || made.GID != "t9" {
		t.Fatalf("task = %+v %s", made, msg)
	}
	made = MadeOutput{}
	if msg := call("create_project_from_template", map[string]any{"template": "pt1", "name": "新しい家", "dates": map[string]string{"開始日": "2027-01-04"}, "roles": map[string]string{}}, &made); msg != "" || made.GID != "p9" {
		t.Fatalf("project = %+v %s", made, msg)
	}
	if last := f.posts[len(f.posts)-1]; !strings.Contains(last, `{"gid":"d1","value":"2027-01-04"}`) {
		t.Errorf("post = %s", last)
	}
	made = MadeOutput{}
	if msg := call("duplicate_project", map[string]any{"project": "新居"}, &made); msg != "" || made.GID != "p9" {
		t.Fatalf("duplicate = %+v %s", made, msg)
	}
	if last := f.posts[len(f.posts)-1]; !strings.Contains(last, `"name":"[家族]新居プロジェクト のコピー"`) {
		t.Errorf("post = %s", last)
	}
	// Refused before sending: an unknown date, a role without a user, an unknown template.
	n := len(f.posts)
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"create_project_from_template", map[string]any{"template": "pt1", "name": "x", "dates": map[string]string{"無い": "2027-01-04"}}},
		{"create_project_from_template", map[string]any{"template": "pt1", "name": "x", "roles": map[string]string{"担当": "none"}}},
		{"create_project_from_template", map[string]any{"template": "無い", "name": "x"}},
		{"create_task_from_template", map[string]any{"project": "新居", "template": "無い"}},
	} {
		if msg := call(tc.tool, tc.args, nil); msg == "" {
			t.Errorf("%s %v: no error", tc.tool, tc.args)
		}
	}
	if len(f.posts) != n {
		t.Errorf("sent: %v", f.posts[n:])
	}
}

// No templates: an empty list with a note that points to duplicate_project.
func TestTemplateToolsEmpty(t *testing.T) {
	f, call := templateServer(t, true)
	f.empty = true
	var tt TaskTemplatesOutput
	if msg := call("list_task_templates", map[string]any{"project": "新居"}, &tt); msg != "" || tt.Templates == nil || !strings.Contains(tt.Note, "duplicate_project") {
		t.Fatalf("= %+v %s", tt, msg)
	}
	var pt ProjectTemplatesOutput
	if msg := call("list_project_templates", map[string]any{}, &pt); msg != "" || pt.Templates == nil || pt.Note == "" {
		t.Fatalf("= %+v %s", pt, msg)
	}
}

func TestTemplateToolsPlan(t *testing.T) {
	f, call := templateServer(t, false)
	f.status = http.StatusPaymentRequired
	if msg := call("create_task_from_template", map[string]any{"project": "新居", "template": "tt1"}, nil); !strings.Contains(msg, "プランでは") {
		t.Fatalf("msg = %q", msg)
	}
}

func TestTemplateToolsOffline(t *testing.T) {
	cs := connect(t, &Server{App: wire.UseCases(seed(t)), NewSyncer: func(context.Context) (*sync.Syncer, error) {
		return nil, fmt.Errorf("ログインしていません")
	}})
	for name, args := range map[string]map[string]any{
		"list_task_templates":          {"project": "新居"},
		"list_project_templates":       {},
		"create_task_from_template":    {"project": "新居", "template": "x"},
		"create_project_from_template": {"template": "x", "name": "y"},
		"duplicate_project":            {"project": "新居"},
	} {
		if msg := toolError(call(t, cs, name, args, nil)); !strings.Contains(msg, "接続できない") {
			t.Errorf("%s: %q", name, msg)
		}
	}
}
