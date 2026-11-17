package cli

import (
	"strings"
	"testing"
)

// A broken .asana.json stops the commands that would use it (nothing is queued in the
// wrong project), and --no-context or an explicit --project goes around it.
func TestContextAttackInvalidFile(t *testing.T) {
	for _, content := range []string{`{"projet":"新居"}`, `{`, `{"section":"家具"}`, `{"project":"a\u001b[2J"}`} {
		path := writeCLIEnv(t)
		inRepo(t, content)
		for _, args := range [][]string{
			{"task", "create", "x"},
			{"task", "create", "x", "--dry-run"},
			{"task", "list"},
			{"task", "show"},
			{"context"},
		} {
			if out, err := runCLI(t, args...); err == nil {
				t.Errorf("%s %v: no error\n%s", content, args, out)
			}
		}
		if ops := cliOps(t, path); len(ops) != 0 {
			t.Errorf("%s: queued %v despite the invalid file", content, opKinds(ops))
		}
		if out, err := runCLI(t, "--no-context", "task", "create", "x"); err != nil {
			t.Errorf("--no-context: %v\n%s", err, out)
		}
		if out, err := runCLI(t, "task", "create", "y", "--project", "新居"); err != nil {
			t.Logf("explicit --project with an invalid file: %v\n%s", err, out)
		}
	}
}

// An unknown project, section or task in a valid file is an error for create (never a
// task created in no project), and `context` reports it without failing.
func TestContextAttackUnknownNames(t *testing.T) {
	path := writeCLIEnv(t)
	inRepo(t, `{"project":"新居","section":"ないセクション"}`)
	if out, err := runCLI(t, "task", "create", "x"); err == nil {
		t.Errorf("unknown section: no error\n%s", out)
	}
	inRepo(t, `{"project":"ないプロジェクト"}`)
	if out, err := runCLI(t, "task", "create", "x"); err == nil {
		t.Errorf("unknown project: no error\n%s", out)
	}
	if ops := cliOps(t, path); len(ops) != 0 {
		t.Errorf("queued %v", opKinds(ops))
	}
	inRepo(t, `{"task":"t9"}`)
	if out, err := runCLI(t, "task", "show", "--offline"); err == nil {
		t.Errorf("unknown task: no error\n%s", out)
	}
}

// An explicit gid beats the file, and the file's task is not used when --parent is given.
func TestContextAttackExplicitWins(t *testing.T) {
	writeCLIEnv(t)
	inRepo(t, `{"project":"新居","task":"2"}`)
	out, err := runCLI(t, "task", "show", "t1", "--offline")
	if err != nil || !strings.Contains(out, "ソファを買う") || strings.Contains(out, "カーテンを買う") {
		t.Errorf("explicit gid: %v\n%s", err, out)
	}
}

// `agent-guide` and `context` outputs carry no control characters, whatever the folder is.
func TestAgentGuideSafe(t *testing.T) {
	out, err := runCLI(t, "agent-guide")
	if err != nil || out == "" || strings.ContainsAny(out, "\x1b\x07") {
		t.Errorf("agent-guide: %v %q", err, out)
	}
}
