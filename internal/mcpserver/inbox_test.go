package mcpserver

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/store"
)

func seedInbox(t *testing.T, state store.InboxState) *store.Store {
	t.Helper()
	st := seed(t)
	ctx := context.Background()
	ns := []asana.Notification{
		{GID: "5550001", CreatedAt: testNow.Add(-time.Minute), StoryResourceSubtype: "comment_added",
			WorkObjects: []asana.WorkRef{{GID: "t1", ResourceType: "task", Name: "old name"}}},
		{GID: "5550002", IsRead: true, CreatedAt: testNow.Add(-time.Hour),
			WorkObjects: []asana.WorkRef{{GID: "tz", ResourceType: "task", Name: "Not cached"}}},
		{GID: "5560002", CreatedAt: testNow.Add(-2 * time.Hour)},
	}
	if err := st.ReplaceNotifications(ctx, "w1", ns); err != nil {
		t.Fatal(err)
	}
	if err := st.SetInboxState(ctx, state); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestListInbox(t *testing.T) {
	st := seedInbox(t, store.InboxState{Status: store.InboxOK})
	cs := connect(t, &Server{Store: st})
	var out InboxOutput
	call(t, cs, "list_inbox", map[string]any{}, &out)
	if !out.Available || out.Unread != 2 || len(out.Notifications) != 3 || out.Notice != "" {
		t.Fatalf("list_inbox %+v", out)
	}
	if n := out.Notifications[0]; n.GID != "5550001" || n.TargetName != "ソファを買う" || !n.TaskCached || n.Read {
		t.Fatalf("first %+v (the cached task's name should be shown)", n)
	}
	if n := out.Notifications[1]; n.TaskCached || n.TargetName != "Not cached" {
		t.Fatalf("second %+v", n)
	}
	call(t, cs, "list_inbox", map[string]any{"unread": true, "limit": 1}, &out)
	if len(out.Notifications) != 1 || out.Notifications[0].GID != "5550001" || out.Unread != 2 {
		t.Fatalf("unread limit 1: %+v", out)
	}
}

func TestListInboxFallsBack(t *testing.T) {
	st := seedInbox(t, store.InboxState{Status: store.InboxPayment, Error: "Payment Required"})
	if err := st.UpsertTasks(context.Background(), []asana.Task{{GID: "t9", Name: "最近のタスク", ModifiedAt: testNow.Add(-time.Hour)}}); err != nil {
		t.Fatal(err)
	}
	cs := connect(t, &Server{Store: st})
	var out InboxOutput
	call(t, cs, "list_inbox", map[string]any{}, &out)
	if out.Available || !strings.Contains(out.Notice, "プラン") || len(out.Notifications) != 0 || out.Notifications == nil {
		t.Fatalf("list_inbox %+v", out)
	}
	found := false
	for _, r := range out.Recent {
		found = found || r.GID == "t9"
	}
	if !found {
		t.Fatalf("recent lacks the task modified an hour ago: %+v", out.Recent)
	}
}

func TestMarkNotificationsOffline(t *testing.T) {
	st := seedInbox(t, store.InboxState{Status: store.InboxOK})
	cs := connect(t, &Server{Store: st}) // no syncer: queued only
	ctx := context.Background()

	var w MarkNotificationsOutput
	call(t, cs, "mark_notifications", map[string]any{"gids": []string{"0001"}}, &w)
	if w.Changed != 1 || w.Sent {
		t.Fatalf("mark %+v", w)
	}
	if ops, _ := st.PendingOps(ctx); len(ops) != 1 || ops[0].Kind != store.OpMarkNotification || ops[0].TaskGID != "5550001" {
		t.Fatalf("ops %+v", ops)
	}
	call(t, cs, "mark_notifications", map[string]any{"gids": []string{"5550001"}}, &w)
	if w.Changed != 0 {
		t.Fatalf("already read: %+v", w)
	}
	call(t, cs, "mark_notifications", map[string]any{"all": true, "read": false}, &w)
	if w.Changed != 2 { // 5550001 back to unread, 5550002 unread
		t.Fatalf("all unread %+v", w)
	}
	if n, _ := st.UnreadNotifications(ctx); n != 3 {
		t.Fatalf("unread %d", n)
	}

	for _, args := range []map[string]any{{}, {"gids": []string{"0002"}}, {"gids": []string{"nope"}}} {
		if res := call(t, cs, "mark_notifications", args, nil); !res.IsError {
			t.Errorf("%v should fail", args)
		}
	}

	if err := st.SetInboxWriteForbidden(ctx, true); err != nil {
		t.Fatal(err)
	}
	// Refused before: a mark the user asks for is still queued (tried once when sent).
	var marked MarkNotificationsOutput
	if res := call(t, cs, "mark_notifications", map[string]any{"all": true}, &marked); res.IsError || marked.Changed == 0 {
		t.Fatalf("mark while refused: %+v %+v", res, marked)
	}
	var out InboxOutput
	call(t, cs, "list_inbox", map[string]any{}, &out)
	if !out.Available || !strings.Contains(out.Notice, "notifications:write") {
		t.Fatalf("list while refused %+v", out)
	}
}

func TestReadOnlyHidesMarkNotifications(t *testing.T) {
	st := seedInbox(t, store.InboxState{Status: store.InboxOK})
	cs := connect(t, &Server{Store: st, ReadOnly: true})
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "mark_notifications", Arguments: map[string]any{"all": true}})
	if err == nil && !res.IsError {
		t.Fatal("mark_notifications callable in read-only mode")
	}
	var out InboxOutput
	call(t, cs, "list_inbox", map[string]any{}, &out)
	if len(out.Notifications) != 3 {
		t.Fatalf("list_inbox read-only %+v", out)
	}
	if n, _ := st.UnreadNotifications(context.Background()); n != 2 {
		t.Fatal("read-only server changed the store")
	}
}
