package mcpserver

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// Whichever relation fails to resolve, nothing is written: not the name, not the tags,
// not the relations that did resolve before it.
func TestUpdateTaskUnresolvedNameWritesNothing(t *testing.T) {
	for _, tc := range []struct {
		arg  string
		args map[string]any
	}{
		{"add_followers", map[string]any{"add_followers": []string{"me", "だれも"}}},
		{"remove_followers", map[string]any{"remove_followers": []string{"だれも"}}},
		{"add_projects", map[string]any{"add_projects": []string{"ない"}}},
		{"remove_projects", map[string]any{"remove_projects": []string{"ない"}}},
		{"custom_fields", map[string]any{"custom_fields": map[string]any{"ないフィールド": "x"}}},
	} {
		t.Run(tc.arg, func(t *testing.T) {
			st := seed(t)
			cs := connect(t, &Server{Store: st})
			args := map[string]any{"gid": "t1", "name": "変えない", "add_tags": []string{"急ぎ"}, "add_dependencies": []string{"t2"}}
			for k, v := range tc.args {
				args[k] = v
			}
			res := call(t, cs, "update_task", args, nil)
			if msg := toolError(res); !strings.HasPrefix(msg, tc.arg+": ") {
				t.Errorf("error = %q, want prefix %q", msg, tc.arg+": ")
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

// A task that waits for itself, or the same change twice, does not crash or queue twice.
func TestUpdateTaskDuplicateAndSelfDependency(t *testing.T) {
	st := seed(t)
	cs := connect(t, &Server{Store: st})
	res := call(t, cs, "update_task", map[string]any{"gid": "t1", "add_dependencies": []string{"t2", "t2"}}, nil)
	if res.IsError {
		t.Logf("duplicate: %s", toolError(res))
	}
	if got := opList(t, st); len(got) > 1 {
		t.Errorf("duplicate dependency queued %v", got)
	}
	res = call(t, cs, "update_task", map[string]any{"gid": "t1", "add_dependencies": []string{"t1"}}, nil)
	t.Logf("self: error=%v %s", res.IsError, toolError(res))
}

// A move or rename that does not resolve writes nothing and leaves the local copy alone.
func TestMoveAndRenameUnresolvedWriteNothing(t *testing.T) {
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"move_task", map[string]any{"gid": "t1", "section": "ない"}},
		{"move_task", map[string]any{"gid": "t1", "section": "手続き", "project": "ない"}},
		{"move_task", map[string]any{"gid": "nope", "section": "手続き"}},
		{"move_task", map[string]any{"gid": "t1", "section": ""}},
		{"rename_section", map[string]any{"project": "ない", "section": "家具", "name": "x"}},
		{"rename_section", map[string]any{"project": "新居", "section": "ない", "name": "x"}},
		{"rename_section", map[string]any{"project": "新居", "section": "家具", "name": ""}},
		{"rename_section", map[string]any{"project": "新居", "section": "家具", "name": "   "}},
	} {
		t.Run(tc.tool+fmt.Sprint(tc.args), func(t *testing.T) {
			st := seed(t)
			cs := connect(t, &Server{Store: st})
			res := call(t, cs, tc.tool, tc.args, nil)
			if !res.IsError {
				t.Errorf("should fail")
			}
			if got := opList(t, st); len(got) != 0 {
				t.Errorf("queued %v", got)
			}
			if n, _ := st.SectionName(context.Background(), "s1"); n != "家具" {
				t.Errorf("s1 = %q", n)
			}
		})
	}
}
