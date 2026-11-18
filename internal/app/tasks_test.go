package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/domain/task"
	"github.com/amasok/asagao/internal/readmodel"
)

// errNotFound is the store's "not in the cache" (Tasks looks such a user up in Asana).
var errNotFound = readmodel.ErrNotFound

// fakeStore records the calls Create makes, in order, and resolves names from its maps.
type fakeStore struct {
	calls      []string
	projects   map[string]string // arg -> gid
	sections   map[string]string // projectGID+"/"+arg -> gid
	first      map[string]string // projectGID -> first section gid
	workspace  string
	users      map[string]string // arg -> gid ("" resolves to "")
	knownTags  map[string]string // name -> gid
	createErr  error
	applyErr   error
	created    []task.NewTask
	createdAt  []time.Time
	tagged     []task.TagEdit
	taggedGIDs []string
	taggedAt   []time.Time
}

func (f *fakeStore) log(format string, a ...any) {
	f.calls = append(f.calls, fmt.Sprintf(format, a...))
}

func (f *fakeStore) ResolveProject(_ context.Context, arg string) (string, error) {
	f.log("ResolveProject %s", arg)
	if gid, ok := f.projects[arg]; ok {
		return gid, nil
	}
	return "", fmt.Errorf("project %q: %w", arg, errNotFound)
}

func (f *fakeStore) ResolveSection(_ context.Context, projectGID, arg string) (string, error) {
	f.log("ResolveSection %s %s", projectGID, arg)
	if gid, ok := f.sections[projectGID+"/"+arg]; ok {
		return gid, nil
	}
	return "", fmt.Errorf("section %q: %w", arg, errNotFound)
}

func (f *fakeStore) FirstSection(_ context.Context, projectGID string) (string, error) {
	f.log("FirstSection %s", projectGID)
	if gid, ok := f.first[projectGID]; ok {
		return gid, nil
	}
	return "", fmt.Errorf("first section of %q: %w", projectGID, errNotFound)
}

func (f *fakeStore) DefaultWorkspace(context.Context) (string, error) {
	f.log("DefaultWorkspace")
	if f.workspace == "" {
		return "", fmt.Errorf("workspace: %w", errNotFound)
	}
	return f.workspace, nil
}

func (f *fakeStore) ResolveUser(_ context.Context, arg string) (string, error) {
	f.log("ResolveUser %s", arg)
	if arg == "" {
		return "", nil
	}
	if gid, ok := f.users[arg]; ok {
		return gid, nil
	}
	return "", fmt.Errorf("user %q: %w", arg, errNotFound)
}

func (f *fakeStore) ResolveTagEdit(_ context.Context, add, remove []string) (task.TagEdit, error) {
	f.log("ResolveTagEdit %s", strings.Join(add, ","))
	var e task.TagEdit
	for _, a := range add {
		switch gid, ok := f.knownTags[a]; {
		case ok:
			e.Add = append(e.Add, task.TagRef{GID: gid})
		case strings.TrimSpace(a) == "":
			return task.TagEdit{}, fmt.Errorf("add tag %q: タグ名が空です", a)
		default:
			e.Add = append(e.Add, task.TagRef{Name: a})
		}
	}
	return e, nil
}

func (f *fakeStore) CreateTaskLocal(_ context.Context, nt task.NewTask, now time.Time) (string, error) {
	f.log("CreateTaskLocal")
	if f.createErr != nil {
		return "", f.createErr
	}
	f.created = append(f.created, nt)
	f.createdAt = append(f.createdAt, now)
	return fmt.Sprintf("local:%d", len(f.created)), nil
}

func (f *fakeStore) ApplyTagEdit(_ context.Context, gid string, e task.TagEdit, now time.Time) error {
	f.log("ApplyTagEdit %s", gid)
	if f.applyErr != nil {
		return f.applyErr
	}
	f.tagged = append(f.tagged, e)
	f.taggedGIDs = append(f.taggedGIDs, gid)
	f.taggedAt = append(f.taggedAt, now)
	return nil
}

// fakeText marks what it converted, so a test can see which output went where.
type fakeText struct{ calls int }

func (t *fakeText) TaskNotes(md string) (html, plain string) {
	t.calls++
	return "<body>" + md + "</body>", "plain:" + md
}

var now = time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)

func newFake() *fakeStore {
	return &fakeStore{
		projects:  map[string]string{"新居": "p1"},
		sections:  map[string]string{"p1/家具": "s2"},
		first:     map[string]string{"p1": "s1"},
		workspace: "w1",
		users:     map[string]string{"me": "u-me", "花子": "u-hanako"},
		knownTags: map[string]string{"急ぎ": "t1"},
	}
}

func newTasks(st *fakeStore, text *fakeText) *Tasks {
	clock := 0
	return &Tasks{Store: st, Text: text, Clock: func() time.Time { clock++; return now.Add(time.Duration(clock-1) * time.Hour) }}
}

