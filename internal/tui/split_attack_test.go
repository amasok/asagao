package tui

import (
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/readmodel"
)

// --- overlays: switching and closing ---

// The overview hands over to the status updates with U; esc then leaves no overlay.
func TestSplitAttackOverviewToStatusesThenEsc(t *testing.T) {
	h := projectHarness(t, fixtureStore(t), nil)
	h.menu("O")
	if !h.m.showing(overlayOverview) {
		t.Fatalf("O should open the overview: %d", h.m.overlay)
	}
	h.key("U")
	if !h.m.showing(overlayStatuses) {
		t.Fatalf("U should switch to the statuses: %d", h.m.overlay)
	}
	h.key("esc")
	if h.m.overlay != overlayNone {
		t.Fatalf("esc left overlay %d", h.m.overlay)
	}
}

// Keys of a closed overlay do not move its cursor: after esc, j moves the task list.
func TestSplitAttackEscThenKeysGoToPanes(t *testing.T) {
	h := newHarness(t, 140, 30)
	h.key("!")
	if !h.m.showing(overlayOutbox) {
		t.Fatal("! should open the outbox")
	}
	h.key("esc")
	before := h.selected()
	h.key("j")
	if h.m.overlay != overlayNone || h.selected() == before {
		t.Fatalf("j after closing should move the list (overlay=%d, %s -> %s)", h.m.overlay, before, h.selected())
	}
}

// The help over the menu, then each overlay key: nothing may keep two overlays.
func TestSplitAttackMenuSpaceTwiceAndHelp(t *testing.T) {
	h := newHarness(t, 140, 30)
	h.key(" ", "esc")
	if h.m.overlay != overlayNone {
		t.Fatalf("esc on the menu left %d", h.m.overlay)
	}
	h.key("?", "?")
	if h.m.overlay != overlayNone {
		t.Fatalf("? on the help left %d", h.m.overlay)
	}
}

// A prompt over an overlay-less screen: esc forgets the comment target and
// the next prompt does not inherit it.
func TestSplitAttackPromptEscClearsTarget(t *testing.T) {
	h := newHarness(t, 140, 30)
	h.key("c")
	if h.m.prompt.commentGID == "" {
		t.Fatal("c should target the selected task")
	}
	h.key("esc")
	if h.m.prompt.mode != promptNone || h.m.prompt.commentGID != "" {
		t.Fatalf("esc left prompt: %+v", h.m.prompt)
	}
	h.key("a")
	if h.m.prompt.mode != promptCreate || h.m.prompt.commentGID != "" {
		t.Fatalf("a inherited a target: %+v", h.m.prompt)
	}
}

// --- late messages after the overlay closed ---

func TestSplitAttackLateOverviewDoesNotReopen(t *testing.T) {
	h := projectHarness(t, fixtureStore(t), nil)
	h.menu("O")
	h.key("esc")
	p := h.m.currentProject()
	h.send(overviewLoadedMsg{project: p, overview: &readmodel.ProjectOverview{GID: p}, fetchErr: errString("403")})
	if h.m.overlay != overlayNone {
		t.Fatalf("a late overview opened overlay %d", h.m.overlay)
	}
	if strings.Contains(h.m.status, "Asana から取れなかった") {
		t.Fatalf("a closed overview reported a fetch error: %q", h.m.status)
	}
}

func TestSplitAttackLateStatusesDoNotReopen(t *testing.T) {
	h := projectHarness(t, fixtureStore(t), nil)
	h.menu("U")
	h.key("esc")
	h.send(statusesLoadedMsg{project: h.m.currentProject(), updates: []readmodel.StatusUpdateView{{}}})
	if h.m.overlay != overlayNone {
		t.Fatalf("late statuses opened overlay %d", h.m.overlay)
	}
}

func TestSplitAttackLateGoalListDoesNotReopen(t *testing.T) {
	h := newHarness(t, 140, 30)
	h.m.openGoalScreen(false)
	seq := h.m.goals.seq
	h.m.closeOverlay(overlayGoals)
	h.send(goalListMsg{seq: seq})
	if h.m.overlay != overlayNone {
		t.Fatalf("a late goal list opened overlay %d", h.m.overlay)
	}
}

// The outbox reload landing while the help is open does not change the overlay;
// a smaller outbox clamps the cursor.
func TestSplitAttackOutboxLoadWhileOtherOverlay(t *testing.T) {
	h := newHarness(t, 140, 30)
	h.m.outboxView.cursor = 5
	h.key("?")
	h.send(outboxLoadedMsg{ops: []outbox.Op{{}, {}}})
	if !h.m.showing(overlayHelp) {
		t.Fatalf("overlay changed to %d", h.m.overlay)
	}
	if h.m.outboxView.cursor != 1 {
		t.Fatalf("cursor not clamped: %d", h.m.outboxView.cursor)
	}
	h.send(outboxLoadedMsg{})
	if h.m.outboxView.cursor != 0 {
		t.Fatalf("cursor on empty outbox: %d", h.m.outboxView.cursor)
	}
}

// A sync finishing while an overlay is open leaves it open.
func TestSplitAttackSyncDoneKeepsOverlay(t *testing.T) {
	h := newHarness(t, 140, 30)
	h.key("!")
	h.send(syncDoneMsg{})
	if !h.m.showing(overlayOutbox) {
		t.Fatalf("sync closed the outbox: %d", h.m.overlay)
	}
	h.key("esc", " ")
	h.send(syncDoneMsg{})
	if !h.m.showing(overlayMenu) {
		t.Fatalf("sync closed the menu: %d", h.m.overlay)
	}
}

// Reopening the outbox scrolls back to the top.
func TestSplitAttackOutboxReopenResetsTop(t *testing.T) {
	h := newHarness(t, 140, 30)
	h.key("!")
	h.m.outboxView.top = 7
	h.key("esc", "!")
	if h.m.outboxView.top != 0 {
		t.Fatalf("top = %d", h.m.outboxView.top)
	}
}

// --- view modes ---

// Cycling the view keeps the selected task, and a search blocks the switch.
func TestSplitAttackViewCycleKeepsSelection(t *testing.T) {
	h := projectHarness(t, fixtureStore(t), nil)
	h.m.selectInList("t3")
	want := h.selected()
	for i := 0; i < 4; i++ {
		h.key("v")
	}
	if h.m.mode != viewList {
		t.Fatalf("four v should return to the list, got %d", h.m.mode)
	}
	if h.selected() != want {
		t.Fatalf("selection %s -> %s", want, h.selected())
	}
	h.m.search.query = "電気"
	mode := h.m.mode
	h.key("v")
	if h.m.mode != mode {
		t.Fatal("v changed the view during a search")
	}
}

// The mode is kept when moving to a scope that cannot show it, and comes back.
func TestSplitAttackModeSurvivesScopeWithoutIt(t *testing.T) {
	h := projectHarness(t, fixtureStore(t), nil)
	h.key("v")
	if h.m.mode != viewBoard || !h.m.boardActive() {
		t.Fatalf("board expected: mode=%d", h.m.mode)
	}
	proj := h.m.scopeCursor
	h.m.moveScope(-proj) // the first scope is not a project
	if h.m.currentScope().kind == scopeProject {
		t.Skip("first scope is a project")
	}
	if h.m.mode != viewBoard || h.m.boardActive() {
		t.Fatalf("mode kept but board inactive expected: mode=%d active=%v", h.m.mode, h.m.boardActive())
	}
	h.m.moveScope(proj)
	if !h.m.boardActive() {
		t.Fatal("board should be back in the project")
	}
}

type errString string

func (e errString) Error() string { return string(e) }
