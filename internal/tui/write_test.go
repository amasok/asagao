package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/amasok/asana-tui/internal/store"
)

// The inline comment prompt (c) sends Markdown as rich text, like the editor (C), the CLI and MCP.
func TestInlineCommentIsRichText(t *testing.T) {
	h := newHarness(t, 140, 30)
	h.key("c")
	h.typeText("**区役所**は9時から")
	h.key("enter")
	d, err := h.m.deps.Store.GetTask(context.Background(), "t2")
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, s := range d.Stories {
		if s.Type == "comment" && s.Pending {
			found = true
			if s.Text != "区役所は9時から" || !strings.Contains(s.HTMLText, "<strong>区役所</strong>") {
				t.Fatalf("comment text=%q html=%q", s.Text, s.HTMLText)
			}
		}
	}
	if !found {
		t.Fatalf("no pending comment: %+v", d.Stories)
	}
}

// outboxAction runs an outbox key on the entry at index i and returns the resulting message.
func outboxAction(t *testing.T, h *harness, i int, key string) writeDoneMsg {
	t.Helper()
	h.run(h.m.loadOutbox())
	h.m.opCursor = i
	_, cmd := h.m.handleOutboxKey(key)
	if cmd == nil {
		t.Fatalf("%q gave no command", key)
	}
	msg, ok := cmd().(writeDoneMsg)
	if !ok {
		t.Fatalf("%q did not finish with writeDoneMsg", key)
	}
	return msg
}

func TestOutboxDiscardRefreshesOnlyTasks(t *testing.T) {
	h := newHarness(t, 140, 30)
	ctx, st := context.Background(), h.m.deps.Store
	if err := st.RenameSectionLocal(ctx, "s1", "家具2", testNow); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "ローカル"}, testNow); err != nil {
		t.Fatal(err)
	}
	// a section rename has no task to fetch (this used to call FetchDetails on the section gid)
	if msg := outboxAction(t, h, 0, "d"); msg.err != nil || msg.refresh != "" {
		t.Fatalf("section discard = %+v", msg)
	}
	if msg := outboxAction(t, h, 0, "d"); msg.err != nil || msg.refresh != "t1" {
		t.Fatalf("task discard = %+v", msg)
	}
	if ops, _ := st.ListOps(ctx); len(ops) != 0 {
		t.Fatalf("outbox should be empty: %+v", ops)
	}
}

func TestOutboxActionsCheckState(t *testing.T) {
	h := newHarness(t, 140, 30)
	ctx, st := context.Background(), h.m.deps.Store
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "ローカル"}, testNow); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"l", "s", "R"} { // conflict / conflict / failed only
		if msg := outboxAction(t, h, 0, key); msg.err == nil {
			t.Errorf("%q on a pending entry should be refused", key)
		}
	}
	ops, _ := st.ListOps(ctx)
	if len(ops) != 1 || ops[0].State != store.OpPending {
		t.Fatalf("refused actions must keep the entry: %+v", ops)
	}
	_ = st.MarkOpConflict(ctx, ops[0].ID, map[string]any{"name": "サーバー"})
	if msg := outboxAction(t, h, 0, "l"); msg.err != nil || msg.refresh != "" {
		t.Fatalf("keep local = %+v", msg)
	}
	if op, _ := st.GetOp(ctx, ops[0].ID); op.State != store.OpPending || op.Base["name"] != "サーバー" {
		t.Fatalf("keep local should requeue: %+v", op)
	}
}