// writes returns the store calls that change something.
func (f *fakeStore) writes() []string {
	var w []string
	for _, c := range f.calls {
		if strings.HasPrefix(c, "CreateTaskLocal") || strings.HasPrefix(c, "ApplyTagEdit") {
			w = append(w, c)
		}
	}
	return w
}

func TestCreateAssigneeDefault(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   CreateTaskInput
		want string // AssigneeGID
	}{
		{"no project: me", CreateTaskInput{}, "u-me"},
		{"no project, given", CreateTaskInput{Assignee: "花子"}, "u-hanako"},
		{"in a project: nobody", CreateTaskInput{Project: "新居"}, ""},
		{"in a project, given", CreateTaskInput{Project: "新居", Assignee: "me"}, "u-me"},
		{"subtask: nobody", CreateTaskInput{Parent: "123"}, ""},
		{"subtask, given", CreateTaskInput{Parent: "123", Assignee: "花子"}, "u-hanako"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newFake()
			tc.in.Name = "牛乳を買う"
			if _, err := newTasks(st, &fakeText{}).Create(context.Background(), tc.in); err != nil {
				t.Fatal(err)
			}
			if got := st.created[0].AssigneeGID; got != tc.want {
				t.Errorf("AssigneeGID = %q, want %q (calls %q)", got, tc.want, st.calls)
			}
		})
	}
}

func TestCreateResolvesInOrder(t *testing.T) {
	for _, tc := range []struct {
		name  string
		in    CreateTaskInput
		calls []string
		want  task.NewTask
	}{
		{
			"my task",
			CreateTaskInput{Name: "a"},
			[]string{"DefaultWorkspace", "ResolveUser me", "ResolveTagEdit ", "CreateTaskLocal", "ApplyTagEdit local:1"},
			task.NewTask{Name: "a", WorkspaceGID: "w1", AssigneeGID: "u-me"},
		},
		{
			"first section",
			CreateTaskInput{Name: "a", Project: "新居"},
			[]string{"ResolveProject 新居", "FirstSection p1", "ResolveUser ", "ResolveTagEdit ", "CreateTaskLocal", "ApplyTagEdit local:1"},
			task.NewTask{Name: "a", ProjectGID: "p1", SectionGID: "s1"},
		},
		{
			"named section",
			CreateTaskInput{Name: "a", Project: "新居", Section: "家具", Assignee: "花子"},
			[]string{"ResolveProject 新居", "ResolveSection p1 家具", "ResolveUser 花子", "ResolveTagEdit ", "CreateTaskLocal", "ApplyTagEdit local:1"},
			task.NewTask{Name: "a", ProjectGID: "p1", SectionGID: "s2", AssigneeGID: "u-hanako"},
		},
		{
			// a subtask ignores the project and section it was given
			"subtask",
			CreateTaskInput{Name: "a", Parent: "123", Project: "新居", Section: "家具"},
			[]string{"ResolveUser ", "ResolveTagEdit ", "CreateTaskLocal", "ApplyTagEdit local:1"},
			task.NewTask{Name: "a", ParentGID: "123"},
		},
		{
			"tags resolved before, added after",
			CreateTaskInput{Name: "a", Project: "新居", Tags: []string{"急ぎ", "新しい"}},
			[]string{"ResolveProject 新居", "FirstSection p1", "ResolveUser ", "ResolveTagEdit 急ぎ,新しい", "CreateTaskLocal", "ApplyTagEdit local:1"},
			task.NewTask{Name: "a", ProjectGID: "p1", SectionGID: "s1"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newFake()
			gid, err := newTasks(st, &fakeText{}).Create(context.Background(), tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if gid != "local:1" {
				t.Errorf("gid = %q", gid)
			}
			if !slices.Equal(st.calls, tc.calls) {
				t.Errorf("calls\n got %q\nwant %q", st.calls, tc.calls)
			}
			if st.created[0] != tc.want {
				t.Errorf("created\n got %+v\nwant %+v", st.created[0], tc.want)
			}
		})
	}
}

func TestCreateNotesAndDue(t *testing.T) {
	st, text := newFake(), &fakeText{}
	_, err := newTasks(st, text).Create(context.Background(), CreateTaskInput{Name: "a", Notes: "# 見出し", Due: "tomorrow"})
	if err != nil {
		t.Fatal(err)
	}
	nt := st.created[0]
	if nt.HTMLNotes != "<body># 見出し</body>" || nt.Notes != "plain:# 見出し" {
		t.Errorf("notes: html %q, plain %q", nt.HTMLNotes, nt.Notes)
	}
	if nt.DueOn != "2026-11-17" {
		t.Errorf("DueOn = %q, want the day after the clock's time", nt.DueOn)
	}
	// The clock is read once: the task and its tags are written with the same time.
	if !st.createdAt[0].Equal(now) || !st.taggedAt[0].Equal(now) {
		t.Errorf("written at %v and %v, want %v", st.createdAt[0], st.taggedAt[0], now)
	}

	// Empty notes are not converted and stay empty.
	st, text = newFake(), &fakeText{}
	if _, err := newTasks(st, text).Create(context.Background(), CreateTaskInput{Name: "a"}); err != nil {
		t.Fatal(err)
	}
	if text.calls != 0 || st.created[0].Notes != "" || st.created[0].HTMLNotes != "" || st.created[0].DueOn != "" {
		t.Errorf("empty input: %d conversions, task %+v", text.calls, st.created[0])
	}
}

func TestCreateInvalidWritesNothing(t *testing.T) {
	for _, tc := range []struct {
		name  string
		in    CreateTaskInput
		field string // "" when the error is not a *FieldError
		is    error
	}{
		{"bad due date", CreateTaskInput{Due: "someday", Project: "新居"}, "due", nil},
		{"unknown assignee", CreateTaskInput{Assignee: "誰か"}, "assignee", errNotFound},
		{"unknown assignee in a project", CreateTaskInput{Project: "新居", Assignee: "誰か"}, "assignee", errNotFound},
		{"unknown project", CreateTaskInput{Project: "ない"}, "", errNotFound},
		{"unknown section", CreateTaskInput{Project: "新居", Section: "ない"}, "", errNotFound},
		{"bad tag", CreateTaskInput{Tags: []string{"急ぎ", " "}}, "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newFake()
			tc.in.Name = "a"
			gid, err := newTasks(st, &fakeText{}).Create(context.Background(), tc.in)
			if err == nil {
				t.Fatal("no error")
			}
			if gid != "" {
				t.Errorf("gid = %q", gid)
			}
			if w := st.writes(); len(w) != 0 {
				t.Errorf("wrote %q", w)
			}
			var fe *FieldError
			if got := errors.As(err, &fe); got != (tc.field != "") {
				t.Fatalf("FieldError: %v (%v)", got, err)
			}
			if fe != nil && fe.Field != tc.field {
				t.Errorf("Field = %q, want %q", fe.Field, tc.field)
			}
			if tc.is != nil && !errors.Is(err, tc.is) {
				t.Errorf("err %v is not %v", err, tc.is)
			}
		})
	}
}

