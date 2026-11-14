package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asana-tui/internal/asana"
	"github.com/amasok/asana-tui/internal/store"
)

func inboxHarness(t *testing.T, state store.InboxState) (*harness, *store.Store) {
	t.Helper()
	ctx := context.Background()
	st := fixtureStore(t)
	if err := st.ReplaceNotifications(ctx, "w1", []asana.Notification{
		{GID: "n2", CreatedAt: testNow.Add(-time.Hour), CreatedBy: &asana.Ref{GID: "u2", Name: "Partner"},
			StoryResourceSubtype: "comment_added", Story: &asana.Story{Text: "見ておいて"},
			WorkObjects: []asana.WorkRef{{GID: "t3", ResourceType: "task", Name: "電気の契約"}}},
		{GID: "n1", IsRead: true, CreatedAt: testNow.Add(-26 * time.Hour), CreatedBy: &asana.Ref{GID: "u2", Name: "Partner"},
			StoryResourceSubtype: "assigned", WorkObjects: []asana.WorkRef{{GID: "t1", ResourceType: "task", Name: "ソファを買う"}}},
		{GID: "n0", CreatedAt: testNow.Add(-27 * time.Hour),
			WorkObjects: []asana.WorkRef{{GID: "p1", ResourceType: "project", Name: "新居プロジェクト"}}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetInboxState(ctx, state); err != nil {
		t.Fatal(err)
	}
	m := New(ctx, Deps{Store: st, Now: func() time.Time { return testNow }})
	h := &harness{t: t, m: m}
	h.send(tea.WindowSizeMsg{Width: 140, Height: 30})
	h.run(m.Init())
	return h, st
}

func TestInboxListOpenAndMarkRead(t *testing.T) {
	h, st := inboxHarness(t, store.InboxState{Status: store.InboxOK})
	ctx := context.Background()
	h.key("h", "j", "l") // sidebar: 受信トレイ, then the list
	if sc := h.m.currentScope(); sc.kind != scopeInbox || !strings.Contains(sc.name, "(2)") {
		t.Fatalf("second scope = %+v", sc)
	}
	s := h.screen()
	for _, want := range []string{"受信トレイ (2)", "今日", "Partner コメント: 電気の契約 — 見ておいて", "昨日", "ソファを買う"} {
		if !strings.Contains(s, want) {
			t.Errorf("screen missing %q:\n%s", want, s)
		}
	}
	if h.selected() != "t3" {
		t.Fatalf("selected %q, want the task of the newest notification", h.selected())
	}

	h.key("enter") // opens the task and marks the notification read
	if !h.m.zoom || h.m.detailGID() != "t3" {
		t.Fatalf("enter should open the task: zoom=%v detail=%q", h.m.zoom, h.m.detailGID())
	}
	if n, _ := st.UnreadNotifications(ctx); n != 1 {
		t.Fatalf("unread = %d after enter", n)
	}
	ops, _ := st.ListOps(ctx)
	if len(ops) != 1 || ops[0].Kind != store.OpMarkNotification || ops[0].TaskGID != "n2" {
		t.Fatalf("ops %+v", ops)
	}
	h.key("esc")

	h.key("R") // toggle back to unread: the queued change is dropped
	if n, _ := st.UnreadNotifications(ctx); n != 2 {
		t.Fatalf("unread = %d after R", n)
	}
	if ops, _ := st.ListOps(ctx); len(ops) != 0 {
		t.Fatalf("ops after toggling back %+v", ops)
	}
	if h.m.selectedNotification() == nil || h.m.selectedNotification().GID != "n2" {
		t.Fatal("selection should stay on the notification")
	}

	h.key("space", "Y") // all read
	if n, _ := st.UnreadNotifications(ctx); n != 0 {
		t.Fatalf("unread = %d after mark all", n)
	}

	// A notification about a project switches to that project.
	h.key("G", "enter")
	if sc := h.m.currentScope(); sc.kind != scopeProject || sc.project != "p1" {
		t.Fatalf("scope after opening a project notification = %+v", sc)
	}
}

func TestInboxUnavailableShowsRecentWithNotice(t *testing.T) {
	h, _ := inboxHarness(t, store.InboxState{Status: store.InboxForbidden, Error: "asana: 403 Forbidden"})
	h.key("h", "j", "l")
	if sc := h.m.currentScope(); sc.kind != scopeRecent {
		t.Fatalf("second scope = %+v", sc)
	}
	s := h.screen()
	if !strings.Contains(s, "最近の更新") || !strings.Contains(s, "受信トレイ（Asana の通知）を見る権限がありません") {
		t.Fatalf("screen should explain the fallback:\n%s", s)
	}
	if c := h.m.listCursor; c >= 0 && h.m.rows[c].note {
		t.Fatal("the notice must not take the cursor")
	}
	h.key("space", "R")
	if !strings.Contains(h.m.status, "使えません") {
		t.Fatalf("inbox actions should not be available in recent updates: %q", h.m.status)
	}
}
