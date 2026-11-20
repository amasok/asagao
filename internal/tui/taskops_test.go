package tui

import (
	"context"
	"strings"
	"testing"
)

// #32: deleting a task asks first; "やめる" queues nothing, "削除する" queues the delete
// and the task leaves the list.
func TestDeleteTaskAsksFirst(t *testing.T) {
	st := fixtureStore(t)
	ctx := context.Background()
	h := projectHarness(t, st, nil)
	sel := h.m.selectedTask()
	if sel == nil {
		t.Fatal("no task selected")
	}
	gid, name := sel.GID, sel.Name
	h.key("space", "Z")
	if p := h.m.picker; p == nil || p.kind != pickDeleteTask || !strings.Contains(p.title, name) {
		t.Fatalf("no confirmation:\n%s", h.screen())
	}
	h.key("enter") // やめる
	if ops, _ := st.ListOps(ctx); len(ops) != 0 {
		t.Fatalf("queued after やめる: %+v", ops)
	}
	h.key("space", "Z", "j", "enter") // 削除する
	ops, _ := st.ListOps(ctx)
	if len(ops) != 1 || ops[0].Kind != "delete_task" || ops[0].TaskGID != gid {
		t.Fatalf("outbox %+v", ops)
	}
	h.screen()
	for _, r := range h.m.list.rows {
		if r.task != nil && r.task.GID == gid {
			t.Errorf("%s still listed:\n%s", name, h.screen())
		}
	}
	if !strings.Contains(h.m.status, "削除しました") {
		t.Errorf("status %q", h.m.status)
	}
}

func TestSetParentFromTheMenu(t *testing.T) {
	st := fixtureStore(t)
	ctx := context.Background()
	h := projectHarness(t, st, nil)
	sel := h.m.selectedTask()
	if sel == nil {
		t.Fatal("no task selected")
	}
	gid := sel.GID
	h.key("space", "p")
	p := h.m.picker
	if p == nil || p.kind != pickParent || len(p.items) == 0 {
		t.Fatalf("no parent chooser:\n%s", h.screen())
	}
	for _, it := range p.items {
		if it.value == gid {
			t.Fatalf("the task itself is offered: %+v", p.items)
		}
	}
	parent := p.items[0].value
	h.key("enter")
	d, err := st.GetTask(ctx, gid)
	if err != nil || d.ParentGID != parent {
		t.Fatalf("parent %q, want %q (%v)\n%s", d.ParentGID, parent, err, h.m.status)
	}
	ops, _ := st.ListOps(ctx)
	if len(ops) != 1 || ops[0].Kind != "set_parent" {
		t.Fatalf("outbox %+v", ops)
	}
}
