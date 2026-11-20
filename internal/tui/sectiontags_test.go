package tui

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/domain/plan"
	"github.com/amasok/asagao/internal/store"
)

// openProjectList opens 新居プロジェクト (p1) as a list, on t1 (家具).
func openProjectList(t *testing.T, st *store.Store) *harness {
	t.Helper()
	m := New(context.Background(), onStore(st, Deps{Now: func() time.Time { return testNow }}))
	h := &harness{t: t, m: m}
	h.send(tea.WindowSizeMsg{Width: 140, Height: 30})
	h.run(m.Init())
	h.key("h", "j", "j", "l")
	if h.m.currentProject() != "p1" || h.selected() != "t1" {
		t.Fatalf("not on t1 in p1: project %q, selected %q", h.m.currentProject(), h.selected())
	}
	return h
}

func typeText(h *harness, s string) {
	for _, r := range s {
		h.key(string(r))
	}
}

func opKindsOf(h *harness) []store.Kind {
	var out []store.Kind
	for _, op := range h.ops() {
		out = append(out, op.Kind)
	}
	return out
}

// A section is added after the one the cursor is in, shown at once even though it is
// empty, and can be selected and deleted (after confirming). Never sent, nothing is left
// to send.
func TestTUIAddSelectAndDeleteSection(t *testing.T) {
	st := fixtureStore(t)
	h := openProjectList(t, st)
	h.key("space", "n")
	typeText(h, "保留")
	h.key("enter")
	if got := opKindsOf(h); !slices.Equal(got, []store.Kind{store.OpCreateSection}) {
		t.Fatalf("ops = %v (status %q)", got, h.m.status)
	}
	s := h.screen()
	if i, j, k := strings.Index(s, "家具"), strings.Index(s, "保留"), strings.Index(s, "手続き"); !(i >= 0 && i < j && j < k) {
		t.Fatalf("the empty section is not shown after 家具:\n%s", s)
	}
	h.key("j") // from t1 to the empty 保留
	if _, gid, name := h.m.sectionHere(); name != "保留" || !store.IsLocalGID(gid) {
		t.Fatalf("on %q (%s), want the new section", name, gid)
	}
	h.key("space", "H")
	if h.m.picker == nil || !strings.Contains(h.m.picker.title, "保留") {
		t.Fatalf("no confirmation: %+v", h.m.picker)
	}
	h.key("enter") // やめる is first
	if got := opKindsOf(h); len(got) != 1 {
		t.Fatalf("cancelled, but ops = %v", got)
	}
	h.key("space", "H", "j", "enter")
	if got := opKindsOf(h); len(got) != 0 {
		t.Fatalf("ops = %v, want none (created and deleted offline)", got)
	}
	for _, r := range h.m.list.rows {
		if r.header == "保留" {
			t.Error("deleted section still shown")
		}
	}
}

// A section with tasks is not deleted; moving one down queues the move.
func TestTUIMoveSectionAndRefuseNonEmpty(t *testing.T) {
	st := fixtureStore(t)
	h := openProjectList(t, st)
	h.key("space", "H", "j", "enter")
	if got := opKindsOf(h); len(got) != 0 || !strings.Contains(h.m.status, "タスクが残って") {
		t.Fatalf("ops = %v, status %q", got, h.m.status)
	}
	h.key("space", "J")
	ops := h.ops()
	if len(ops) != 1 || ops[0].Kind != store.OpInsertSection || ops[0].TaskGID != "s1" || ops[0].Payload["after"] != "s2" {
		t.Fatalf("ops = %+v", ops)
	}
	s := h.screen()
	if strings.Index(s, "手続き") > strings.Index(s, "家具") {
		t.Errorf("家具 not moved below 手続き:\n%s", s)
	}
	h.key("space", "J") // already last
	if len(h.ops()) != 1 || !strings.Contains(h.m.status, "これ以上") {
		t.Errorf("ops = %v, status %q", opKindsOf(h), h.m.status)
	}
}

