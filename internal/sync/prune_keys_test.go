package sync

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/amasok/asana-tui/internal/asana"
	"github.com/amasok/asana-tui/internal/config"
	"github.com/amasok/asana-tui/internal/store"
)

// #20: a full sync removes workspaces and projects Asana no longer returns, with their
// sync_state keys; an incremental sync does not.
func TestFullSyncPrunesGoneWorkspaceAndProjectKeys(t *testing.T) {
	fake := &fakeAsana{t: t, projectTasks: `[]`}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	api := asana.NewClient(srv.Client())
	api.BaseURL = srv.URL
	s := &Syncer{API: api, Store: st, Cfg: config.SyncConfig{CompletedDays: 14}}
	if _, err := s.Run(ctx, true); err != nil {
		t.Fatal(err)
	}
	// Left over from before: a workspace and a project the account no longer has.
	if err := st.UpsertWorkspaces(ctx, []asana.Workspace{{GID: "w9", Name: "Old"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertProjects(ctx, "w9", []asana.Project{{GID: "900"}}, "u1"); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertProjects(ctx, "w1", []asana.Project{{GID: "901"}}, "u1"); err != nil {
		t.Fatal(err)
	}
	keys := []string{store.KeyCustomFields + "900", store.KeyCollapsed + "900", store.KeyCustomFields + "901", store.KeyCollapsed + "901"}
	for _, k := range keys {
		_ = st.SetSyncState(ctx, k, "x")
	}
	if res, err := s.Run(ctx, false); err != nil || res.Full {
		t.Fatalf("incremental = %+v %v", res, err)
	}
	for _, k := range keys {
		if v, _ := st.SyncState(ctx, k); v == "" {
			t.Fatalf("incremental sync must not prune (%s)", k)
		}
	}
	if _, err := s.Run(ctx, true); err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		if v, _ := st.SyncState(ctx, k); v != "" {
			t.Errorf("key %s left after full sync", k)
		}
	}
	if stats, _ := st.Stats(ctx); stats.Workspaces != 1 {
		t.Errorf("workspaces = %d, want 1", stats.Workspaces)
	}
}
