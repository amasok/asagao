package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/store"
)

// inRepo makes a repository folder with .asana.json holding content (none when empty) and
// a subfolder, and runs the CLI from that subfolder.
func inRepo(t *testing.T, content string) string {
	t.Helper()
	repo := t.TempDir()
	sub := filepath.Join(repo, "src", "pkg")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if content != "" {
		if err := os.WriteFile(filepath.Join(repo, ".asana.json"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := getwd
	getwd = func() (string, error) { return sub, nil }
	t.Cleanup(func() { getwd = old })
	return filepath.Join(repo, ".asana.json")
}

// task create without --project creates the task in .asana.json's project and section,
// and says so on the error output.
func TestContextTaskCreate(t *testing.T) {
	path := writeCLIEnv(t)
	file := inRepo(t, `{"project": "新居", "section": "完了"}`)
	stdout, stderr, err := runCLISplit(t, "task", "create", "棚を買う")
	if err != nil {
		t.Fatalf("%v\n%s%s", err, stdout, stderr)
	}
	if !strings.Contains(stderr, file) {
		t.Errorf("should say .asana.json was used:\n%s", stderr)
	}
	ops := cliOps(t, path)
	if len(ops) != 1 || ops[0].Kind != store.OpCreateTask || ops[0].Payload["project"] != "p1" || ops[0].Payload["section"] != "s2" {
		t.Fatalf("ops %+v", ops)
	}
	// --section alone: in .asana.json's project
	if out, err := runCLI(t, "task", "create", "椅子を買う", "--section", "家具"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if ops := cliOps(t, path); len(ops) != 2 || ops[1].Payload["project"] != "p1" || ops[1].Payload["section"] != "s1" {
		t.Fatalf("ops %+v", ops)
	}
	// --no-context: as without the file (assigned to me, in no project)
	if out, err := runCLI(t, "--no-context", "task", "create", "電球を買う"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if ops := cliOps(t, path); len(ops) != 3 || ops[2].Payload["project"] != nil && ops[2].Payload["project"] != "" {
		t.Fatalf("--no-context: ops %+v", ops[2])
	}
	// --project "": as without the file
	if out, err := runCLI(t, "task", "create", "鍵を買う", "--project", ""); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if ops := cliOps(t, path); len(ops) != 4 || ops[3].Payload["project"] != nil && ops[3].Payload["project"] != "" {
		t.Fatalf("--project \"\": ops %+v", ops[3])
	}
	// --parent: a subtask, not in the project
	if out, err := runCLI(t, "task", "create", "脚", "--parent", "t1"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if ops := cliOps(t, path); len(ops) != 5 || ops[4].Payload["parent"] != "t1" {
		t.Fatalf("--parent: ops %+v", ops[4])
	}
}

// task list without --project lists .asana.json's project (everyone's tasks in it);
// --project "" lists as without the file.
func TestContextTaskList(t *testing.T) {
	writeCLIEnv(t)
	inRepo(t, `{"project": "p1"}`)
	if out, err := runCLI(t, "task", "create", "電球を買う", "--no-context"); err != nil { // mine, in no project
		t.Fatalf("%v\n%s", err, out)
	}
	stdout, stderr, err := runCLISplit(t, "--json", "task", "list")
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	var tasks []struct{ Name string }
	if err := json.Unmarshal([]byte(stdout), &tasks); err != nil {
		t.Fatalf("%v: %s", err, stdout)
	}
	if len(tasks) != 2 || !strings.Contains(stderr, ".asana.json") {
		t.Errorf("tasks %+v\n%s", tasks, stderr)
	}
	out, err := runCLI(t, "task", "list", "--project", "")
	if err != nil || !strings.Contains(out, "電球を買う") {
		t.Errorf("--project \"\": %v\n%s", err, out)
	}
}

// task show without a gid shows .asana.json's task; context shows what the file names.
func TestContextShowAndContextCommand(t *testing.T) {
	path := writeCLIEnv(t)
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertTasks(context.Background(), []asana.Task{{GID: "1002", Name: "照明を買う", Workspace: &asana.Ref{GID: "w1"}}}); err != nil {
		t.Fatal(err)
	}
	st.Close()
	file := inRepo(t, `{"project": "新居", "section": "家", "task": "1002"}`)
	out, err := runCLI(t, "task", "show", "--offline")
	if err != nil || !strings.Contains(out, "照明を買う") {
		t.Fatalf("show: %v\n%s", err, out)
	}
	out, err = runCLI(t, "context")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{file, "プロジェクト: 新居 (p1)", "セクション: 家具 (s1)", "タスク: 照明を買う (1002)  [未完了]"} {
		if !strings.Contains(out, want) {
			t.Errorf("context lacks %q:\n%s", want, out)
		}
	}
	stdout, _, err := runCLISplit(t, "--json", "context")
	if err != nil {
		t.Fatal(err)
	}
	var w struct {
		Found   bool
		Path    string
		Project struct{ GID, Name string }
		Task    struct {
			GID       string
			Completed bool
		}
	}
	if err := json.Unmarshal([]byte(stdout), &w); err != nil || !w.Found || w.Path != file || w.Project.GID != "p1" || w.Task.GID != "1002" {
		t.Errorf("json %v %s", err, stdout)
	}
}

// What the cache does not have is shown with the reason, not an error.
func TestContextNotInCache(t *testing.T) {
	writeCLIEnv(t)
	inRepo(t, `{"project": "ない", "task": "9999"}`)
	out, err := runCLI(t, "context")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "プロジェクト: ない（") || !strings.Contains(out, "タスク: 9999（") {
		t.Errorf("context:\n%s", out)
	}
	// task create into a project that is not there fails, writing nothing
	if out, err := runCLI(t, "task", "create", "x"); err == nil {
		t.Errorf("no error:\n%s", out)
	}
}

// No file: context says so; task show needs a gid.
func TestContextNone(t *testing.T) {
	writeCLIEnv(t)
	inRepo(t, "")
	if out, err := runCLI(t, "context"); err != nil || !strings.Contains(out, "ありません") {
		t.Errorf("%v\n%s", err, out)
	}
	if stdout, _, err := runCLISplit(t, "--json", "context"); err != nil || strings.TrimSpace(stdout) != "{\n  \"found\": false\n}" {
		t.Errorf("%v %q", err, stdout)
	}
	if _, err := runCLI(t, "task", "show"); err == nil || !strings.Contains(err.Error(), "gid") {
		t.Errorf("show without gid: %v", err)
	}
}

// An invalid .asana.json stops the commands that use it, rather than working on another
// project than the one written down; --no-context goes on without it.
func TestContextInvalid(t *testing.T) {
	path := writeCLIEnv(t)
	inRepo(t, `{"projet": "新居"}`)
	for _, args := range [][]string{{"task", "create", "x"}, {"task", "list"}, {"context"}} {
		if out, err := runCLI(t, args...); err == nil || !strings.Contains(err.Error(), ".asana.json") {
			t.Errorf("%v: %v\n%s", args, err, out)
		}
	}
	if len(cliOps(t, path)) != 0 {
		t.Error("queued a change")
	}
	if out, err := runCLI(t, "--no-context", "task", "list"); err != nil {
		t.Errorf("--no-context: %v\n%s", err, out)
	}
}

// The guide printed by agent-guide is the one in README.md.
func TestAgentGuideInReadme(t *testing.T) {
	out, err := runCLI(t, "agent-guide")
	if err != nil || out != agentGuide {
		t.Fatalf("%v\n%s", err, out)
	}
	readme, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(readme), agentGuide) {
		t.Error("README.md does not have the text of agent-guide (update it from `asagao agent-guide`)")
	}
	for _, cmd := range []string{"context --json", "task list --json", "--dry-run", "comment add"} {
		if !strings.Contains(agentGuide, cmd) {
			t.Errorf("the guide lacks %q", cmd)
		}
	}
}
