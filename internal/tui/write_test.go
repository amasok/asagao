package tui

import (
	"context"
	"strings"
	"testing"

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
	for _, want := range []string{"[送信中]", "[確認待ち]", "Asana への反映を待っています"} {
		if !strings.Contains(s, want) {
			t.Errorf("outbox missing %q:\n%s", want, s)
		}
	}
	// the entry being sent cannot be resolved
	if msg := outboxAction(t, h, 0, "d"); msg.err == nil || !strings.Contains(msg.err.Error(), "送信中") {
		t.Fatalf("discarding a sending entry = %+v", msg)
	}
}
