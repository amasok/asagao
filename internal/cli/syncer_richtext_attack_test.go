package cli

import (
	"testing"

	"github.com/amasok/asagao/internal/asana"
)

// Every Syncer the CLI builds (and the TUI and MCP server get theirs from it) can fit queued
// rich text: without RichText, Push stops with store.ErrNoFit.
func TestSyncerHasRichText(t *testing.T) {
	a := &app{wiring: testWiring()}
	for name, api := range map[string]*asana.Client{"online": asana.NewClient(nil), "offline (nil client)": nil} {
		if s := a.syncer(api, nil); s.RichText == nil {
			t.Errorf("%s: Syncer.RichText is not set", name)
		}
	}
}
