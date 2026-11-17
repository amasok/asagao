package sync

import (
	"context"
	"errors"
	"testing"
)

// A Syncer made without an API (offline: cached files only) must not reach for the client:
// each online-only operation says it is offline, and the server clock falls back to ours.
func TestOfflineSyncerNeverCallsAPI(t *testing.T) {
	_, _, _, st := setupPush(t)
	ctx := context.Background()
	s := &Syncer{Store: st, CacheDir: t.TempDir()}
	if s.API != nil {
		t.Fatal("API must be a nil interface")
	}
	if _, err := s.CreateProject(ctx, "x"); !errors.Is(err, ErrOffline) {
		t.Errorf("CreateProject: %v", err)
	}
	if err := s.SetProjectArchived(ctx, "p1", true); !errors.Is(err, ErrOffline) {
		t.Errorf("SetProjectArchived: %v", err)
	}
	if err := s.SetProjectMembers(ctx, "p1", []string{"u"}, nil); !errors.Is(err, ErrOffline) {
		t.Errorf("SetProjectMembers: %v", err)
	}
	if err := s.FetchStatusUpdates(ctx, "p1"); !errors.Is(err, ErrOffline) {
		t.Errorf("FetchStatusUpdates: %v", err)
	}
	if _, err := s.DownloadAttachment(ctx, "t1", "nope"); err == nil {
		t.Error("DownloadAttachment: no error")
	}
	if got := s.serverNow(); got.IsZero() {
		t.Error("serverNow is zero")
	}
}
