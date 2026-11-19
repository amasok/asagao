package tui

import (
	"context"
	"path/filepath"
	"strings"
	stdsync "sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/store"
	"github.com/amasok/asagao/internal/sync"
)

// #68 attack: exact 24h boundary (23h59m59s, 24h-1ns, 24h, 24h+1s) and exact start boundary
// (recordedAt-1ns, recordedAt) for all 7 plan-gated features, comparing TUI planOK against
// readmodel.PlanOffFor and app use cases without any write or manual loadPlan.
func TestTUIPlanRecheckExactBoundariesAttack(t *testing.T) {
	ctx := context.Background()
	st := fixtureStore(t)
	if err := st.UpsertWorkspaces(ctx, []asana.Workspace{{GID: "w1", Name: "W1"}}); err != nil {
		t.Fatal(err)
	}
	recordedAt := testNow
	recStr := recordedAt.UTC().Format(time.RFC3339)
	allFeatures := []string{
		readmodel.PlanSections,
		readmodel.PlanTags,
		readmodel.PlanTimeTracking,
		readmodel.PlanGoals,
		readmodel.PlanPortfolios,
		readmodel.PlanProjectTemplates,
		readmodel.PlanCustomFields,
	}
	for _, f := range allFeatures {
		if err := st.SetSyncState(ctx, readmodel.PlanKey(f, "w1"), recStr); err != nil {
			t.Fatal(err)
		}
	}

	now := recordedAt.Add(time.Hour)
	m := New(ctx, Deps{Store: st, Now: func() time.Time { return now }})
	h := &harness{t: t, m: m}
	h.send(tea.WindowSizeMsg{Width: 140, Height: 30})
	h.run(m.Init())

	appAvail := func(feature string, at time.Time) bool {
		clk := func() time.Time { return at }
		switch feature {
		case readmodel.PlanSections:
			return (&app.Sections{Store: st, Clock: clk}).Available(ctx)
		case readmodel.PlanTags:
			return (&app.Tags{Store: st, Clock: clk}).Available(ctx)
		case readmodel.PlanTimeTracking:
			return (&app.TimeTracking{Store: st, Clock: clk}).Available(ctx)
		case readmodel.PlanGoals:
			return (&app.Goals{Store: st, Clock: clk}).Available(ctx)
		case readmodel.PlanPortfolios:
			return (&app.Goals{Store: st, Clock: clk}).PortfoliosAvailable(ctx)
		case readmodel.PlanProjectTemplates:
			return app.TemplatesOffered(ctx, st, readmodel.PlanProjectTemplates, "w1", at)
		case readmodel.PlanCustomFields:
			return (&app.ProjectFields{Store: st, Clock: clk}).Available(ctx, "w1")
		default:
			t.Fatalf("unexpected feature %q", feature)
			return false
		}
	}

	cases := []struct {
		name      string
		at        time.Time
		wantOffer bool
	}{
		{"before_recorded_1h", recordedAt.Add(-time.Hour), true},
		{"before_recorded_1ns", recordedAt.Add(-time.Nanosecond), true},
		{"at_recorded_exact", recordedAt, false},
		{"before_24h_1s", recordedAt.Add(readmodel.PlanRecheckAfter - time.Second), false},
		{"before_24h_1ns", recordedAt.Add(readmodel.PlanRecheckAfter - time.Nanosecond), false},
		{"at_24h_exact", recordedAt.Add(readmodel.PlanRecheckAfter), true},
		{"after_24h_1s", recordedAt.Add(readmodel.PlanRecheckAfter + time.Second), true},
	}

	// First check dynamically WITHOUT reloading planOff from DB (planWindow.off(now)).
	for _, tc := range cases {
		now = tc.at
		for _, f := range allFeatures {
			wantByReadmodel := !readmodel.PlanOffFor(recStr, tc.at)
			wantByApp := appAvail(f, tc.at)
			gotTUI := h.m.planOK(f)
			if wantByReadmodel != tc.wantOffer || wantByApp != tc.wantOffer || gotTUI != tc.wantOffer {
				t.Errorf("cached %s [%s]: TUI=%v app=%v readmodel=%v, want %v",
					tc.name, f, gotTUI, wantByApp, wantByReadmodel, tc.wantOffer)
			}
		}
	}

	// Also check AFTER re-running loadPlan() at each timestamp.
	for _, tc := range cases {
		now = tc.at
		h.run(h.m.loadPlan())
		for _, f := range allFeatures {
			wantByApp := appAvail(f, tc.at)
			gotTUI := h.m.planOK(f)
			if gotTUI != wantByApp || gotTUI != tc.wantOffer {
				t.Errorf("reloaded %s [%s]: TUI=%v app=%v, want %v",
					tc.name, f, gotTUI, wantByApp, tc.wantOffer)
			}
		}
	}
}

