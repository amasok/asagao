package store

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asana-tui/internal/asana"
)

// A store from before the inbox (main's user_version, no notifications table) opens with
// its data intact and an empty inbox.
func TestMigrateStoreFromBeforeInbox(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "store.db")
	idx := -1
	for i, m := range migrations {
		if strings.Contains(m, "CREATE TABLE notifications") {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("no migration creates notifications")
	}

	st, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "残るタスク"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "未送信の変更"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	// Back to the state of a store written before this migration (and every later one).
	for _, q := range []string{"DROP TABLE notifications", fmt.Sprintf("PRAGMA user_version = %d", idx)} {
		if _, err := st.db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()

	st, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("open old store: %v", err)
	}
	defer st.Close()
	var v int
	if err := st.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil || v != len(migrations) {
		t.Fatalf("user_version %d %v, want %d", v, err, len(migrations))
	}
	if task, err := st.GetTask(ctx, "t1"); err != nil || task.Name != "未送信の変更" {
		t.Fatalf("task %+v %v", task, err)
	}
	if ops, _ := st.PendingOps(ctx); len(ops) != 1 {
		t.Fatalf("queued change lost: %+v", ops)
	}
	if ns, err := st.ListNotifications(ctx, NotificationFilter{}); err != nil || len(ns) != 0 {
		t.Fatalf("notifications %+v %v", ns, err)
	}
	if state, err := st.InboxState(ctx); err != nil || state.Available() || state.Status != "" {
		t.Fatalf("inbox state %+v %v", state, err)
	}
	if err := st.ReplaceNotifications(ctx, "w1", []asana.Notification{{GID: "n1"}}); err != nil {
		t.Fatalf("notifications not writable after migration: %v", err)
	}
}

// Reset clears the cached notifications and the inbox state with the rest.
func TestResetClearsInbox(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.ReplaceNotifications(ctx, "w1", []asana.Notification{{GID: "n1"}, {GID: "n2", IsRead: true}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetInboxState(ctx, InboxState{Status: InboxOK}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetInboxWriteForbidden(ctx, true); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkNotificationsLocal(ctx, []string{"n1"}, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Reset(ctx, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	if ns, _ := st.ListNotifications(ctx, NotificationFilter{}); len(ns) != 0 {
		t.Fatalf("notifications left after reset: %+v", ns)
	}
	if state, _ := st.InboxState(ctx); state.Status != "" || state.WriteForbidden {
		t.Fatalf("inbox state left after reset: %+v", state)
	}
}
