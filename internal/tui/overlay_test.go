package tui

import (
	"strings"
	"testing"
)

// The overlays share one slot: opening one closes the one before, and closing one that
// is no longer open leaves the one opened meanwhile alone.
func TestOverlaySlotIsExclusive(t *testing.T) {
	kinds := []overlayKind{overlayMenu, overlayHelp, overlayOutbox, overlayStatuses, overlayOverview, overlayGoals}
	for _, a := range kinds {
		for _, b := range kinds {
			if a == b {
				continue
			}
			m := &Model{}
			m.openOverlay(a)
			m.openOverlay(b)
			if !m.showing(b) || m.showing(a) {
				t.Fatalf("open %d then %d: overlay = %d", a, b, m.overlay)
			}
			m.closeOverlay(a)
			if !m.showing(b) {
				t.Fatalf("closing %d (not open) closed %d", a, b)
			}
			m.closeOverlay(b)
			if m.overlay != overlayNone {
				t.Fatalf("closing %d left overlay %d", b, m.overlay)
			}
		}
	}
}

// The help opened from the menu replaces the menu, and the next key closes the help and
// shows the panes (no menu left behind under it).
func TestHelpFromMenuLeavesNoMenu(t *testing.T) {
	h := newHarness(t, 100, 30)
	h.key("space")
	if !h.m.showing(overlayMenu) {
		t.Fatal("space did not open the menu")
	}
	h.key("?")
	if !h.m.showing(overlayHelp) {
		t.Fatalf("? from the menu did not open the help: overlay = %d", h.m.overlay)
	}
	h.key("x")
	if h.m.overlay != overlayNone {
		t.Fatalf("a key on the help left overlay %d", h.m.overlay)
	}
	if strings.Contains(h.screen(), "メニュー（") {
		t.Fatalf("the menu is still drawn:\n%s", h.screen())
	}
}
