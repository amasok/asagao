package cli

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/config"
	"github.com/amasok/asagao/internal/sync"
)

func TestSyncerKeepsOfflineAndSettings(t *testing.T) {
	a := &app{wiring: testWiring(), cfg: config.Config{Sync: config.SyncConfig{CompletedDays: 30, AllProjects: true}}}
	st, err := a.wiring.OpenStore(context.Background(), filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if s := a.syncer(nil, st); s.API != nil {
		t.Fatalf("a nil client must leave the Syncer offline: %#v", s.API)
	}
	c := asana.NewClient(nil)
	s := a.syncer(c, st)
	if s.API != sync.AsanaAPI(c) || s.Cfg != (sync.Config{CompletedDays: 30, AllProjects: true}) || s.RichText == nil {
		t.Fatalf("syncer = %+v", s)
	}
	if s.Store == nil || s.Store != st.Store.(sync.LocalStore) {
		t.Fatalf("syncer store = %T, want the store opened", s.Store)
	}
}
