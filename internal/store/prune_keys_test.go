package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/amasok/asagao/internal/asana"
)

// #20: per-project sync_state keys go away with their project or workspace.
func TestPruneDeletesProjectAndWorkspaceKeys(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.UpsertWorkspaces(ctx, []asana.Workspace{{GID: "w1", Name: "A"}, {GID: "w2", Name: "B"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertProjects(ctx, "w1", []asana.Project{{GID: "111"}, {GID: "222"}}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertProjects(ctx, "w2", []asana.Project{{GID: "333"}}, "me"); err != nil {
		t.Fatal(err)
	}
	keys := []string{
		keyCustomFields + "111", keyCustomFields + "222", keyCustomFields + "333",
		keyCollapsed + "111", keyCollapsed + "222", keyCollapsed + "333",
		keyCollapsed + "999",                                     // left behind by an older version (project already gone)
		keyCustomFields + "999",                                  // likewise
		keyCollapsed + "my", keyCollapsed + "inbox", "ui:layout", // not project keys
		keyMeGID, keyLastSync, resultPrefixTask + "local:abc", "tag:local:def",
	}
	for _, k := range keys {
		if err := st.SetSyncState(ctx, k, "x"); err != nil {
			t.Fatal(err)
		}
	}
	check := func(step string, gone ...string) {
		t.Helper()
		isGone := map[string]bool{}
		for _, k := range gone {
			isGone[k] = true
		}
		for _, k := range keys {
			v, err := st.SyncState(ctx, k)
			if err != nil {
				t.Fatal(err)
			}
			if isGone[k] && v != "" {
				t.Errorf("%s: key %q should be deleted", step, k)
			}
			if !isGone[k] && v == "" {
				t.Errorf("%s: key %q should be kept", step, k)
			}
		}
	}

	// Project 222 is no longer in Asana.
	if err := st.PruneProjects(ctx, "w1", []asana.Project{{GID: "111"}}); err != nil {
		t.Fatal(err)
	}
	gone := []string{keyCustomFields + "222", keyCollapsed + "222", keyCollapsed + "999", keyCustomFields + "999"}
	check("prune projects", gone...)

	// An empty answer is not trusted: nothing is removed.
	if err := st.PruneWorkspaces(ctx, nil); err != nil {
		t.Fatal(err)
	}
	check("empty workspaces", gone...)

	// Workspace w2 is gone: its project 333 and its keys go too.
	if err := st.PruneWorkspaces(ctx, []asana.Workspace{{GID: "w1"}}); err != nil {
		t.Fatal(err)
	}
	gone = append(gone, keyCustomFields+"333", keyCollapsed+"333")
	check("prune workspaces", gone...)
	if stats, _ := st.Stats(ctx); stats.Workspaces != 1 || stats.Projects != 1 {
		t.Fatalf("stats = %+v, want 1 workspace and 1 project", stats)
	}
}
