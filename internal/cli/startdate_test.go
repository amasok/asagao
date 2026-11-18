package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/store"
)

// #57: `task start` and `task create --start` set a task's start date, queued offline.

func TestTaskStartCommand(t *testing.T) {
	path := writeCLIEnv(t) // t1 has no due date
	out, err := runCLI(t, "task", "start", "t1", "2026-12-01", "--due", "2026-12-10")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "開始日を 2026-12-01 にしました") || !strings.Contains(out, "期日 2026-12-10") || !strings.Contains(out, "未送信") {
		t.Errorf("output:\n%s", out)
	}
	ops := cliOps(t, path)
	if len(ops) != 1 || ops[0].Kind != store.OpUpdateTask || ops[0].Payload["start_on"] != "2026-12-01" || ops[0].Payload["due_on"] != "2026-12-10" {
		t.Fatalf("ops = %+v", ops)
	}
	if out, _ := runCLI(t, "task", "show", "t1"); !strings.Contains(out, "2026-12-01") {
		t.Errorf("task show lacks the start date:\n%s", out)
	}
	// Clearing the due date alone is refused while the start date is there.
	if out, err := runCLI(t, "task", "start", "t1", "2026-12-01", "--due", ""); err == nil || !strings.Contains(err.Error(), "期日だけを消す") {
		t.Errorf("clearing the due date: %v\n%s", err, out)
	}
	// "" clears the start date.
	if out, err := runCLI(t, "task", "start", "t1", ""); err != nil || !strings.Contains(out, "開始日を外しました") {
		t.Fatalf("clear: %v\n%s", err, out)
	}
}

func TestTaskStartRefused(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"task", "start", "t1", "2026-12-01"}, "開始日を付けるには期日も必要です"},
		{[]string{"task", "start", "t1", "2026-12-11", "--due", "2026-12-10"}, "より後です"},
		{[]string{"task", "start", "t1", "そのうち", "--due", "2026-12-10"}, "そのうち"},
		{[]string{"task", "start", "t1", "2026-12-01", "--due", "そのうち"}, "--due:"},
		{[]string{"task", "start", "no-such-task", "2026-12-01", "--due", "2026-12-10"}, ""},
		{[]string{"task", "create", "x", "--start", "2026-12-01"}, "--start: 開始日を付けるには期日も必要です"},
		{[]string{"task", "create", "x", "--start", "そのうち", "--due", "2026-12-10"}, "--start:"},
		{[]string{"task", "create", "x", "--start", "2026-12-11", "--due", "2026-12-10"}, "--start:"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			path := writeCLIEnv(t)
			out, err := runCLI(t, tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q\n%s", err, tc.want, out)
			}
			if strings.HasPrefix(err.Error(), "start_on:") {
				t.Errorf("the use case's field name leaked: %v", err)
			}
			if ops := cliOps(t, path); len(ops) != 0 {
				t.Fatalf("queued %v", opKinds(ops))
			}
		})
	}
}

func TestTaskCreateWithStart(t *testing.T) {
	path := writeCLIEnv(t)
	if out, err := runCLI(t, "task", "create", "設計", "--project", "新居", "--start", "2026-12-01", "--due", "2026-12-10"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	ops := cliOps(t, path)
	if len(ops) != 1 || ops[0].Kind != store.OpCreateTask || ops[0].Payload["start_on"] != "2026-12-01" {
		t.Fatalf("ops = %+v", ops)
	}
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if d, _ := st.GetTask(context.Background(), ops[0].TaskGID); d == nil || d.StartOn != "2026-12-01" {
		t.Fatalf("local task: %+v", d)
	}
}