// Tags: rename, recolor and delete (after confirming) from the tag manager.
func TestTUITagManager(t *testing.T) {
	st := fixtureStore(t)
	ctx := context.Background()
	if err := st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "ソファを買う", DueOn: "2026-11-20", Assignee: &asana.Ref{GID: "me"},
		Tags:        []asana.Ref{{GID: "g1", Name: "急ぎ"}},
		Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}, Section: &asana.Ref{GID: "s1"}}}}}); err != nil {
		t.Fatal(err)
	}
	h := openProjectList(t, st)
	h.key("space", "y", "enter") // 急ぎ
	if h.m.picker == nil || h.m.picker.kind != pickTagAction {
		t.Fatalf("picker = %+v", h.m.picker)
	}
	h.key("j", "enter") // 色を変える
	h.key("j", "j", "enter")
	if g, _ := st.GetTag(ctx, "g1"); g.Color != "dark-green" {
		t.Fatalf("g1 = %+v (status %q)", g, h.m.status)
	}
	h.key("space", "y", "enter", "j", "j", "enter") // 削除する
	if h.m.picker == nil || !strings.Contains(h.m.picker.title, "1 件") {
		t.Fatalf("no confirmation with the task count: %+v", h.m.picker)
	}
	h.key("j", "enter")
	if _, err := st.GetTag(ctx, "g1"); err == nil {
		t.Fatal("tag not deleted")
	}
	if got := opKindsOf(h); !slices.Equal(got, []store.Kind{store.OpDeleteTag}) {
		t.Errorf("ops = %v (the unsent color change goes with the tag)", got)
	}
}