// A bad due date is reported before any name is looked up.
func TestCreateDueCheckedFirst(t *testing.T) {
	st := newFake()
	_, err := newTasks(st, &fakeText{}).Create(context.Background(), CreateTaskInput{Name: "a", Due: "someday", Project: "ない"})
	var fe *FieldError
	if !errors.As(err, &fe) || fe.Field != "due" {
		t.Fatalf("err = %v, want a due FieldError", err)
	}
	if len(st.calls) != 0 {
		t.Errorf("calls %q", st.calls)
	}
}

func TestCreateStoreErrors(t *testing.T) {
	// The store refuses the task: nothing is tagged.
	st := newFake()
	st.createErr = errors.New("タスク名が空です")
	gid, err := newTasks(st, &fakeText{}).Create(context.Background(), CreateTaskInput{Tags: []string{"急ぎ"}})
	if err == nil || err.Error() != "タスク名が空です" || gid != "" {
		t.Errorf("gid %q, err %v", gid, err)
	}
	if len(st.tagged) != 0 || slices.Contains(st.calls, "ApplyTagEdit local:1") {
		t.Errorf("tagged after a failed create: %q", st.calls)
	}

	// Tagging fails after the task is created: the error is returned with its gid.
	st = newFake()
	st.applyErr = errors.New("add tag: boom")
	gid, err = newTasks(st, &fakeText{}).Create(context.Background(), CreateTaskInput{Name: "a", Tags: []string{"急ぎ"}})
	if err == nil || err.Error() != "add tag: boom" || gid != "local:1" {
		t.Errorf("gid %q, err %v", gid, err)
	}
}

func TestCreateTagsGoToTheNewTask(t *testing.T) {
	st := newFake()
	if _, err := newTasks(st, &fakeText{}).Create(context.Background(), CreateTaskInput{Name: "a", Tags: []string{"急ぎ", "新しい"}}); err != nil {
		t.Fatal(err)
	}
	want := task.TagEdit{Add: []task.TagRef{{GID: "t1"}, {Name: "新しい"}}}
	if len(st.tagged) != 1 || st.taggedGIDs[0] != "local:1" || !slices.Equal(st.tagged[0].Add, want.Add) || len(st.tagged[0].Remove) != 0 {
		t.Errorf("tagged %q with %+v", st.taggedGIDs, st.tagged)
	}
}

func TestFieldError(t *testing.T) {
	inner := errors.New("bad")
	err := error(&FieldError{Field: "due", Err: inner})
	if err.Error() != "due: bad" || !errors.Is(err, inner) {
		t.Errorf("%v", err)
	}
}
