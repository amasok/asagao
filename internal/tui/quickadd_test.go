package tui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/domain/task"
	"github.com/amasok/asagao/internal/store"
)

// quickAddStore is fixtureStore with a user and two tags to name in the create prompt.
func quickAddStore(t *testing.T) *store.Store {
	t.Helper()
	st := fixtureStore(t)
	ctx := context.Background()
	if err := st.UpsertUsers(ctx, []asana.User{{GID: "u2", Name: "Partner Two", Email: "partner@example.com"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertTags(ctx, []asana.Ref{{GID: "g1", Name: "買い物"}, {GID: "g2", Name: "急ぎ"}}); err != nil {
		t.Fatal(err)
	}
	return st
}

// The create prompt (a) reads the assignee, due date and tags at the end of the name, and
// the status line says what it read. The tags go to the outbox with the new task.
func TestCreateTaskQuickAdd(t *testing.T) {
	st := quickAddStore(t)
	h := projectHarness(t, st, nil)
	h.key("a")
	h.typeText("カーテンを買う @partner 明日 #買い物 #急ぎ")
	h.key("enter")
	got := h.m.selectedTask()
	if got == nil || got.Name != "カーテンを買う" || got.DueOn != "2026-11-14" ||
		got.Assignee == nil || got.Assignee.GID != "u2" || !slices.Equal(got.Tags, []string{"急ぎ", "買い物"}) && !slices.Equal(got.Tags, []string{"買い物", "急ぎ"}) {
		t.Fatalf("created task: %+v", got)
	}
	if want := "担当: Partner Two / 期日: 2026-11-14 / タグ: #買い物 #急ぎ"; !strings.Contains(h.m.status, want) {
		t.Errorf("status %q should say %q", h.m.status, want)
	}
	ops, err := st.ListOps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, op := range ops {
		kinds = append(kinds, op.Kind)
	}
	if !slices.Equal(kinds, []string{store.OpCreateTask, store.OpAddTag, store.OpAddTag}) {
		t.Errorf("ops %q", kinds)
	}
}

// A token that does not resolve stays in the name, and the status line says so.
func TestCreateTaskQuickAddUnread(t *testing.T) {
	st := quickAddStore(t)
	h := projectHarness(t, st, nil)
	h.key("a")
	h.typeText("カーテンを買う #ない 明日")
	h.key("enter")
	got := h.m.selectedTask()
	if got == nil || got.Name != "カーテンを買う #ない" || got.DueOn != "2026-11-14" || len(got.Tags) != 0 {
		t.Fatalf("created task: %+v", got)
	}
	for _, want := range []string{"期日: 2026-11-14", "「#ない」は読めませんでした", "名前に残しました"} {
		if !strings.Contains(h.m.status, want) {
			t.Errorf("status %q should say %q", h.m.status, want)
		}
	}
	if tags, _ := st.ListTags(context.Background()); len(tags) != 2 {
		t.Errorf("no tag should be made: %+v", tags)
	}
}

// In My Tasks a new task is mine unless the prompt names someone else.
func TestCreateTaskQuickAddAssigneeInMyTasks(t *testing.T) {
	st := quickAddStore(t)
	if err := st.UpsertWorkspaces(context.Background(), []asana.Workspace{{GID: "w1", Name: "家"}}); err != nil {
		t.Fatal(err)
	}
	h := projectHarness(t, st, nil)
	h.key("h", "k", "k", "l") // back to My Tasks
	h.key("a")
	h.typeText("頼む @partner")
	h.key("enter")
	created := func(name string) *store.TaskDetail {
		t.Helper()
		ops, _ := st.ListOps(context.Background())
		for _, op := range ops {
			if op.Kind == store.OpCreateTask {
				if d, _ := st.GetTask(context.Background(), op.TaskGID); d != nil && d.Name == name {
					return d
				}
			}
		}
		t.Fatalf("no task %q created (ops %+v)", name, ops)
		return nil
	}
	if d := created("頼む"); d.Assignee == nil || d.Assignee.GID != "u2" {
		t.Errorf("assignee %+v", d.Assignee)
	}
	h.key("a")
	h.typeText("自分で")
	h.key("enter")
	if d := created("自分で"); d.Assignee == nil || d.Assignee.GID != "me" {
		t.Errorf("assignee %+v", d.Assignee)
	}
}

// The subtask prompt (S) reads them too.
func TestCreateSubtaskQuickAdd(t *testing.T) {
	st := quickAddStore(t)
	addSubtasks(t, st)
	h := projectHarness(t, st, nil)
	h.key("S")
	h.typeText("配送日を決める @me +3d #急ぎ")
	h.key("enter")
	got := h.m.selectedTask()
	if got == nil || got.Name != "配送日を決める" || got.DueOn != "2026-11-16" || got.ParentGID != "t1" ||
		got.Assignee == nil || got.Assignee.GID != "me" || !slices.Equal(got.Tags, []string{"急ぎ"}) {
		t.Fatalf("new subtask: %+v", got)
	}
	if want := "担当: 自分 / 期日: 2026-11-16 / タグ: #急ぎ"; !strings.Contains(h.m.status, want) {
		t.Errorf("status %q should say %q", h.m.status, want)
	}
}

// tagFailStore is a store whose tags cannot be added, with control characters in the error.
type tagFailStore struct{ *store.Store }

func (tagFailStore) ApplyTagEdit(context.Context, string, task.TagEdit, time.Time) error {
	return errors.New("disk\x1b]0;pwned\x07 full")
}

// When the tags cannot be added the task is still created and shown, and the status line
// says why, without the error's control characters.
func TestCreateTaskQuickAddTagError(t *testing.T) {
	st := quickAddStore(t)
	m := New(context.Background(), Deps{Store: tagFailStore{st}, Now: func() time.Time { return testNow }})
	h := &harness{t: t, m: m}
	h.send(tea.WindowSizeMsg{Width: 140, Height: 30})
	h.run(m.Init())
	h.key("h", "j", "j", "l")
	h.key("a")
	h.typeText("カーテンを買う #買い物")
	h.key("enter")
	if got := h.m.selectedTask(); got == nil || got.Name != "カーテンを買う" {
		t.Fatalf("the task should be created and selected: %+v", got)
	}
	if !strings.Contains(h.m.status, "タグを付けられませんでした: disk") || strings.ContainsAny(h.m.status, "\x1b\x07") {
		t.Errorf("status %q", h.m.status)
	}
}
