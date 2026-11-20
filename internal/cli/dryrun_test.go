package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/auth"
	"github.com/amasok/asagao/internal/store"
	"github.com/amasok/asagao/internal/xdg"
)

// dryRunEnv is writeCLIEnv with a token and an Asana that counts every request (a dry
// run must make none) and answers none of them. TMPDIR is private, so that the copies a
// dry run makes can be checked to be gone.
func dryRunEnv(t *testing.T) (path string, requests *atomic.Int32, run func(args ...string) (string, string, error)) {
	t.Helper()
	path = writeCLIEnv(t)
	t.Setenv(auth.EnvToken, "test-pat")
	t.Setenv("TMPDIR", t.TempDir())
	requests = new(atomic.Int32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "dry run must not reach Asana", http.StatusTeapot)
	}))
	t.Cleanup(srv.Close)
	run = func(args ...string) (string, string, error) {
		cmd := newTestRoot(srv.URL)
		var out, errOut bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&errOut)
		cmd.SetArgs(args)
		err := cmd.ExecuteContext(context.Background())
		return out.String(), errOut.String(), err
	}
	return path, requests, run
}

// storeState is what a write could change in writeCLIEnv's store, as JSON.
func storeState(t *testing.T, path string) string {
	t.Helper()
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	var state []any
	for _, gid := range []string{"t1", "t2"} {
		d, err := st.GetTask(ctx, gid)
		if err != nil {
			t.Fatal(err)
		}
		state = append(state, d)
	}
	ps, err := st.ListProjects(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	ops, err := st.ListOps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(append(state, ps, ops))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// With --dry-run, a write command changes nothing: not the store, not the outbox, not
// Asana, not the attachment cache. It says what it would have done, and leaves no copy.
func TestDryRunChangesNothing(t *testing.T) {
	file := filepath.Join(t.TempDir(), "見積もり.txt")
	if err := os.WriteFile(file, []byte("3 万円"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		args []string
		want string // in what it says it would do
	}{
		{"complete", []string{"task", "complete", "t1"}, "ソファを買う: 完了"},
		{"create", []string{"task", "create", "牛乳を買う", "--project", "新居"}, "タスク「牛乳を買う」の作成"},
		{"create quick", []string{"task", "create", "--quick", "牛乳を買う @me 明日"}, "タスク「牛乳を買う」の作成"},
		{"assign", []string{"task", "assign", "t1", "Partner"}, "ソファを買う"},
		{"move", []string{"task", "move", "t1", "--section", "完了"}, "完了"},
		{"tag", []string{"task", "tag", "t1", "--add", "急ぎ"}, "急ぎ"},
		{"like", []string{"task", "like", "t1"}, "ソファを買う"},
		{"follow", []string{"task", "follow", "t1", "--add", "Partner"}, "Partner"},
		{"field", []string{"task", "field", "t1", "優先度", "高"}, "優先度"},
		{"depend", []string{"task", "depend", "t1", "--add", "t2"}, "カーテンを買う"},
		{"type", []string{"task", "type", "t1", "milestone"}, "ソファを買う"},
		{"duplicate", []string{"task", "duplicate", "t1"}, "ソファを買う"},
		{"attach", []string{"task", "attach", "t1", file}, "見積もり.txt"},
		{"comment", []string{"comment", "add", "t1", "届く"}, "届く"},
		{"section rename", []string{"section", "rename", "家具", "大型家具", "--project", "新居"}, "大型家具"},
		{"section create", []string{"section", "create", "保留", "--project", "新居"}, "セクション「保留」の作成"},
		{"section move", []string{"section", "move", "完了", "--project", "新居", "--before", "家具"}, "セクション「完了」の並べ替え"},
		{"section rm", []string{"section", "rm", "完了", "--project", "新居"}, "セクション「完了」の削除"}, // no --yes needed
		{"tag rename", []string{"tag", "rename", "急ぎ", "至急"}, "名前を「至急」に変更"},
		{"tag color", []string{"tag", "color", "急ぎ", "dark-red"}, "色を dark-red に変更"},
		{"tag color new name", []string{"tag", "color", "急ぎ", "red"}, "色を dark-red に変更"}, // #52
		{"tag rm", []string{"tag", "rm", "急ぎ"}, "タグ「急ぎ」の削除"},
		{"move my tasks", []string{"task", "move", "t1", "--section", "今日やる", "--my-tasks"}, "マイタスクのセクション「今日やる」への移動"},
		{"status", []string{"project", "status", "post", "新居", "--text", "順調"}, "新居"},
		{"project create", []string{"project", "create", "引っ越し"}, "プロジェクト「引っ越し」の作成"},
		{"project edit", []string{"project", "edit", "新居", "--due", "2026-12-31"}, "新居: プロジェクトの期日を 2026-12-31 に変更"},
		{"project edit color new name", []string{"project", "edit", "新居", "--color", "blue"}, "dark-blue"}, // #52
		{"project archive", []string{"project", "archive", "新居"}, "プロジェクト「新居」のアーカイブ"},
		{"project members", []string{"project", "members", "新居", "--add", "Partner", "--remove", "me"}, "メンバー「Partner」を追加"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, requests, run := dryRunEnv(t)
			addMyTaskSections(t, path)
			before := storeState(t, path)
			out, errOut, err := run(append(tc.args, "--dry-run")...)
			if err != nil {
				t.Fatalf("%v\n%s%s", err, out, errOut)
			}
			if !strings.Contains(out, "--dry-run: 次の変更をするところでした") || !strings.Contains(out, tc.want) {
				t.Errorf("should say what it would do (%q):\n%s%s", tc.want, out, errOut)
			}
			if strings.Contains(out, "しました") {
				t.Errorf("should not say it did it:\n%s", out)
			}
			if n := requests.Load(); n != 0 {
				t.Errorf("%d requests to Asana", n)
			}
			if after := storeState(t, path); after != before {
				t.Errorf("the store changed:\nbefore %s\nafter  %s", before, after)
			}
			if _, err := os.Stat(filepath.Join(xdg.CacheDir(), "attachments")); !os.IsNotExist(err) {
				t.Errorf("the attachment cache was written: %v", err)
			}
			if left, _ := os.ReadDir(os.Getenv("TMPDIR")); len(left) != 0 {
				t.Errorf("the dry run left %v", left)
			}
		})
	}
}

// --dry-run checks the command as a real run does: an invalid one fails the same way.
func TestDryRunValidates(t *testing.T) {
	path, _, run := dryRunEnv(t)
	before := storeState(t, path)
	for _, args := range [][]string{
		{"task", "create", "牛乳", "--due", "来週のどこか"},
		{"task", "move", "t1", "--section", "ない"},
		{"task", "assign", "t1", "だれか"},
		{"project", "archive", "ないプロジェクト"},
		{"project", "members", "新居", "--add", "だれか"},
		{"outbox", "discard", "99"},
	} {
		out, errOut, err := run(append(args, "--dry-run")...)
		if err == nil {
			t.Errorf("%v: no error\n%s%s", args, out, errOut)
		}
	}
	if after := storeState(t, path); after != before {
		t.Errorf("the store changed:\nbefore %s\nafter  %s", before, after)
	}
}

// --json: what would be queued, with the outbox kind; no local gid of the copy.
func TestDryRunJSON(t *testing.T) {
	_, _, run := dryRunEnv(t)
	stdout, errOut, err := run("--json", "task", "complete", "t1", "t2", "--dry-run")
	if err != nil {
		t.Fatalf("%v\n%s", err, errOut)
	}
	var r dryRunResult
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("json %q: %v", stdout, err)
	}
	if !r.DryRun || r.TaskGID != "t1,t2" || len(r.Planned) != 2 || r.Planned[0].Kind != store.OpUpdateTask ||
		r.Planned[0].TaskGID != "t1" || r.Planned[0].Payload["completed"] != true || r.Planned[1].TaskGID != "t2" {
		t.Fatalf("result %s", stdout)
	}

	stdout, errOut, err = run("--json", "task", "create", "牛乳", "--dry-run")
	if err != nil {
		t.Fatalf("%v\n%s", err, errOut)
	}
	r = dryRunResult{}
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("json %q: %v", stdout, err)
	}
	if !r.DryRun || r.TaskGID != "" || len(r.Planned) != 1 || r.Planned[0].Kind != store.OpCreateTask || r.Planned[0].TaskGID != "" {
		t.Fatalf("result %s", stdout)
	}
	if strings.Contains(stdout, "local:") {
		t.Errorf("a local gid of the copy: %s", stdout)
	}

	// nothing to do: an empty list, not null
	stdout, _, err = run("--json", "project", "create", "x", "--dry-run")
	if err != nil || !strings.Contains(stdout, `"planned": [`) {
		t.Fatalf("%v %s", err, stdout)
	}
}