// #68 attack: multi-workspace 402 timeline and backward clock jump.
// ws1 gets 402 at T+0h, ws2 gets 402 at T+10h:
//   - T+12h: both active -> hidden (TUI == app == false)
//   - T+24h-1ns: both still active -> hidden
//   - T+24h and T+24h+1s: ws1 expired, ws2 still active -> offered (TUI == app == true)
//   - T+5h (clock jumps backward before ws2's 402!): ws2's mark is in the future so
//     readmodel.PlanOffFor(ws2, T+5h) is false -> offered, and TUI planOK MUST agree with
//     app.planAnywhere (both before and after loadPlan)!
func TestTUIPlanMultiWorkspaceTimelineAndClockRollbackAttack(t *testing.T) {
	isolateDirs(t)
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "multi.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.UpsertWorkspaces(ctx, []asana.Workspace{
		{GID: "ws1", Name: "WS1"},
		{GID: "ws2", Name: "WS2"},
	}); err != nil {
		t.Fatal(err)
	}
	if wss, err := st.WorkspaceGIDs(ctx); err != nil || len(wss) != 2 {
		t.Fatalf("workspaces = %v, %v; want exactly [ws1 ws2]", wss, err)
	}

	base := testNow
	tWS1 := base
	tWS2 := base.Add(10 * time.Hour)
	multiFeatures := []string{readmodel.PlanSections, readmodel.PlanTags, readmodel.PlanTimeTracking}
	for _, f := range multiFeatures {
		if err := st.SetSyncState(ctx, readmodel.PlanKey(f, "ws1"), tWS1.UTC().Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
		if err := st.SetSyncState(ctx, readmodel.PlanKey(f, "ws2"), tWS2.UTC().Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
	}

	// Start at T+12h (both active) so m.planOff caches the intersection window [T+10h, T+24h).
	now := base.Add(12 * time.Hour)
	m := New(ctx, Deps{Store: st, Now: func() time.Time { return now }})
	h := &harness{t: t, m: m}
	h.send(tea.WindowSizeMsg{Width: 140, Height: 30})
	h.run(m.Init())

	appAvail := func(feature string, at time.Time) bool {
		clk := func() time.Time { return at }
		switch feature {
		case readmodel.PlanSections:
			return (&app.Sections{Store: st, Clock: clk}).Available(ctx)
		case readmodel.PlanTags:
			return (&app.Tags{Store: st, Clock: clk}).Available(ctx)
		case readmodel.PlanTimeTracking:
			return (&app.TimeTracking{Store: st, Clock: clk}).Available(ctx)
		default:
			t.Fatalf("unexpected feature %q", feature)
			return false
		}
	}

	timeline := []struct {
		name      string
		at        time.Time
		wantOffer bool
	}{
		{"T+12h_both_active", base.Add(12 * time.Hour), false},
		{"T+24h-1s_both_active", base.Add(readmodel.PlanRecheckAfter - time.Second), false},
		{"T+24h-1ns_both_active", base.Add(readmodel.PlanRecheckAfter - time.Nanosecond), false},
		{"T+24h_ws1_expired", base.Add(readmodel.PlanRecheckAfter), true},
		{"T+24h+1s_ws1_expired_ws2_active", base.Add(readmodel.PlanRecheckAfter + time.Second), true},
		{"T+5h_clock_rollback_before_ws2", base.Add(5 * time.Hour), true},
		{"T+10h-1ns_clock_rollback_just_before_ws2", base.Add(10*time.Hour - time.Nanosecond), true},
		{"T+10h_exact_both_active", base.Add(10 * time.Hour), false},
		{"T-1h_clock_before_both", base.Add(-time.Hour), true},
	}

	// Phase 1: check WITHOUT reloading planOff (pure planWindow evaluation on cached window).
	for _, tc := range timeline {
		now = tc.at
		for _, f := range multiFeatures {
			gotApp := appAvail(f, tc.at)
			gotTUI := h.m.planOK(f)
			if gotApp != tc.wantOffer {
				t.Errorf("cached %s [%s]: app.Available = %v, want %v", tc.name, f, gotApp, tc.wantOffer)
			}
			if gotTUI != gotApp {
				t.Errorf("cached %s [%s]: TUI planOK=%v disagrees with app.Available=%v", tc.name, f, gotTUI, gotApp)
			}
		}
	}

	// Phase 2: check AFTER loadPlan() at each timestamp (including starting behind at T+5h
	// and then moving forward to T+12h and opening space).
	for _, tc := range timeline {
		now = tc.at
		h.run(h.m.loadPlan())
		for _, f := range multiFeatures {
			gotApp := appAvail(f, tc.at)
			gotTUI := h.m.planOK(f)
			if gotTUI != gotApp || gotTUI != tc.wantOffer {
				t.Errorf("reloaded %s [%s]: TUI=%v app=%v, want %v", tc.name, f, gotTUI, gotApp, tc.wantOffer)
			}
		}
	}

	// Phase 3: non-overlapping 402 windows (ws1 at T+0h, ws2 at T+30h) and corrupt value
	// must never hide the feature, matching app.Available.
	if err := st.SetSyncState(ctx, readmodel.PlanKey(readmodel.PlanTags, "ws2"), base.Add(30*time.Hour).UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Time{base.Add(12 * time.Hour), base.Add(25 * time.Hour), base.Add(35 * time.Hour)} {
		now = at
		h.run(h.m.loadPlan())
		if !h.m.planOK(readmodel.PlanTags) || !appAvail(readmodel.PlanTags, at) {
			t.Errorf("non-overlapping at %v: TUI=%v app=%v, want true", at, h.m.planOK(readmodel.PlanTags), appAvail(readmodel.PlanTags, at))
		}
	}

	// Timezone-offset RFC3339 (+09:00) and garbage string.
	jst := time.FixedZone("JST", 9*3600)
	if err := st.SetSyncState(ctx, readmodel.PlanKey(readmodel.PlanTags, "ws2"), base.Add(2*time.Hour).In(jst).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	now = base.Add(3 * time.Hour)
	h.run(h.m.loadPlan())
	if h.m.planOK(readmodel.PlanTags) || appAvail(readmodel.PlanTags, now) {
		t.Errorf("RFC3339 +09:00 at T+3h: TUI=%v app=%v, want false", h.m.planOK(readmodel.PlanTags), appAvail(readmodel.PlanTags, now))
	}
	if err := st.SetSyncState(ctx, readmodel.PlanKey(readmodel.PlanTags, "ws2"), "not-a-timestamp"); err != nil {
		t.Fatal(err)
	}
	h.run(h.m.loadPlan())
	if !h.m.planOK(readmodel.PlanTags) || !appAvail(readmodel.PlanTags, now) {
		t.Errorf("garbage sync_state: TUI=%v app=%v, want true", h.m.planOK(readmodel.PlanTags), appAvail(readmodel.PlanTags, now))
	}
}

// #68 attack: when another process modifies sync_state in the same SQLite DB (adding or
// clearing 402 marks), pressing space in TUI reloads the plan state and updates the menu.
func TestTUISpaceReloadsPlanFromOtherProcessAttack(t *testing.T) {
	isolateDirs(t)
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "shared.db")

	st1, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st1.Close() })

	// Seed minimal project/task fixture in st1.
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st1.SetSyncState(ctx, store.KeyMeGID, "me"))
	must(st1.UpsertWorkspaces(ctx, []asana.Workspace{{GID: "w1", Name: "W1"}}))
	must(st1.UpsertProjects(ctx, "w1", []asana.Project{{GID: "p1", Name: "P1", Members: []asana.Ref{{GID: "me"}}}}, "me"))
	must(st1.SetSyncState(ctx, store.KeyProjectTasks+"p1", "all"))
	must(st1.ReplaceSections(ctx, "p1", []asana.Section{{GID: "s1", Name: "S1"}}))
	must(st1.UpsertTasks(ctx, []asana.Task{{
		GID: "t1", Name: "T1", Workspace: &asana.Ref{GID: "w1"},
		Assignee:    &asana.Ref{GID: "me", Name: "Me"},
		Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}, Section: &asana.Ref{GID: "s1"}}},
	}}))

	// Open a SECOND store handle on the same DB file to simulate another process (e.g. CLI/MCP).
	st2, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st2.Close() })

	now := testNow
	m := New(ctx, Deps{Store: st1, Now: func() time.Time { return now }})
	h := &harness{t: t, m: m}
	h.send(tea.WindowSizeMsg{Width: 140, Height: 30})
	h.run(m.Init())
	h.key("h", "j", "j", "l") // open p1 on t1

	menuText := func() string {
		s := strings.Join(menuScreens(t, h), "\n")
		h.key("esc")
		return s
	}

	// Initially no 402 -> sections and tags are offered.
	if s := menuText(); !strings.Contains(s, "セクションを追加") || !strings.Contains(s, "タグの名前・色") {
		t.Fatalf("initial menu missing items:\n%s", s)
	}

	// Other process writes 402 marks into sync_state.
	mark := now.UTC().Format(time.RFC3339)
	must(st2.SetSyncState(ctx, readmodel.PlanKey(readmodel.PlanSections, "w1"), mark))
	must(st2.SetSyncState(ctx, readmodel.PlanKey(readmodel.PlanTags, "w1"), mark))

	// Pressing space reloads plan from DB -> items disappear.
	if s := menuText(); strings.Contains(s, "セクションを追加") || strings.Contains(s, "タグの名前・色") {
		t.Fatalf("after other process wrote 402, space menu still offered them:\n%s", s)
	}
	if h.m.planOK(readmodel.PlanSections) || h.m.planOK(readmodel.PlanTags) {
		t.Fatal("planOK still true after other process wrote 402 and space was pressed")
	}

	// Other process clears 402 marks (well before 24h!).
	now = now.Add(5 * time.Minute)
	must(st2.SetSyncState(ctx, readmodel.PlanKey(readmodel.PlanSections, "w1"), ""))
	must(st2.SetSyncState(ctx, readmodel.PlanKey(readmodel.PlanTags, "w1"), ""))

	// Pressing space reloads plan from DB -> items reappear!
	if s := menuText(); !strings.Contains(s, "セクションを追加") || !strings.Contains(s, "タグの名前・色") {
		t.Fatalf("after other process cleared 402, space menu still hid them:\n%s", s)
	}
	if !h.m.planOK(readmodel.PlanSections) || !h.m.planOK(readmodel.PlanTags) {
		t.Fatal("planOK still false after other process cleared 402 and space was pressed")
	}
}

