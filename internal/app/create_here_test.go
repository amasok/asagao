package app

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/amasok/asagao/internal/domain/task"
)

// CreateHere takes gids as they are: the only store calls are the ones the placement needs
// (no name lookups, no tags), and the task is written once, with the clock's time.
func TestCreateHere(t *testing.T) {
	for _, tc := range []struct {
		name  string
		in    CreateHereInput
		calls []string
		want  task.NewTask
	}{
		{"subtask", CreateHereInput{Parent: "123"},
			[]string{"CreateTaskLocal"},
			task.NewTask{ParentGID: "123"}},
		{"in a project and section", CreateHereInput{Project: "p9", Section: "s9"},
			[]string{"CreateTaskLocal"},
			task.NewTask{ProjectGID: "p9", SectionGID: "s9"}},
		{"in a project: the first section", CreateHereInput{Project: "p1"},
			[]string{"FirstSection p1", "CreateTaskLocal"},
			task.NewTask{ProjectGID: "p1", SectionGID: "s1"}},
		{"no project: the default workspace, me", CreateHereInput{},
			[]string{"DefaultWorkspace", "ResolveUser me", "CreateTaskLocal"},
			task.NewTask{WorkspaceGID: "w1", AssigneeGID: "u-me"}},
		{"a subtask ignores the project", CreateHereInput{Parent: "123", Project: "p1", Section: "s9"},
			[]string{"CreateTaskLocal"},
			task.NewTask{ParentGID: "123"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, text := newFake(), &fakeText{}
			tc.in.Name, tc.in.Due = "牛乳を買う", "2026-11-17"
			gid, err := newTasks(st, text).CreateHere(context.Background(), tc.in)
			if err != nil || gid != "local:1" {
				t.Fatalf("gid %q, err %v", gid, err)
			}
			if !slices.Equal(st.calls, tc.calls) {
				t.Errorf("calls %q, want %q", st.calls, tc.calls)
			}
			tc.want.Name, tc.want.DueOn = "牛乳を買う", "2026-11-17"
			if st.created[0] != tc.want {
				t.Errorf("created %+v, want %+v", st.created[0], tc.want)
			}
			if !st.createdAt[0].Equal(now) {
				t.Errorf("created at %v, want the first clock reading %v", st.createdAt[0], now)
			}
			if text.calls != 0 {
				t.Errorf("converted rich text %d times", text.calls)
			}
		})
	}
}

// The due date is not parsed again: it is already a date.
func TestCreateHereKeepsDue(t *testing.T) {
	st := newFake()
	if _, err := newTasks(st, &fakeText{}).CreateHere(context.Background(), CreateHereInput{Name: "a", Parent: "1", Due: "tomorrow"}); err != nil {
		t.Fatal(err)
	}
	if got := st.created[0].DueOn; got != "tomorrow" {
		t.Errorf("DueOn = %q", got)
	}
}

// A store error is returned as it is (not a *FieldError) and nothing is written.
func TestCreateHereErrorsWriteNothing(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*fakeStore)
		in    CreateHereInput
	}{
		{"no workspace", func(f *fakeStore) { f.workspace = "" }, CreateHereInput{}},
		{"no me", func(f *fakeStore) { delete(f.users, "me") }, CreateHereInput{}},
		{"first section fails", func(*fakeStore) {}, CreateHereInput{Project: "p9"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newFake()
			tc.setup(st)
			tc.in.Name = "a"
			gid, err := newTasks(st, &fakeText{}).CreateHere(context.Background(), tc.in)
			if err == nil || gid != "" {
				t.Fatalf("gid %q, err %v", gid, err)
			}
			if !errors.Is(err, errNotFound) {
				t.Errorf("err %v is not the store's", err)
			}
			var fe *FieldError
			if errors.As(err, &fe) {
				t.Errorf("err is a FieldError: %v", err)
			}
			if w := st.writes(); len(w) != 0 {
				t.Errorf("wrote %q", w)
			}
		})
	}

	// The store refuses the task.
	st := newFake()
	st.createErr = errors.New("タスク名が空です")
	if gid, err := newTasks(st, &fakeText{}).CreateHere(context.Background(), CreateHereInput{Parent: "1"}); err != st.createErr || gid != "" {
		t.Errorf("gid %q, err %v", gid, err)
	}
}

// An assignee given to CreateHere replaces the default (me outside a project, nobody in
// one), and tags are added to the new task after it is created.
func TestCreateHereAssigneeAndTags(t *testing.T) {
	hanako, nobody := "u-hanako", ""
	tags := []task.TagRef{{GID: "g1", Name: "急ぎ"}}
	for _, tc := range []struct {
		name     string
		in       CreateHereInput
		calls    []string
		assignee string
	}{
		{"no project, given", CreateHereInput{Assignee: &hanako},
			[]string{"DefaultWorkspace", "CreateTaskLocal"}, "u-hanako"},
		{"no project, nobody", CreateHereInput{Assignee: &nobody},
			[]string{"DefaultWorkspace", "CreateTaskLocal"}, ""},
		{"in a project, given", CreateHereInput{Project: "p9", Section: "s9", Assignee: &hanako},
			[]string{"CreateTaskLocal"}, "u-hanako"},
		{"subtask, given", CreateHereInput{Parent: "123", Assignee: &hanako},
			[]string{"CreateTaskLocal"}, "u-hanako"},
		{"tags", CreateHereInput{Project: "p9", Section: "s9", Tags: tags},
			[]string{"CreateTaskLocal", "ApplyTagEdit local:1"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newFake()
			tc.in.Name = "牛乳を買う"
			gid, err := newTasks(st, &fakeText{}).CreateHere(context.Background(), tc.in)
			if err != nil || gid != "local:1" {
				t.Fatalf("gid %q, err %v", gid, err)
			}
			if !slices.Equal(st.calls, tc.calls) {
				t.Errorf("calls %q, want %q", st.calls, tc.calls)
			}
			if got := st.created[0].AssigneeGID; got != tc.assignee {
				t.Errorf("assignee %q, want %q", got, tc.assignee)
			}
			if tc.in.Tags != nil {
				if len(st.tagged) != 1 || !slices.Equal(st.tagged[0].Add, tags) || !st.taggedAt[0].Equal(now) {
					t.Errorf("tagged %+v at %v", st.tagged, st.taggedAt)
				}
			}
		})
	}
}

// When the tags cannot be added, the task is still created: its gid comes with the error.
func TestCreateHereTagError(t *testing.T) {
	st := newFake()
	boom := errors.New("boom")
	st.applyErr = boom
	gid, err := newTasks(st, &fakeText{}).CreateHere(context.Background(),
		CreateHereInput{Name: "a", Project: "p9", Section: "s9", Tags: []task.TagRef{{GID: "g1"}}})
	if gid != "local:1" || !errors.Is(err, boom) {
		t.Fatalf("gid %q, err %v", gid, err)
	}
}