// outbox discard / retry / resolve --dry-run: says what it would do to the entry, which
// stays queued.
func TestDryRunOutbox(t *testing.T) {
	path := writeCLIEnv(t)
	t.Setenv("TMPDIR", t.TempDir())
	if out, err := runCLI(t, "comment", "add", "t1", "届く"); err != nil { // offline: queued
		t.Fatalf("%v\n%s", err, out)
	}
	ops := cliOps(t, path)
	if len(ops) != 1 {
		t.Fatalf("ops %v", opKinds(ops))
	}
	id := fmt.Sprint(ops[0].ID)
	out, err := runCLI(t, "outbox", "discard", id, "--dry-run")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "#"+id) || !strings.Contains(out, "届く") || !strings.Contains(out, "の取り消し") {
		t.Errorf("should say it would discard #%s:\n%s", id, out)
	}
	if after := cliOps(t, path); len(after) != 1 || after[0].ID != ops[0].ID {
		t.Errorf("the entry was dropped: %v", opKinds(after))
	}
	// resolving a pending entry fails as it would without --dry-run
	if out, err := runCLI(t, "outbox", "resolve", id, "--keep", "local", "--dry-run"); err == nil {
		t.Errorf("resolve of a pending entry: no error\n%s", out)
	}
}

// A queued change the command would cancel (reopening a task whose completion is still
// queued) is listed.
func TestDryRunCancelled(t *testing.T) {
	path := writeCLIEnv(t)
	if out, err := runCLI(t, "task", "complete", "t1"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	before := cliOps(t, path)
	out, err := runCLI(t, "task", "reopen", "t1", "--dry-run")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "未送信の変更 #1（ソファを買う: ") || !strings.Contains(out, "の取り消し") {
		t.Errorf("output:\n%s", out)
	}
	if after := cliOps(t, path); len(after) != len(before) {
		t.Errorf("outbox changed: %v → %v", opKinds(before), opKinds(after))
	}
}

