package cli

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/amasok/asagao/internal/asana"
)

// Every Syncer the CLI builds (and the TUI and MCP server get theirs from it) can fit queued
// rich text: without RichText, Push stops with store.ErrNoFit.
func TestSyncerHasRichText(t *testing.T) {
	a := &app{wiring: testWiring()}
	st, err := a.wiring.OpenStore(context.Background(), filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for name, api := range map[string]*asana.Client{"online": asana.NewClient(nil), "offline (nil client)": nil} {
		if s := a.syncer(api, st); s.RichText == nil {
			t.Errorf("%s: Syncer.RichText is not set", name)
		}
	}
}
