package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/store"
)

// With the sections of My Tasks known, My Tasks is grouped by them (in the list's order)
// and m moves a task between them.
func TestMyTasksBySectionsAndMove(t *testing.T) {
	st := fixtureStore(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.UpsertWorkspaces(ctx, []asana.Workspace{{GID: "w1", Name: "WS"}}))
	must(st.ReplaceMyTaskSections(ctx, "w1", []asana.Section{{GID: "m1", Name: "今日やる"}, {GID: "m2", Name: "あとで"}}))
	in := func(sec string) model.OptionalRef { return model.OptionalRef{Set: true, Ref: &asana.Ref{GID: sec}} }
	me := &asana.Ref{GID: "me", Name: "Me"}
	must(st.UpsertTasks(ctx, []asana.Task{
		{GID: "t1", Name: "ソファを買う", DueOn: "2026-11-20", Assignee: me, Workspace: &asana.Ref{GID: "w1"}, AssigneeSection: in("m2")},
		{GID: "t2", Name: "転居届を出す", DueOn: "2026-11-10", Assignee: me, Workspace: &asana.Ref{GID: "w1"}, AssigneeSection: in("m1")},
	}))
	h := newHarnessWith(t, st)
	s := h.screen()
	i, j, k := strings.Index(s, "今日やる"), strings.Index(s, "あとで"), strings.Index(s, "セクション不明")
	if !(i >= 0 && i < j && j < k) || strings.Contains(s, "期限切れ") {
		t.Fatalf("My Tasks not grouped by its sections:\n%s", s)
	}
	if h.selected() != "t2" {
		t.Fatalf("selected %q, want t2 (今日やる first)", h.selected())
	}
	h.key("m")
	if h.m.picker == nil || h.m.picker.kind != pickMySection || len(h.m.picker.items) != 2 {
		t.Fatalf("picker = %+v", h.m.picker)
	}
	h.key("j", "enter") // あとで
	ops := h.ops()
	if len(ops) != 1 || ops[0].Kind != store.OpMoveMySection || ops[0].TaskGID != "t2" || ops[0].Payload["section"] != "m2" {
		t.Fatalf("ops = %+v", ops)
	}
	if d, _ := st.GetTask(ctx, "t2"); d.MySection == nil || d.MySection.GID != "m2" {
		t.Errorf("t2 = %+v", d.MySection)
	}
}

func newHarnessWith(t *testing.T, st *store.Store) *harness {
	t.Helper()
	m := New(context.Background(), Deps{Store: st, Now: func() time.Time { return testNow }})
	h := &harness{t: t, m: m}
	h.send(tea.WindowSizeMsg{Width: 140, Height: 30})
	h.run(m.Init())
	return h
}
