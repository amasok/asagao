package mcpserver

import (
	"context"
	"github.com/amasok/asagao/internal/wire"
	"slices"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/store"
)

func opList(t *testing.T, st *store.Store) []string {
	t.Helper()
	ops, err := st.ListOps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, op := range ops {
		out = append(out, string(op.Kind)+":"+op.TaskGID)
	}
	return out
}

// update_task writes the relations in a fixed order (tags, custom fields, dependencies,
// followers, projects), one queued change for each, and a dependent's change goes on the
// task that waits.
func TestUpdateTaskRelationsQueueInOrder(t *testing.T) {
	st := seedFields(t)
	ctx := context.Background()
	_ = st.UpsertUsers(ctx, []asana.User{{GID: "u2", Name: "Partner"}})
	_ = st.UpsertProjects(ctx, "w1", []asana.Project{{GID: "p2", Name: "家計"}}, "me")
	_ = st.ReplaceSections(ctx, "p2", []asana.Section{{GID: "s9", Name: "今月"}})
	cs := connect(t, &Server{App: wire.UseCases(st)})
	var w WriteOutput
	res := call(t, cs, "update_task", map[string]any{
		"gid": "t1", "add_projects": []string{"家計"}, "add_followers": []string{"Partner"},
		"add_dependents": []string{"t3"}, "add_dependencies": []string{"t2"}, "add_tags": []string{"急ぎ"},
		"custom_fields": map[string]any{"見積": 3, "優先度": "高"},
	}, &w)
	if res.IsError {
		t.Fatal(toolError(res))
	}
	want := []string{string(store.OpAddTag) + ":t1", string(store.OpSetCustomField) + ":t1", string(store.OpSetCustomField) + ":t1",
		string(store.OpAddDependency) + ":t1", string(store.OpAddDependency) + ":t3", string(store.OpAddFollower) + ":t1", string(store.OpAddProject) + ":t1"}
	if got := opList(t, st); !slices.Equal(got, want) {
		t.Fatalf("ops = %v, want %v", got, want)
	}
	// removing them again cancels each unsent change
	res = call(t, cs, "update_task", map[string]any{
		"gid": "t1", "remove_projects": []string{"家計"}, "remove_followers": []string{"Partner"},
		"remove_dependents": []string{"t3"}, "remove_dependencies": []string{"t2"},
	}, &w)
	if res.IsError {
		t.Fatal(toolError(res))
	}
	if got := opList(t, st); !slices.Equal(got, want[:3]) {
		t.Errorf("after removing: ops = %v, want %v", got, want[:3])
	}
}

// A task that does not resolve is reported with the argument it came from, and nothing is
// written, not even the relations that did resolve.
func TestUpdateTaskRelationsNameTheArgument(t *testing.T) {
	for _, arg := range []string{"add_dependencies", "remove_dependencies", "add_dependents", "remove_dependents"} {
		t.Run(arg, func(t *testing.T) {
			st := seed(t)
			cs := connect(t, &Server{App: wire.UseCases(st)})
			res := call(t, cs, "update_task", map[string]any{"gid": "t1", "name": "新しい名前", "add_tags": []string{"急ぎ"},
				"add_followers": []string{"me"}, arg: []string{"t2", "no-such-task"}}, nil)
			if msg := toolError(res); !strings.HasPrefix(msg, arg+": ") {
				t.Errorf("error = %q, want it to start with %q", msg, arg+": ")
			}
			if got := opList(t, st); len(got) != 0 {
				t.Errorf("queued %v", got)
			}
			if d, _ := st.GetTask(context.Background(), "t1"); d.Name != "ソファを買う" {
				t.Errorf("name changed to %q", d.Name)
			}
		})
	}
}
