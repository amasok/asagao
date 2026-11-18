package cli

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/store"
)

func cliStoreOps(t *testing.T, path string) []store.Op {
	t.Helper()
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ops, err := st.ListOps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return ops
}

// #32: task rm asks for --yes; --dry-run shows the delete without it and writes nothing.
func TestTaskRemoveConfirms(t *testing.T) {
	path := writeCLIEnv(t)
	out, err := runCLI(t, "task", "rm", "t1")
	if err == nil || !strings.Contains(err.Error(), "--yes") || !strings.Contains(err.Error(), "ソファを買う") {
		t.Fatalf("without --yes: %v\n%s", err, out)
	}
	if ops := cliStoreOps(t, path); len(ops) != 0 {
		t.Fatalf("queued without --yes: %v", opKinds(ops))
	}
	out, err = runCLI(t, "task", "rm", "t1", "--dry-run")
	if err != nil || !strings.Contains(out, "タスクの削除") {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	if ops := cliStoreOps(t, path); len(ops) != 0 {
		t.Fatalf("queued by a dry run: %v", opKinds(ops))
	}
	out, err = runCLI(t, "task", "rm", "t1", "--yes")
	if err != nil || !strings.Contains(out, "未送信") {
		t.Fatalf("rm --yes: %v\n%s", err, out)
	}
	if got := opKinds(cliStoreOps(t, path)); !slices.Equal(got, []string{"delete_task:t1"}) {
		t.Fatalf("outbox %v", got)
	}
	out, err = runCLI(t, "task", "list", "--assignee", "any")
	if err != nil || strings.Contains(out, "ソファを買う") || !strings.Contains(out, "カーテンを買う") {
		t.Errorf("list after rm: %v\n%s", err, out)
	}
}

func TestTaskParentAndMoveToAPlace(t *testing.T) {
	path := writeCLIEnv(t)
	for _, bad := range [][]string{
		{"task", "parent", "t2"},
		{"task", "parent", "t2", "t1", "--none"},
		{"task", "parent", "t2", "t2"},
		{"task", "move", "t1"},
		{"task", "move", "t1", "--before", "t2", "--after", "t2"},
	} {
		if out, err := runCLI(t, bad...); err == nil {
			t.Errorf("%v: no error\n%s", bad, out)
		}
	}
	if ops := cliStoreOps(t, path); len(ops) != 0 {
		t.Fatalf("queued by a refused command: %v", opKinds(ops))
	}
	if out, err := runCLI(t, "task", "parent", "t2", "t1"); err != nil || !strings.Contains(out, "親タスクを付け替えました") {
		t.Fatalf("parent: %v\n%s", err, out)
	}
	if out, err := runCLI(t, "task", "move", "t1", "--after", "t2"); err != nil || !strings.Contains(out, "並び順を変えました") {
		t.Fatalf("move --after: %v\n%s", err, out)
	}
	ops := cliStoreOps(t, path)
	if got := opKinds(ops); !slices.Equal(got, []string{"set_parent:t2", "move_task:t1"}) {
		t.Fatalf("outbox %v", got)
	}
	if ops[0].Payload["parent"] != "t1" || ops[1].Payload["insert_after"] != "t2" || ops[1].Payload["section"] != "s1" {
		t.Errorf("payloads %v / %v", ops[0].Payload, ops[1].Payload)
	}
	// back to a top-level task: nothing left to send
	if out, err := runCLI(t, "task", "parent", "t2", "--none"); err != nil || !strings.Contains(out, "親タスクから外しました") {
		t.Fatalf("parent --none: %v\n%s", err, out)
	}
	if got := opKinds(cliStoreOps(t, path)); !slices.Equal(got, []string{"move_task:t1"}) {
		t.Errorf("outbox %v", got)
	}
}
