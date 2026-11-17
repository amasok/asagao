package cli

import (
	"context"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/dates"
)

// task create names the flag of a bad input once ("--due: …", "--assignee: …") followed
// by the error itself, and reports a name that does not resolve as the store says.
func TestCreateTaskErrorText(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 13, 10, 0, 0, 0, time.Local)
	st := createStore(t)
	errText := func(_ string, err error) string {
		if err == nil {
			t.Fatal("expected an error")
		}
		return err.Error()
	}
	for _, tc := range []struct {
		name string
		opts createOpts
		want string
	}{
		{"bad due date", createOpts{due: "someday"}, "--due: " + errText(dates.Parse("someday", now))},
		{"unknown assignee", createOpts{assignee: "誰か"}, "--assignee: " + errText(st.ResolveUser(ctx, "誰か"))},
		{"unknown assignee in a project", createOpts{project: "新居", assignee: "誰か"}, "--assignee: " + errText(st.ResolveUser(ctx, "誰か"))},
		{"unknown project", createOpts{project: "ない"}, errText(st.ResolveProject(ctx, "ない"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.opts.name = "牛乳を買う"
			if _, err := createTask(ctx, st, tc.opts, now); err == nil || err.Error() != tc.want {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}
