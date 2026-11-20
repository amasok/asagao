package mcpserver

import (
	"context"
	"github.com/amasok/asagao/internal/wire"
	"testing"

	"github.com/amasok/asagao/internal/dates"
)

// create_task reports a bad input in the words it always has: a due date as "due_on: …",
// and a name that does not resolve exactly as the store says (not wrapped with the
// use case's field name).
func TestCreateTaskErrorText(t *testing.T) {
	st := seed(t)
	ctx := context.Background()
	cs := connect(t, &Server{App: wire.UseCases(st)}) // offline
	errText := func(_ string, err error) string {
		if err == nil {
			t.Fatal("expected an error")
		}
		return err.Error()
	}
	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"bad due date", map[string]any{"due_on": "someday"}, "due_on: " + errText(dates.Parse("someday", testNow))},
		{"unknown assignee", map[string]any{"assignee": "誰か"}, errText(st.ResolveUser(ctx, "誰か"))},
		{"unknown assignee in a project", map[string]any{"project": "新居", "assignee": "誰か"}, errText(st.ResolveUser(ctx, "誰か"))},
		{"unknown project", map[string]any{"project": "ない"}, errText(st.ResolveProject(ctx, "ない"))},
		{"unknown section", map[string]any{"project": "新居", "section": "ない"}, errText(st.ResolveSection(ctx, "p1", "ない"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.args["name"] = "x"
			if got := toolError(call(t, cs, "create_task", tc.args, nil)); got != tc.want {
				t.Fatalf("error = %q, want %q", got, tc.want)
			}
		})
	}
}