func TestOutboxShowsSendingAndVerify(t *testing.T) {
	h := newHarness(t, 140, 30)
	ctx, st := context.Background(), h.m.deps.Store
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "ローカル"}, testNow); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateTaskLocal(ctx, store.NewTask{Name: "新規", WorkspaceGID: "w1"}, testNow); err != nil {
		t.Fatal(err)
	}
	ops, _ := st.ListOps(ctx)
	if ok, err := st.BeginSend(ctx, ops[0].ID, testNow); !ok || err != nil {
		t.Fatalf("BeginSend = %v, %v", ok, err)
	}
	if err := st.MarkOpVerify(ctx, ops[1].ID, "Asana への反映を待っています（次の同期で確かめます）"); err != nil {
		t.Fatal(err)
	}
	h.key("!")
	s := h.screen()
	for _, want := range []string{"[送信中]", "[自動確認中]", "Asana への反映を待っています", "操作は不要です"} {
		if !strings.Contains(s, want) {
			t.Errorf("outbox missing %q:\n%s", want, s)
		}
	}
	// the resolve keys do nothing on the entry being sent
	h.m.opCursor = 0
	for _, key := range []string{"l", "s", "d", "R"} {
		h.m.status = ""
		if _, cmd := h.m.handleOutboxKey(key); cmd != nil || !strings.Contains(h.m.status, "送信中") {
			t.Errorf("%q on a sending entry: cmd=%v status=%q", key, cmd != nil, h.m.status)
		}
	}
	if op, _ := st.GetOp(ctx, ops[0].ID); op.State != store.OpSending {
		t.Fatalf("the sending entry must be left alone: %+v", op)
	}
}

// Detail lines (conflict values, notes) start where the entry's description starts.
func TestOutboxDetailLinesAligned(t *testing.T) {
	h := newHarness(t, 120, 24)
	ctx, st := context.Background(), h.m.deps.Store
	_ = st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "ローカル"}, testNow)
	_ = st.UpdateTaskLocal(ctx, "t2", map[string]any{"name": "ローカル2"}, testNow)
	ops, _ := st.ListOps(ctx)
	_ = st.MarkOpConflict(ctx, ops[0].ID, map[string]any{"name": "サーバー"})
	_ = st.MarkOpVerify(ctx, ops[1].ID, "Asana への反映を待っています")
	h.key("!")
	col := func(sub string) int {
		t.Helper()
		for _, line := range strings.Split(h.screen(), "\n") {
			if i := strings.Index(line, sub); i >= 0 {
				return ansi.StringWidth(line[:i])
			}
		}
		t.Fatalf("%q not on screen:\n%s", sub, h.screen())
		return 0
	}
	desc := col("ローカル: 名前→")
	for _, detail := range []string{"名前: こちら=", "ローカル2: 名前→", "Asana への反映を待っています"} {
		if got := col(detail); got != desc {
			t.Errorf("%q starts at column %d, the description at %d", detail, got, desc)
		}
	}
}

// Changes dropped because their task was deleted in Asana are announced once, with what
// they were, and listed in the outbox for the rest of the session.
func TestDiscardedChangesAnnouncedOnce(t *testing.T) {
	h := newHarness(t, 140, 30)
	ctx, st := context.Background(), h.m.deps.Store
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "ソファを2台買う"}, testNow); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateTaskLocal(ctx, "t2", map[string]any{"completed": true}, testNow); err != nil {
		t.Fatal(err)
	}
	for _, gid := range []string{"t1", "t2"} {
		if _, err := st.DiscardDeletedTask(ctx, gid, "Asana で削除されていました", testNow); err != nil {
			t.Fatal(err)
		}
	}
	h.run(h.m.loadOutbox())
	if s := h.m.status; !strings.Contains(s, store.DiscardNotice) || !strings.Contains(s, "ソファを2台買う") || !strings.Contains(s, "ほか 1 件") {
		t.Fatalf("status = %q", s)
	}
	if ds, _ := st.PendingDiscards(ctx); len(ds) != 0 {
		t.Fatalf("announced changes should be marked as notified: %+v", ds)
	}
	h.m.status = ""
	h.run(h.m.loadOutbox())
	if h.m.status != "" {
		t.Fatalf("announced again: %q", h.m.status)
	}
	h.key("!")
	s := h.screen()
	if !strings.Contains(s, store.DiscardNotice) || strings.Count(s, "」: ") < 2 {
		t.Fatalf("outbox should list the dropped changes:\n%s", s)
	}
}
