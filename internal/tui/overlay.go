package tui

import (
	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/readmodel"
)

// The screens drawn over the panes (the space menu, the help, the outbox, the status
// updates, the project overview, the goals) are shown one at a time, so the one open is
// kept in a single slot (Model.overlay): opening one closes the one before, and there is
// no flag to forget to clear. A chooser (Model.picker) and the footer inputs (the prompt,
// the search, a y/n question of the bulk changes) are layers of their own: a chooser can
// be shown over the overview (the confirmation of B) and the footer inputs over anything.

type overlayKind int

const (
	overlayNone     overlayKind = iota
	overlayMenu                 // the space menu (keymap.go)
	overlayHelp                 // every key (help.go)
	overlayOutbox               // the changes waiting to be sent (write.go)
	overlayStatuses             // the project's status updates (projects.go)
	overlayOverview             // the project overview (project_overview.go)
	overlayGoals                // goals and portfolios (goals.go)
)

// showing reports whether k is the overlay open.
func (m *Model) showing(k overlayKind) bool { return m.overlay == k }

// openOverlay shows k in place of the overlay open (if any).
func (m *Model) openOverlay(k overlayKind) { m.overlay = k }

// closeOverlay closes k if it is the overlay open (an overlay opened meanwhile stays).
func (m *Model) closeOverlay(k overlayKind) {
	if m.overlay == k {
		m.overlay = overlayNone
	}
}

// outboxScreen is the state of the outbox overlay (write.go).
type outboxScreen struct {
	ops      []outbox.Op
	cursor   int
	top      int                 // first line on screen (it scrolls)
	discards []readmodel.Discard // recently dropped changes, newest first (shown in full)
}

// statusScreen is the state of the status updates overlay (projects.go).
type statusScreen struct {
	offset  int
	updates []readmodel.StatusUpdateView
}

// overviewScreen is the state of the project overview overlay (project_overview.go).
type overviewScreen struct {
	offset int
	data   *readmodel.ProjectOverview
}
