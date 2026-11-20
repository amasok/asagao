package tui

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/sync"
	"github.com/amasok/asagao/internal/wire"
)

// Offline, attachments come from the local syncer (in the TUI's cache folder when asked);
// without one they are not found, rather than the TUI failing (#71).
func TestAttachmentsOffline(t *testing.T) {
	st := fixtureStore(t)
	now := func() time.Time { return testNow }

	m := New(context.Background(), Deps{App: wire.UseCases(st), Now: now, CacheDir: "/cache"})
	if _, err := m.attachments(true).DownloadAttachment(context.Background(), "t1", "a1"); !errors.Is(err, errNoAttachments) {
		t.Errorf("without a local syncer: err = %v, want errNoAttachments", err)
	}

	m = New(context.Background(), onStore(st, Deps{Now: now, CacheDir: "/cache"}))
	s, ok := m.attachments(true).(*sync.Syncer)
	if !ok || s.CacheDir != "/cache" || s.API != nil {
		t.Errorf("offline in the cache folder: %#v", m.attachments(true))
	}
	if s, ok := m.attachments(false).(*sync.Syncer); !ok || s.CacheDir != "" {
		t.Errorf("offline: %#v, want the local syncer as it is", m.attachments(false))
	}

	online := &sync.Syncer{Store: st}
	m.syncer = online
	if got := m.attachments(true); got != attachmentSource(online) || online.CacheDir != "" {
		t.Errorf("online: %#v, want the model's syncer, untouched", got)
	}
}
