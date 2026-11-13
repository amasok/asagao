package cli

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asana-tui/internal/asana"
	"github.com/amasok/asana-tui/internal/store"
)

func createStore(t *testing.T) *store.Store {
	t.Helper()
	return createStoreAt(t, filepath.Join(t.TempDir(), "store.db"))
}

// createStoreAt opens (or creates) the store at path with the test fixture.
func createStoreAt(t *testing.T, path string) *store.Store {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.SetSyncState(ctx, store.KeyMeGID, "me"))
	must(st.UpsertUsers(ctx, []asana.User{{GID: "me", Name: "Me"}, {GID: "u2", Name: "Partner"}}))
	must(st.UpsertWorkspaces(ctx, []asana.Workspace{{GID: "w1", Name: "WS"}}))
	must(st.UpsertProjects(ctx, "w1", []asana.Project{{GID: "p1", Name: "新居", Members: []asana.Ref{{GID: "me"}}}}, "me"))
	must(st.ReplaceSections(ctx, "p1", []asana.Section{{GID: "s1", Name: "家具"}}))
	return st
}

func TestCreateTaskAssignee(t *testing.T) {
	now := time.Date(2026, 11, 13, 10, 0, 0, 0, time.Local)
	for _, tc := range []struct {
		name     string
		project  string
		assignee string
		want     string // assignee gid ("" = unassigned)
	}{
		{"My Tasks defaults to me", "", "", "me"},
		{"project defaults to unassigned", "新居", "", ""},
		{"none in My Tasks", "", "none", ""},
		{"none in a project", "新居", "none", ""},
		{"me", "新居", "me", "me"},
		{"exact name", "新居", "Partner", "u2"},
		{"partial name", "", "part", "u2"},
		{"gid", "", "u2", "u2"},
		{"numeric gid not seen yet", "", "1209999", "1209999"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			st := createStore(t)
			gid, err := createTask(ctx, st, createOpts{name: "牛乳を買う", project: tc.project, assignee: tc.assignee}, now)
			if err != nil {
				t.Fatal(err)
			}
			d, err := st.GetTask(ctx, gid)
			if err != nil {
				t.Fatal(err)
			}
			got := ""
			if d.Assignee != nil {
				got = d.Assignee.GID
			}
			if got != tc.want {
				t.Fatalf("assignee = %q, want %q", got, tc.want)
			}
		})
	}
}

// An invalid option fails before anything is written.
func TestCreateTaskInvalidWritesNothing(t *testing.T) {
	now := time.Date(2026, 11, 13, 10, 0, 0, 0, time.Local)
	for _, tc := range []struct {
		name string
		opts createOpts
		want string
		is   error
	}{
		{"unknown assignee", createOpts{assignee: "誰か"}, "--assignee", store.ErrNotFound},
		{"unknown assignee in a project", createOpts{project: "新居", assignee: "誰か"}, "--assignee", store.ErrNotFound},
		{"bad due date", createOpts{due: "someday"}, "--due", nil},
		{"unknown project", createOpts{project: "ない"}, "ない", store.ErrNotFound},
		{"unknown section", createOpts{project: "新居", section: "ない"}, "ない", store.ErrNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			st := createStore(t)
			tc.opts.name = "牛乳を買う"
			_, err := createTask(ctx, st, tc.opts, now)
			if err == nil || !strings.Contains(err.Error(), tc.want) || (tc.is != nil && !errors.Is(err, tc.is)) {
				t.Fatalf("err = %v, want %q (%v)", err, tc.want, tc.is)
			}
			if tasks, _ := st.ListTasks(ctx, store.TaskFilter{}); len(tasks) != 0 {
				t.Fatalf("no task should be created: %+v", tasks)
			}
			if ops, _ := st.ListOps(ctx); len(ops) != 0 {
				t.Fatalf("nothing should be queued: %+v", ops)
			}
		})
	}
}
