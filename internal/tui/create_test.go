package tui

import (
	"context"
	"maps"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/store"
)

// createdPayload returns the payload of the only create_task in the outbox.
func createdPayload(t *testing.T, st *store.Store) map[string]any {
	t.Helper()
	ops, err := st.ListOps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var found []map[string]any
	for _, op := range ops {
		if op.Kind == "create_task" {
			found = append(found, op.Payload)
		}
	}
	if len(found) != 1 {
		t.Fatalf("want one create_task in the outbox, got %d: %+v", len(found), ops)
	}
	return found[0]
}

func samePayload(got, want map[string]any) bool {
	return maps.EqualFunc(got, want, func(a, b any) bool { return a == b })
}

// myTasksHarness opens My Tasks on st, which has a workspace (the fixture has none).
func myTasksHarness(t *testing.T, st *store.Store) *harness {
	t.Helper()
	if err := st.UpsertWorkspaces(context.Background(), []asana.Workspace{{GID: "w1", Name: "家"}}); err != nil {
		t.Fatal(err)
	}
	m := New(context.Background(), Deps{Store: st, Now: func() time.Time { return testNow }})
	h := &harness{t: t, m: m}
	h.send(tea.WindowSizeMsg{Width: 140, Height: 30})
	h.run(m.Init())
	return h
}

// In My Tasks, a new task goes to the default workspace, assigned to me, in no project.
func TestCreateTaskInMyTasks(t *testing.T) {
	h := myTasksHarness(t, fixtureStore(t))
	h.key("a")
	h.typeText("牛乳を買う due:明日")
	h.key("enter")
	if s := h.screen(); !strings.Contains(s, "タスクを作成しました: 牛乳を買う") {
		t.Fatalf("status:\n%s", s)
	}
	want := map[string]any{"workspace": "w1", "name": "牛乳を買う", "due_on": "2026-11-14", "assignee": "me"}
	if got := createdPayload(t, h.m.deps.Store.(*store.Store)); !samePayload(got, want) {
		t.Fatalf("payload = %v, want %v", got, want)
	}
}

// In a project with no task selected, a new task goes to the first section, assigned to
// no one.
func TestCreateTaskInEmptyProjectGoesToFirstSection(t *testing.T) {
	st := fixtureStore(t)
	ctx := context.Background()
	if err := st.UpsertProjects(ctx, "w1", []asana.Project{
		{GID: "p1", Name: "新居プロジェクト", Members: []asana.Ref{{GID: "me"}}},
		{GID: "p2", Name: "旅行", Members: []asana.Ref{{GID: "me"}}},
	}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceSections(ctx, "p2", []asana.Section{{GID: "s3", Name: "準備"}, {GID: "s4", Name: "当日"}}); err != nil {
		t.Fatal(err)
	}
	h := projectHarness(t, st, nil)
	for h.m.currentScope().project != "p2" {
		if h.m.scopeCursor >= len(h.m.scopes)-1 {
			t.Fatalf("project p2 not in the sidebar: %+v", h.m.scopes)
		}
		h.key("h", "j", "l")
	}
	if got := h.m.selectedTask(); got != nil {
		t.Fatalf("p2 should have no tasks: %+v", got)
	}
	h.key("a")
	h.typeText("宿を取る")
	h.key("enter")
	want := map[string]any{"workspace": "w1", "name": "宿を取る", "project": "p2", "section": "s3"}
	if got := createdPayload(t, st); !samePayload(got, want) {
		t.Fatalf("payload = %v, want %v", got, want)
	}
}

// Without my user in the cache, creating in My Tasks shows the store's error as it is and
// writes nothing.
func TestCreateTaskWithoutMeWritesNothing(t *testing.T) {
	st := fixtureStore(t)
	if err := st.SetMeGID(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	h := myTasksHarness(t, st)
	h.key("a")
	h.typeText("牛乳を買う")
	h.key("enter")
	if s := h.screen(); !strings.Contains(s, "エラー: 自分のユーザー情報がローカルストアにありません") {
		t.Fatalf("status:\n%s", s)
	}
	if ops, err := st.ListOps(context.Background()); err != nil || len(ops) != 0 {
		t.Fatalf("nothing should be queued: %+v %v", ops, err)
	}
}
