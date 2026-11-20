package mcpserver

import (
	"context"
	"github.com/amasok/asagao/internal/wire"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/store"
)

// repoWith makes a repository folder with .asana.json holding content (none when empty)
// and returns a subfolder of it.
func repoWith(t *testing.T, content string) (dir, file string) {
	t.Helper()
	repo := t.TempDir()
	dir = filepath.Join(repo, "cmd")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	file = filepath.Join(repo, ".asana.json")
	if content != "" {
		if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir, file
}

// seedNumeric is seed with a task whose gid is digits (as .asana.json's task must be).
func seedNumeric(t *testing.T) *store.Store {
	t.Helper()
	st := seed(t)
	if err := st.UpsertTasks(context.Background(), []asana.Task{{GID: "1002", Name: "住民票を移す", DueOn: "2026-11-10",
		Workspace: &asana.Ref{GID: "w1"}, Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}, Section: &asana.Ref{GID: "s2"}}}}}); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestGetContext(t *testing.T) {
	dir, file := repoWith(t, `{"project": "新居", "section": "手続き", "task": "1002"}`)
	cs := connect(t, &Server{App: wire.UseCases(seedNumeric(t)), ContextDir: dir})
	var out ContextOutput
	if res := call(t, cs, "get_context", nil, &out); res.IsError {
		t.Fatalf("error: %s", toolError(res))
	}
	if !out.Found || out.Path != file || out.Project == nil || out.Project.GID != "p1" || out.Project.Name != "[家族]新居プロジェクト" ||
		out.Section == nil || out.Section.GID != "s2" || out.Task == nil || out.Task.Name != "住民票を移す" || out.Task.DueOn != "2026-11-10" {
		t.Fatalf("context %+v %+v %+v", out.Project, out.Section, out.Task)
	}
}

func TestGetContextNone(t *testing.T) {
	dir, _ := repoWith(t, "")
	for _, d := range []string{dir, ""} { // no file; no folder given (asagao mcp --no-context)
		cs := connect(t, &Server{App: wire.UseCases(seed(t)), ContextDir: d})
		var out ContextOutput
		if res := call(t, cs, "get_context", nil, &out); res.IsError || out.Found || out.Path != "" {
			t.Errorf("%q: %+v %s", d, out, toolError(res))
		}
	}
}

func TestGetContextInvalid(t *testing.T) {
	dir, _ := repoWith(t, `{"project": "新居", "tsk": "t1"}`)
	cs := connect(t, &Server{App: wire.UseCases(seed(t)), ContextDir: dir})
	res := call(t, cs, "get_context", nil, nil)
	if !res.IsError || !strings.Contains(toolError(res), ".asana.json") {
		t.Errorf("an invalid file should be an error: %s", toolError(res))
	}
}

func getPrompt(t *testing.T, cs *mcp.ClientSession, name string, args map[string]string) (string, error) {
	t.Helper()
	res, err := cs.GetPrompt(context.Background(), &mcp.GetPromptParams{Name: name, Arguments: args})
	if err != nil {
		return "", err
	}
	if len(res.Messages) != 1 || res.Messages[0].Role != "user" {
		t.Fatalf("messages %+v", res.Messages)
	}
	return res.Messages[0].Content.(*mcp.TextContent).Text, nil
}

func TestPromptsListed(t *testing.T) {
	cs := connect(t, &Server{App: wire.UseCases(seed(t))})
	res, err := cs.ListPrompts(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range res.Prompts {
		got = append(got, p.Name)
		if len(p.Arguments) != 1 || p.Arguments[0].Name != "project" || p.Arguments[0].Required {
			t.Errorf("%s arguments %+v", p.Name, p.Arguments)
		}
	}
	if strings.Join(got, ",") != "standup,triage_overdue,weekly_review" {
		t.Errorf("prompts %v", got)
	}
}

// Without a project the prompts work on my tasks; with one (the argument, or .asana.json's)
// on that project, by gid; .asana.json's task is named.
func TestPromptScope(t *testing.T) {
	cs := connect(t, &Server{App: wire.UseCases(seed(t))})
	text, err := getPrompt(t, cs, "standup", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "今日は 2026-11-13") || !strings.Contains(text, `assignee: "me"`) || strings.Contains(text, "project:") {
		t.Errorf("standup for my tasks:\n%s", text)
	}
	text, err = getPrompt(t, cs, "triage_overdue", map[string]string{"project": "新居"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, `project: "p1"`) || !strings.Contains(text, "[家族]新居プロジェクト") || !strings.Contains(text, "overdue: true") {
		t.Errorf("triage for a project:\n%s", text)
	}
	if _, err := getPrompt(t, cs, "weekly_review", map[string]string{"project": "ないプロジェクト"}); err == nil {
		t.Error("an unknown project should be an error")
	}

	dir, file := repoWith(t, `{"project": "p1", "task": "1002"}`)
	cs = connect(t, &Server{App: wire.UseCases(seedNumeric(t)), ContextDir: dir})
	text, err = getPrompt(t, cs, "weekly_review", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`project: "p1"`, file, "「住民票を移す」（gid 1002）", "create_status_update"} {
		if !strings.Contains(text, want) {
			t.Errorf("weekly review lacks %q:\n%s", want, text)
		}
	}
	// the argument wins over .asana.json
	if text, _ := getPrompt(t, cs, "standup", map[string]string{"project": "p1"}); strings.Contains(text, file+" で指定") {
		t.Errorf("an explicit project said to come from the file:\n%s", text)
	}
}

// A read-only server's prompts only propose changes.
func TestPromptsReadOnly(t *testing.T) {
	for _, ro := range []bool{false, true} {
		cs := connect(t, &Server{App: wire.UseCases(seed(t)), ReadOnly: ro})
		text, err := getPrompt(t, cs, "triage_overdue", nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(text, "読み取り専用"); got != ro {
			t.Errorf("read-only %v:\n%s", ro, text)
		}
		if got := strings.Contains(text, "了承を得てから"); got == ro {
			t.Errorf("read-only %v: should ask before writing:\n%s", ro, text)
		}
	}
}
