package mcpserver

import (
	"context"
	"fmt"
	"testing"

	"github.com/amasok/asana-tui/internal/asana"
)

// A client cannot ask for more than maxLimit rows, and a huge modified_within_days works
// (it means "all"). (#41)
func TestReadToolLimitsAreCapped(t *testing.T) {
	st := seed(t)
	ctx := context.Background()
	tasks := make([]asana.Task, maxLimit+50)
	for i := range tasks {
		tasks[i] = asana.Task{GID: fmt.Sprint(500000 + i), Name: fmt.Sprintf("まとめ %04d", i),
			Assignee: &asana.Ref{GID: "me"}, Workspace: &asana.Ref{GID: "w1"}, ModifiedAt: testNow}
	}
	if err := st.UpsertTasks(ctx, tasks); err != nil {
		t.Fatal(err)
	}
	cs := connect(t, &Server{Store: st})
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"list_tasks", map[string]any{"limit": 1000000}},
		{"search_tasks", map[string]any{"query": "まとめ", "limit": 1000000}},
		{"list_tasks", map[string]any{"limit": 1000000, "modified_within_days": 1 << 40}},
	} {
		var out TasksOutput
		res := call(t, cs, tc.tool, tc.args, &out)
		if res.IsError {
			t.Fatalf("%s %v: %s", tc.tool, tc.args, toolError(res))
		}
		if out.Count != maxLimit {
			t.Fatalf("%s %v: %d tasks, want %d", tc.tool, tc.args, out.Count, maxLimit)
		}
	}
	if got := clampLimit(0, 50); got != 50 {
		t.Fatalf("default limit = %d", got)
	}
	if got := clampLimit(-3, 50); got != 50 {
		t.Fatalf("negative limit = %d", got)
	}
}