// Every write command takes --dry-run (so that an agent can try any of them first).
func TestWriteCommandsHaveDryRun(t *testing.T) {
	root := NewRoot(testWiring())
	for _, path := range []string{
		"task create", "task complete", "task reopen", "task move", "task assign", "task tag",
		"task like", "task follow", "task project", "task duplicate", "task attach", "task field",
		"task depend", "task type", "task approve", "comment add", "section rename",
		"project status post", "project create", "project archive", "project members",
		"inbox read", "inbox unread", "outbox resolve", "outbox retry", "outbox discard",
	} {
		cmd, _, err := root.Find(strings.Fields(path))
		if err != nil || cmd.Name() != strings.Fields(path)[len(strings.Fields(path))-1] {
			t.Errorf("%s: not found (%v)", path, err)
			continue
		}
		if cmd.Flags().Lookup("dry-run") == nil {
			t.Errorf("%s has no --dry-run", path)
		}
	}
}

// A copy left behind by a dry run that was stopped (Ctrl-C) is removed by the next dry run
// once it is old; a recent one (another dry run running now) is left alone.
func TestDryRunRemovesStaleCopies(t *testing.T) {
	_, _, run := dryRunEnv(t)
	tmp := os.Getenv("TMPDIR")
	old := filepath.Join(tmp, dryRunPrefix+"old")
	recent := filepath.Join(tmp, dryRunPrefix+"recent")
	other := filepath.Join(tmp, "keep-me")
	for _, d := range []string{old, recent, other} {
		if err := os.MkdirAll(filepath.Join(d, "x"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	long := time.Now().Add(-2 * staleDryRun)
	for _, d := range []string{old, other} {
		if err := os.Chtimes(d, long, long); err != nil {
			t.Fatal(err)
		}
	}
	if out, errOut, err := run("task", "complete", "t1", "--dry-run"); err != nil {
		t.Fatalf("%v\n%s%s", err, out, errOut)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("the old copy is still there: %v", err)
	}
	for _, d := range []string{recent, other} {
		if _, err := os.Stat(d); err != nil {
			t.Errorf("%s was removed: %v", d, err)
		}
	}
}
