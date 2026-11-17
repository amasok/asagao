package cli

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/store"
)

// createdTask returns the task the first queued create_task made, and the kinds of all
// the queued ops.
func createdTask(t *testing.T, path string) (*store.TaskDetail, []string) {
	t.Helper()
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ops, err := st.ListOps(context.Background())
	if err != nil || len(ops) == 0 || ops[0].Kind != store.OpCreateTask {
		t.Fatalf("ops %+v, err %v", ops, err)
	}
	d, err := st.GetTask(context.Background(), ops[0].TaskGID)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, op := range ops {
		kinds = append(kinds, op.Kind)
	}
	return d, kinds
}

func tagNames(d *store.TaskDetail) []string { return d.Tags }

// --quick reads the assignee, due date and tags at the end of the name, says what it read,
// and queues them with the task.
func TestCreateQuick(t *testing.T) {
	path := writeCLIEnv(t)
	out, err := runCLI(t, "task", "create", "--quick", "牛乳を", "買う", "@partner", "2026-12-01", "#急ぎ")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{"読み取り: 担当: Partner / 期日: 2026-12-01 / タグ: #急ぎ\n", "タスクを作成しました: 牛乳を 買う（"} {
		if !strings.Contains(out, want) {
			t.Errorf("output should say %q:\n%s", want, out)
		}
	}
	d, kinds := createdTask(t, path)
	if d.Name != "牛乳を 買う" || d.DueOn != "2026-12-01" || d.Assignee == nil || d.Assignee.GID != "u2" ||
		!slices.Equal(tagNames(d), []string{"急ぎ"}) {
		t.Errorf("task %+v tags %q", d, tagNames(d))
	}
	if !slices.Equal(kinds, []string{store.OpCreateTask, store.OpAddTag}) {
		t.Errorf("ops %q", kinds)
	}
}

// Without --quick the name is taken as it is (the command does what it did before).
func TestCreateWithoutQuickKeepsName(t *testing.T) {
	path := writeCLIEnv(t)
	out, err := runCLI(t, "task", "create", "牛乳を買う", "@partner", "2026-12-01", "#急ぎ")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.Contains(out, "読み取り") {
		t.Errorf("nothing should be read:\n%s", out)
	}
	d, kinds := createdTask(t, path)
	if d.Name != "牛乳を買う @partner 2026-12-01 #急ぎ" || d.DueOn != "" || d.Assignee == nil || d.Assignee.GID != "me" {
		t.Errorf("task %+v", d)
	}
	if !slices.Equal(kinds, []string{store.OpCreateTask}) {
		t.Errorf("ops %q", kinds)
	}
}

// A token --quick cannot read stays in the name, and the output says so. No tag is made.
func TestCreateQuickUnread(t *testing.T) {
	path := writeCLIEnv(t)
	out, err := runCLI(t, "task", "create", "--quick", "a", "#ない", "@me")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{"読み取り: 担当: 自分\n", "「#ない」は読めませんでした（", "名前に残しました"} {
		if !strings.Contains(out, want) {
			t.Errorf("output should say %q:\n%s", want, out)
		}
	}
	d, kinds := createdTask(t, path)
	if d.Name != "a #ない" || d.Assignee == nil || d.Assignee.GID != "me" || len(d.Tags) != 0 {
		t.Errorf("task %+v", d)
	}
	if !slices.Equal(kinds, []string{store.OpCreateTask}) {
		t.Errorf("ops %q", kinds)
	}
}

// --quick with a flag that says the same thing is an error, and nothing is written.
func TestCreateQuickConflictsWithFlags(t *testing.T) {
	for _, args := range [][]string{
		{"--due", "2026-12-01", "a", "明日"},
		{"--assignee", "none", "a", "@partner"},
	} {
		path := writeCLIEnv(t)
		out, err := runCLI(t, append([]string{"task", "create", "--quick"}, args...)...)
		if err == nil {
			t.Errorf("%q: should fail:\n%s", args, out)
		}
		if ops := cliOps(t, path); len(ops) != 0 {
			t.Errorf("%q: nothing should be queued: %+v", args, ops)
		}
	}
	// the flags alone (no token for them) are fine together with --quick
	path := writeCLIEnv(t)
	if out, err := runCLI(t, "task", "create", "--quick", "--due", "2026-12-01", "--assignee", "Partner", "a", "#急ぎ"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if d, _ := createdTask(t, path); d.DueOn != "2026-12-01" || d.Assignee == nil || d.Assignee.GID != "u2" || !slices.Equal(tagNames(d), []string{"急ぎ"}) {
		t.Errorf("task %+v", d)
	}
}

// --json says what --quick read, in the result (no extra lines that would break the JSON).
func TestCreateQuickJSON(t *testing.T) {
	writeCLIEnv(t)
	stdout, _, err := runCLISplit(t, "--json", "task", "create", "--quick", "a", "#x", "@me", "#急ぎ")
	if err != nil {
		t.Fatalf("%v\n%s", err, stdout)
	}
	var r struct {
		Quick *quickAddJSON `json:"quick"`
	}
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("%v\n%s", err, stdout)
	}
	want := quickAddJSON{Name: "a #x", Assignee: "me", Tags: []string{"g1"}, Unread: "#x"}
	if r.Quick == nil || r.Quick.Name != want.Name || r.Quick.Assignee != want.Assignee ||
		!slices.Equal(r.Quick.Tags, want.Tags) || r.Quick.Unread != want.Unread || r.Quick.UnreadReason == "" {
		t.Errorf("quick %+v", r.Quick)
	}
}
