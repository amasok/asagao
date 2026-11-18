package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/store"
)

const cliBriefTable = `<table><tr><td>予算</td><td>100万</td></tr></table>`

// briefCLIEnv is projectCLIEnv with p1's brief (nil: none).
func briefCLIEnv(t *testing.T, brief *asana.ProjectBrief) string {
	t.Helper()
	path := projectCLIEnv(t)
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	state := store.BriefNone
	if brief != nil {
		state = store.BriefAvailable
	}
	if err := st.SaveProjectOverview(context.Background(), "p1", nil, state, brief, time.Now()); err != nil {
		t.Fatal(err)
	}
	return path
}

// #60: `project brief` prints the brief as editable Markdown (its table as an asana-html
// block), and --file with that output changes nothing; an edited file is queued with the
// table as it was.
func TestProjectBriefShowAndFile(t *testing.T) {
	path := briefCLIEnv(t, &asana.ProjectBrief{GID: "b1", Title: "目的", Text: "年内",
		HTMLText: "<body><h2>計画</h2>年内\n" + cliBriefTable + "</body>"})
	stdout, stderr, err := runCLISplit(t, "project", "brief", "新居", "--offline")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "## 計画") || !strings.Contains(stdout, "```asana-html\n"+cliBriefTable+"\n```") || !strings.Contains(stderr, "タイトル: 目的") {
		t.Fatalf("stdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	file := filepath.Join(t.TempDir(), "brief.md")
	if err := os.WriteFile(file, []byte(stdout), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, "project", "brief", "新居", "--offline", "--file", file); err != nil || !strings.Contains(out, "変更はありません") {
		t.Fatalf("unchanged: %v\n%s", err, out)
	}
	if ops := cliOps(t, path); len(ops) != 0 {
		t.Fatalf("queued without a change: %+v", ops)
	}
	if err := os.WriteFile(file, []byte(strings.Replace(stdout, "年内", "春まで", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, "project", "brief", "新居", "--offline", "--file", file, "--title", "新しい目的"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	ops := cliOps(t, path)
	if len(ops) != 1 || ops[0].Kind != store.OpUpdateBrief || ops[0].Payload["title"] != "新しい目的" {
		t.Fatalf("ops = %+v", ops)
	}
	if html, _ := ops[0].Payload["html_text"].(string); !strings.Contains(html, cliBriefTable) || !strings.Contains(html, "春まで") {
		t.Fatalf("html = %q", html)
	}
	// --json
	stdout, _, err = runCLISplit(t, "--json", "project", "brief", "新居", "--offline")
	var b briefJSON
	if err != nil || json.Unmarshal([]byte(stdout), &b) != nil || b.Title != "新しい目的" || !b.Pending || b.State != "available" {
		t.Fatalf("--json: %v %s", err, stdout)
	}
}

// A project without a brief gets one from standard input; rm deletes it (the queued
// create is dropped: nothing to send).
func TestProjectBriefCreateAndRm(t *testing.T) {
	path := briefCLIEnv(t, nil)
	cmd := newRoot(&app{baseURL: unreachableAsana(t)})
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader("**大事**なこと\n"))
	cmd.SetArgs([]string{"project", "brief", "新居", "--offline", "--title", "概要", "--file", "-"})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	ops := cliOps(t, path)
	if len(ops) != 1 || ops[0].Payload["html_text"] != "<body><strong>大事</strong>なこと</body>" {
		t.Fatalf("ops = %+v", ops)
	}
	if out, err := runCLI(t, "project", "brief", "rm", "新居"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if ops := cliOps(t, path); len(ops) != 0 {
		t.Fatalf("left %+v", ops)
	}
	if _, err := runCLI(t, "project", "brief", "rm", "新居"); err == nil {
		t.Fatal("deleted a brief that is not there")
	}
}

// Refused: a broken asana-html block, a brief Asana does not give, --file with --edit.
func TestProjectBriefRefuses(t *testing.T) {
	path := briefCLIEnv(t, &asana.ProjectBrief{GID: "b1", Title: "t", Text: "x", HTMLText: "<body>x</body>"})
	file := filepath.Join(t.TempDir(), "b.md")
	_ = os.WriteFile(file, []byte("```asana-html\n<table><tr>\n```\n"), 0o600)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"project", "brief", "新居", "--offline", "--file", file}, "asana-html"},
		{[]string{"project", "brief", "新居", "--offline", "--file", file, "--edit"}, "--edit"},
		{[]string{"project", "brief", "仕事", "--offline", "--title", "x"}, ""}, // not fetched: a create, allowed
	} {
		_, err := runCLI(t, tc.args...)
		if tc.want == "" {
			if err != nil {
				t.Errorf("%v: %v", tc.args, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: err = %v, want %q", tc.args, err, tc.want)
		}
	}
	if ops := cliOps(t, path); len(ops) != 1 || ops[0].TaskGID != "p2" {
		t.Fatalf("ops = %+v", ops)
	}
	// p1 of the fixture's unavailable brief
	st, _ := store.Open(context.Background(), path)
	_ = st.SaveProjectOverview(context.Background(), "p1", nil, store.BriefUnavailable, nil, time.Now())
	st.Close()
	if _, err := runCLI(t, "project", "brief", "新居", "--offline", "--title", "y"); err == nil || !strings.Contains(err.Error(), "ブリーフ") {
		t.Fatalf("unavailable: %v", err)
	}
}
