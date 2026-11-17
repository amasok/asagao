package cli

import (
	"testing"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/config"
	"github.com/amasok/asagao/internal/sync"
)

func TestSyncerKeepsOfflineAndSettings(t *testing.T) {
	a := &app{cfg: config.Config{Sync: config.SyncConfig{CompletedDays: 30, AllProjects: true}}}
	if s := a.syncer(nil, nil); s.API != nil {
		t.Fatalf("a nil client must leave the Syncer offline: %#v", s.API)
	}
	c := asana.NewClient(nil)
	s := a.syncer(c, nil)
	if s.API != sync.AsanaAPI(c) || s.Cfg != (sync.Config{CompletedDays: 30, AllProjects: true}) {
		t.Fatalf("syncer = %+v", s)
	}
}