// After Asana refused a feature with a 402 its entries leave the menu.
func TestTUIHidesUnavailableFeatures(t *testing.T) {
	st := fixtureStore(t)
	h := openProjectList(t, st)
	labels := func() string { // what the menu shows, on all its pages
		s := strings.Join(menuScreens(t, h), "\n")
		h.key("esc")
		return s
	}
	if !strings.Contains(labels(), "セクションを追加") {
		t.Fatalf("menu without セクションを追加:\n%s", labels())
	}
	ctx := context.Background()
	// A record before #50 (for every workspace, for good) is not read any more.
	if err := st.SetSyncState(ctx, "plan_unavailable:"+plan.Sections, "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	h.run(h.m.loadOutbox())
	if !strings.Contains(labels(), "セクションを追加") {
		t.Fatalf("an old record hid セクションを追加:\n%s", labels())
	}
	// A recent 402 in every workspace hides it.
	wss, err := st.WorkspaceGIDs(ctx)
	if err != nil || len(wss) == 0 {
		t.Fatalf("workspaces = %v, %v", wss, err)
	}
	for _, ws := range wss {
		if err := st.RecordPlanRefusal(ctx, plan.Sections, ws, h.m.deps.Now()); err != nil {
			t.Fatal(err)
		}
	}
	h.run(h.m.loadOutbox())
	s := labels()
	if strings.Contains(s, "セクションを追加") || strings.Contains(s, "空のセクションを削除") {
		t.Errorf("unavailable entries still in the menu:\n%s", s)
	}
	if !strings.Contains(s, "セクション名を変更") || !strings.Contains(s, "タグの名前・色") {
		t.Errorf("other entries gone:\n%s", s)
	}
	h.key("space", "n")
	if got := opKindsOf(h); len(got) != 0 || h.m.prompt.mode != promptNone {
		t.Errorf("n still works: ops %v, prompt %v", got, h.m.prompt.mode)
	}
}

// #68: without any write, sync or restart, plan-gated menu items reappear once
// plan.RecheckAfter (24h) has passed when opening the menu.
func TestTUIPlanMenuReappearsAfterRecheckWithoutWrites(t *testing.T) {
	st := fixtureStore(t)
	ctx := context.Background()
	if err := st.UpsertWorkspaces(ctx, []asana.Workspace{{GID: "w1", Name: "W1"}}); err != nil {
		t.Fatal(err)
	}
	now := testNow
	wss, err := st.WorkspaceGIDs(ctx)
	if err != nil || len(wss) == 0 {
		t.Fatalf("workspaces = %v, %v", wss, err)
	}
	ws, err := st.DefaultWorkspace(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range wss {
		for _, f := range []string{plan.Sections, plan.Tags, plan.TimeTracking} {
			if err := st.RecordPlanRefusal(ctx, f, w, now); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, f := range []string{plan.Goals, plan.Portfolios, plan.CustomFields} {
		if err := st.RecordPlanRefusal(ctx, f, ws, now); err != nil {
			t.Fatal(err)
		}
	}

	m := New(ctx, onStore(st, Deps{Now: func() time.Time { return now }}))
	h := &harness{t: t, m: m}
	h.send(tea.WindowSizeMsg{Width: 140, Height: 30})
	h.run(m.Init())
	h.key("h", "j", "j", "l") // open p1 on t1
	if h.m.currentProject() != "p1" || h.selected() != "t1" {
		t.Fatalf("not on t1 in p1: project %q, selected %q", h.m.currentProject(), h.selected())
	}

	labels := func() string {
		s := strings.Join(menuScreens(t, h), "\n")
		h.key("esc")
		return s
	}
	wantLabels := []string{
		"セクションを追加",
		"空のセクションを削除",
		"タグの名前・色",
		"作業時間を見る",
		"ゴール（期間・進み具合）",
		"ポートフォリオ（自分がオーナーのもの）",
		"カスタムフィールドを追加",
	}

	// Just before 24h: all 6 features are still hidden.
	now = testNow.Add(plan.RecheckAfter - time.Second)
	before := labels()
	for _, want := range wantLabels {
		if strings.Contains(before, want) {
			t.Errorf("before 24h: %q still in menu:\n%s", want, before)
		}
	}
	h.key("space", "n")
	if h.m.prompt.mode != promptNone {
		t.Fatalf("before 24h: space n opened prompt %v", h.m.prompt.mode)
	}

	// At 24h, without any write or manual loadPlan/loadOutbox: opening the menu re-offers all of them.
	now = testNow.Add(plan.RecheckAfter)
	after := labels()
	for _, want := range wantLabels {
		if !strings.Contains(after, want) {
			t.Errorf("after 24h: %q still hidden in menu:\n%s", want, after)
		}
	}
	// And the menu action itself works again.
	h.key("space", "n")
	if h.m.prompt.mode != promptNewSection {
		t.Fatalf("after 24h: space n did not open promptNewSection (got %v, status %q)", h.m.prompt.mode, h.m.status)
	}
}

// #68: across multiple workspaces, a feature reappears as soon as the earliest workspace's
// 402 expires (even if another workspace's 402 is newer), and a stale planLoadedMsg does
// not overwrite newer state.
func TestTUIPlanMultiWorkspaceRecheckAndStaleMsg(t *testing.T) {
	st := fixtureStore(t)
	ctx := context.Background()
	if err := st.UpsertWorkspaces(ctx, []asana.Workspace{{GID: "w1", Name: "W1"}, {GID: "w2", Name: "W2"}}); err != nil {
		t.Fatal(err)
	}
	now := testNow.Add(2 * time.Hour)
	if err := st.RecordPlanRefusal(ctx, plan.Tags, "w1", testNow); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordPlanRefusal(ctx, plan.Tags, "w2", now); err != nil {
		t.Fatal(err)
	}
	m := New(ctx, onStore(st, Deps{Now: func() time.Time { return now }}))
	h := &harness{t: t, m: m}
	h.send(tea.WindowSizeMsg{Width: 140, Height: 30})
	h.run(m.Init())

	if h.m.planOK(plan.Tags) {
		t.Fatal("PlanTags offered while both workspaces have active 402s")
	}
	// 23h59m after w1's 402: still hidden.
	now = testNow.Add(plan.RecheckAfter - time.Minute)
	if h.m.planOK(plan.Tags) {
		t.Fatal("PlanTags offered before earliest 402 expired")
	}
	// 24h after w1's 402 (still only 22h after w2's): w1 can be tried again -> offered!
	now = testNow.Add(plan.RecheckAfter)
	if !h.m.planOK(plan.Tags) {
		t.Fatal("PlanTags not offered once w1's 402 expired")
	}
	// Stale planLoadedMsg with older seq must not re-hide it.
	h.send(planLoadedMsg{seq: h.m.planSeq - 1, off: map[string]planWindow{
		plan.Tags: {from: now, until: now.Add(plan.RecheckAfter)},
	}})
	if !h.m.planOK(plan.Tags) {
		t.Fatal("stale planLoadedMsg overwrote current planOff")
	}
}
