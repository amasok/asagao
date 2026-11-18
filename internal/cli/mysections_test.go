package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/store"
)

func addMyTaskSections(t *testing.T, path string) {
	t.Helper()
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.ReplaceMyTaskSections(context.Background(), "w1", []asana.Section{{GID: "m1", Name: "今日やる"}, {GID: "m2", Name: "あとで"}}); err != nil {
		t.Fatal(err)
	}
}

// task move --my-tasks moves within the sections of My Tasks; section mine lists them.
func TestTaskMoveMyTasks(t *testing.T) {
	path := writeCLIEnv(t)
	addMyTaskSections(t, path)
	if out, err := runCLI(t, "section", "mine"); err != nil || !strings.Contains(out, "今日やる") || !strings.Contains(out, "あとで") {
		t.Fatalf("section mine: %v\n%s", err, out)
	}
	for _, args := range [][]string{
		{"task", "move", "t1", "--section", "ない", "--my-tasks"},
		{"task", "move", "t1", "--section", "今日やる", "--my-tasks", "--project", "新居"},
	} {
		if out, err := runCLI(t, args...); err == nil {
			t.Errorf("%v: no error\n%s", args, out)
		}
	}
	if ops := cliOps(t, path); len(ops) != 0 {
		t.Fatalf("queued %v", opKinds(ops))
	}
	out, err := runCLI(t, "task", "move", "t1", "--section", "今日やる", "--my-tasks")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	ops := cliOps(t, path)
	if len(ops) != 1 || ops[0].Kind != store.OpMoveMySection || ops[0].TaskGID != "t1" || ops[0].Payload["section"] != "m1" {
		t.Fatalf("ops = %v", opKinds(ops))
	}
}
