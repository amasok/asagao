package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/domain/task"
	"github.com/amasok/asagao/internal/store"
)

// A wrong positional argument is reported as the parser or the store says it, with no
// field name in front (there is no flag to name), and nothing is queued.
func TestTaskUpdateCommandsErrorWording(t *testing.T) {
	_, errSub := task.ParseSubtype("bug")
	_, errAp := task.ParseApprovalStatus("maybe")
	for _, tc := range []struct {
		args []string
		want string
		not  []string
	}{
		{[]string{"task", "type", "t1", "bug"}, errSub.Error(), []string{"resource_subtype"}},
		{[]string{"task", "approve", "t1", "maybe"}, errAp.Error(), []string{"approval_status"}},
		{[]string{"task", "assign", "t1", "誰か"}, `user "誰か"`, []string{"assignee:"}},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			path := writeCLIEnv(t)
			_, err := runCLI(t, tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			for _, n := range tc.not {
				if strings.Contains(err.Error(), n) {
					t.Errorf("err = %q mentions %q", err, n)
				}
			}
			if strings.HasPrefix(err.Error(), "resource_subtype:") || strings.HasPrefix(err.Error(), "approval_status:") || strings.HasPrefix(err.Error(), "assignee:") {
				t.Errorf("err = %q has a field prefix", err)
			}
			if ops := cliOps(t, path); len(ops) != 0 {
				t.Fatalf("queued %v", opKinds(ops))
			}
		})
	}
}

// approve with no state means approved; on a task that is not an approval it is refused
// before anything is queued, and a bad state on such a task reports the state first.
func TestApproveCommand(t *testing.T) {
	path := writeCLIEnv(t)
	if _, err := runCLI(t, "task", "approve", "t1"); err == nil || !strings.Contains(err.Error(), "承認タスクではありません") {
		t.Fatalf("err = %v", err)
	}
	if _, err := runCLI(t, "task", "approve", "t1", "maybe"); err == nil || strings.Contains(err.Error(), "承認タスクではありません") {
		t.Fatalf("a bad state is reported before the type: %v", err)
	}
	if ops := cliOps(t, path); len(ops) != 0 {
		t.Fatalf("queued %v", opKinds(ops))
	}
	if _, err := runCLI(t, "task", "type", "t1", "approval"); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, "task", "approve", "t1"); err != nil || !strings.Contains(out, "承認済み") {
		t.Fatalf("approve = %q, %v", out, err)
	}
	if out, err := runCLI(t, "task", "approve", "t1", "却下"); err != nil || !strings.Contains(out, "却下") {
		t.Fatalf("approve 却下 = %q, %v", out, err)
	}
	st, _ := store.Open(context.Background(), path)
	defer st.Close()
	d, _ := st.GetTask(context.Background(), "t1")
	if d.ResourceSubtype != "approval" || d.ApprovalStatus != "rejected" {
		t.Fatalf("t1 = %s %s", d.ResourceSubtype, d.ApprovalStatus)
	}
}

// complete with several gids writes one at a time, as before: the ones before an unknown gid
// stay done, and the error is the store's.
func TestCompleteSeveralStopsAtTheFirstError(t *testing.T) {
	path := writeCLIEnv(t)
	_, err := runCLI(t, "task", "complete", "t1", "nope", "t2")
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("err = %v", err)
	}
	ops := cliOps(t, path)
	if len(ops) != 1 || ops[0].TaskGID != "t1" || ops[0].Payload["completed"] != true {
		t.Fatalf("ops = %v", opKinds(ops))
	}
	st, _ := store.Open(context.Background(), path)
	defer st.Close()
	if d, _ := st.GetTask(context.Background(), "t2"); d.Completed {
		t.Error("t2 completed after the error")
	}
}

// reopen, like and unassign go through the same path with the same payload.
func TestSimpleUpdateCommandsPayload(t *testing.T) {
	path := writeCLIEnv(t)
	for _, args := range [][]string{
		{"task", "complete", "t1"}, {"task", "reopen", "t1"}, {"task", "like", "t2"}, {"task", "like", "t2", "--off"}, {"task", "assign", "t2", "none"},
	} {
		if _, err := runCLI(t, args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	st, _ := store.Open(context.Background(), path)
	defer st.Close()
	d, _ := st.GetTask(context.Background(), "t2")
	if d.Assignee != nil || d.Liked {
		t.Fatalf("t2 = %+v", d.TaskSummary)
	}
}