// #68 attack: out-of-order / stale planLoadedMsg (including a pre-402 loadPlan arriving
// after a projectTemplatesMsg 402, or a rogue future seq) must never overwrite newer state.
func TestTUIPlanSeqDropsStaleOutOfOrderAndTemplateRaceAttack(t *testing.T) {
	ctx := context.Background()
	st := fixtureStore(t)
	if err := st.UpsertWorkspaces(ctx, []asana.Workspace{{GID: "w1", Name: "W1"}}); err != nil {
		t.Fatal(err)
	}

	now := testNow
	m := New(ctx, Deps{Store: st, Now: func() time.Time { return now }})
	h := &harness{t: t, m: m}
	h.send(tea.WindowSizeMsg{Width: 140, Height: 30})
	h.run(m.Init())

	// 1. Out-of-order loadPlan cmds: cmd1 reads 402 (seq N), DB is cleared, cmd2 reads clear
	// (seq N+1), msg2 arrives BEFORE msg1.
	if err := st.SetSyncState(ctx, readmodel.PlanKey(readmodel.PlanTags, "w1"), now.UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	cmd1 := h.m.loadPlan()
	msg1 := cmd1() // snapshot with 402, older seq

	if err := st.SetSyncState(ctx, readmodel.PlanKey(readmodel.PlanTags, "w1"), ""); err != nil {
		t.Fatal(err)
	}
	cmd2 := h.m.loadPlan()
	msg2 := cmd2() // snapshot cleared, newer seq

	// Deliver newer first, then older.
	h.send(msg2)
	if !h.m.planOK(readmodel.PlanTags) {
		t.Fatal("after newer msg2 (cleared), PlanTags should be offered")
	}
	h.send(msg1)
	if !h.m.planOK(readmodel.PlanTags) {
		t.Fatal("stale msg1 overwrote newer cleared planOff")
	}

	// Rogue future seq must also be ignored.
	h.send(planLoadedMsg{
		seq: h.m.planSeq + 1,
		off: map[string]planWindow{
			readmodel.PlanTags: {from: now, until: now.Add(readmodel.PlanRecheckAfter)},
		},
	})
	if !h.m.planOK(readmodel.PlanTags) {
		t.Fatal("rogue future seq overwrote current planOff")
	}

	// 2. Interleaving with projectTemplatesMsg 402:
	// Suppose space triggered loadPlan() (cmdBefore) while DB had NO 402 for project templates,
	// and before cmdBefore finishes, fetchProjectTemplates hits 402, writes sync_state, and
	// delivers projectTemplatesMsg{err: *sync.TemplatePlanError}.
	cmdBefore := h.m.loadPlan()
	msgBefore := cmdBefore() // snapshot from BEFORE the 402!

	if err := st.SetSyncState(ctx, readmodel.PlanKey(readmodel.PlanProjectTemplates, "w1"), now.UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	// Deliver projectTemplatesMsg 402 -> templateError hides it AND returns a fresh loadPlan()
	// (bumping planSeq!).
	nextCmd := h.sendCmd(projectTemplatesMsg{err: &sync.TemplatePlanError{Feature: readmodel.PlanProjectTemplates}})
	if h.m.planOK(readmodel.PlanProjectTemplates) {
		t.Fatal("PlanProjectTemplates offered right after TemplatePlanError")
	}
	// Now the stale cmdBefore arrives -> must be dropped!
	h.send(msgBefore)
	if h.m.planOK(readmodel.PlanProjectTemplates) {
		t.Fatal("stale pre-402 planLoadedMsg un-hid PlanProjectTemplates after TemplatePlanError")
	}
	// Running the follow-up loadPlan keeps it hidden.
	h.run(nextCmd)
	if h.m.planOK(readmodel.PlanProjectTemplates) {
		t.Fatal("follow-up loadPlan un-hid PlanProjectTemplates")
	}
}

// sendCmd sends a single message and returns the resulting tea.Cmd without executing it.
func (h *harness) sendCmd(msg tea.Msg) tea.Cmd {
	h.t.Helper()
	_, cmd := h.m.Update(msg)
	return cmd
}

// #68 attack: concurrent loadPlan executions while another handle updates sync_state are race-free.
func TestTUIPlanLoadConcurrentAttack(t *testing.T) {
	isolateDirs(t)
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "race.db")

	st1, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st1.Close() })
	st2, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st2.Close() })

	if err := st1.UpsertWorkspaces(ctx, []asana.Workspace{{GID: "w1", Name: "W1"}, {GID: "w2", Name: "W2"}}); err != nil {
		t.Fatal(err)
	}

	m := New(ctx, Deps{Store: st1, Now: func() time.Time { return testNow }})
	var cmds []tea.Cmd
	for i := 0; i < 8; i++ {
		cmds = append(cmds, m.loadPlan())
	}

	var wg stdsync.WaitGroup
	msgs := make([]tea.Msg, len(cmds))
	for i, cmd := range cmds {
		wg.Add(1)
		go func(i int, c tea.Cmd) {
			defer wg.Done()
			msgs[i] = c()
		}(i, cmd)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		mark := testNow.UTC().Format(time.RFC3339)
		for i := 0; i < 8; i++ {
			v := mark
			if i%2 == 1 {
				v = ""
			}
			_ = st2.SetSyncState(ctx, readmodel.PlanKey(readmodel.PlanTags, "w1"), v)
			_ = st2.SetSyncState(ctx, readmodel.PlanKey(readmodel.PlanTags, "w2"), v)
		}
	}()
	wg.Wait()

	// Deliver messages in reverse order: only the latest seq should be accepted.
	for i := len(msgs) - 1; i >= 0; i-- {
		_, _ = m.Update(msgs[i])
	}
}
