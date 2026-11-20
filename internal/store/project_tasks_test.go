package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/readmodel"
)

// #54: which projects' tasks are cached.
func TestProjectTaskStates(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	me := []asana.Ref{{GID: "me"}}
	must(st.UpsertWorkspaces(ctx, []asana.Workspace{{GID: "w1", Name: "A"}}))
	must(st.UpsertProjects(ctx, "w1", []asana.Project{
		{GID: "1", Name: "opened", Members: me}, {GID: "2", Name: "before", Members: me},
		{GID: "3", Name: "fav", Members: me}, {GID: "4", Name: "listed", Members: me}, {GID: "5", Name: "other"},
	}, "me"))
	must(st.ReplaceFavoriteProjects(ctx, "w1", []asana.Ref{{GID: "3"}}))
	must(st.SetSyncState(ctx, keyEvents+"2", "T")) // synced before #54

	if s, err := st.WantProject(ctx, "1"); err != nil || s != readmodel.TasksWanted {
		t.Fatalf("want 1: %q %v", s, err)
	}
	// Wanting a project fetched already keeps its state.
	if s, err := st.WantProject(ctx, "2"); err != nil || s != readmodel.TasksAll {
		t.Fatalf("want 2: %q %v", s, err)
	}
	if v, _ := st.SyncState(ctx, keyProjectTasks+"2"); v != "" {
		t.Fatalf("want 2 wrote %q", v)
	}
	must(st.SetSyncState(ctx, keyProjectTasks+"1", readmodel.TasksOpen))
	if s, _ := st.WantProject(ctx, "1"); s != readmodel.TasksOpen {
		t.Fatalf("want 1 again: %q, want open (not back to wanted)", s)
	}
	states, err := st.ProjectTaskStates(ctx)
	must(err)
	want := map[string]string{"1": readmodel.TasksOpen, "2": readmodel.TasksAll, "3": readmodel.TasksWanted}
	if len(states) != len(want) {
		t.Fatalf("states %v, want %v", states, want)
	}
	for k, v := range want {
		if states[k] != v {
			t.Fatalf("states %v, want %v", states, want)
		}
	}
	// Listed as cached: 1 (incomplete fetched) and 2; not 3 (wanted only), 4, 5.
	ps, err := st.ListProjects(ctx, false)
	must(err)
	for _, p := range ps {
		if got, want := p.Unfetched, p.GID != "1" && p.GID != "2"; got != want {
			t.Errorf("%s unfetched = %v", p.Name, got)
		}
	}
	if n, err := st.CountUnfetchedProjects(ctx, true); err != nil || n != 2 { // 3, 4
		t.Errorf("unfetched members = %d %v", n, err)
	}
	if n, err := st.CountUnfetchedProjects(ctx, false); err != nil || n != 3 {
		t.Errorf("unfetched = %d %v", n, err)
	}
	// A task created in a project (offline) opens it; one with an events token stays as it is.
	must(st.UpsertProjects(ctx, "w1", []asana.Project{{GID: "6", Name: "new"}}, "me"))
	if _, err := st.CreateTaskLocal(ctx, NewTask{Name: "新しい", ProjectGID: "6"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateTaskLocal(ctx, NewTask{Name: "もう一つ", ProjectGID: "2"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if s, _ := st.ProjectTaskState(ctx, "6"); s != readmodel.TasksWanted {
		t.Errorf("project of a new task: %q, want wanted", s)
	}
	if v, _ := st.SyncState(ctx, keyProjectTasks+"2"); v != "" {
		t.Errorf("project synced before #54 got %q", v)
	}
	// The state goes with its project, My Tasks' with its workspace.
	must(st.SetSyncState(ctx, keyMyTasks+"w1", readmodel.TasksAll))
	must(st.PruneProjects(ctx, "w1", []asana.Project{{GID: "2"}}))
	if v, _ := st.SyncState(ctx, keyProjectTasks+"1"); v != "" {
		t.Errorf("state of a removed project kept: %q", v)
	}
	must(st.UpsertWorkspaces(ctx, []asana.Workspace{{GID: "w2", Name: "B"}}))
	must(st.PruneWorkspaces(ctx, []asana.Workspace{{GID: "w2"}}))
	if v, _ := st.SyncState(ctx, keyMyTasks+"w1"); v != "" {
		t.Errorf("my tasks state of a removed workspace kept: %q", v)
	}
	if r, err := st.ProjectRef(ctx, "9"); err == nil {
		t.Errorf("ProjectRef of an unknown project = %+v", r)
	}
}
