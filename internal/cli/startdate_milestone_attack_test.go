package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/store"
)

// #62: a start date on a milestone is refused with the reason from the CLI, nothing queued.
func TestAttackCLIMilestoneStartRefused(t *testing.T) {
	path := writeCLIEnv(t)
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertTasks(context.Background(), []asana.Task{{GID: "m1", Name: "リリース", ResourceSubtype: "milestone",
		DueOn: "2026-12-10", Assignee: &asana.Ref{GID: "me"}, Workspace: &asana.Ref{GID: "w1"}}}); err != nil {
		t.Fatal(err)
	}
	st.Close()

	for _, args := range [][]string{
		{"task", "start", "m1", "2026-12-01"},
		{"task", "start", "m1", "2026-12-01", "--due", "2026-12-10"},
	} {
		out, err := runCLI(t, args...)
		if err == nil || !strings.Contains(err.Error(), "マイルストーン") || !strings.Contains(err.Error(), "開始日") {
			t.Fatalf("%v: err = %v\n%s", args, err, out)
		}
	}
	if ops := cliOps(t, path); len(ops) != 0 {
		t.Fatalf("queued %v", opKinds(ops))
	}
	// clearing is allowed
	if out, err := runCLI(t, "task", "start", "m1", ""); err != nil {
		t.Fatalf("clear: %v\n%s", err, out)
	}
	// a task with a start date is not made a milestone (until the start date goes too)
	if out, err := runCLI(t, "task", "start", "t1", "2026-12-01", "--due", "2026-12-10"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, err := runCLI(t, "task", "type", "t1", "milestone"); err == nil || !strings.Contains(err.Error(), "マイルストーン") {
		t.Fatalf("type milestone on a task with a start date: %v", err)
	}
	// a milestone made first refuses the start date afterwards
	if out, err := runCLI(t, "task", "type", "t2", "milestone"); err != nil {
		t.Fatalf("type: %v\n%s", err, out)
	}
	if _, err := runCLI(t, "task", "start", "t2", "2026-12-02", "--due", "2026-12-10"); err == nil || !strings.Contains(err.Error(), "マイルストーン") {
		t.Fatalf("start date on a task made a milestone locally: %v", err)
	}
}
