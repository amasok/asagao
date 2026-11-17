package store

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
)

var filterNow = time.Date(2026, 11, 13, 10, 0, 0, 0, time.Local)

func TestBuildFilter(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.UpsertUsers(ctx, []asana.User{{GID: "u2", Name: "Partner"}}); err != nil {
		t.Fatal(err)
	}
	open, done := ptrTo(false), ptrTo(true)
	modifiedAfter := filterNow.AddDate(0, 0, -3).UTC().Format(time.RFC3339)

	for _, tc := range []struct {
		name string
		spec FilterSpec
		want TaskFilter
	}{
		{"default me, open (list_tasks)",
			FilterSpec{DefaultAssignee: "me", DefaultCompleted: open},
			TaskFilter{AssigneeGID: "me", Completed: open}},
		{"default any, both (search)",
			FilterSpec{DefaultAssignee: "any"},
			TaskFilter{}},
		{"project makes the default assignee everyone",
			FilterSpec{DefaultAssignee: "me", Project: "新居", Section: "手続き"},
			TaskFilter{ProjectGID: "p1", SectionGID: "s2"}},
		{"explicit assignee wins over project",
			FilterSpec{Assignee: "me", DefaultAssignee: "me", Project: "p1"},
			TaskFilter{AssigneeGID: "me", ProjectGID: "p1"}},
		{"assignee all",
			FilterSpec{Assignee: "all", DefaultAssignee: "me"},
			TaskFilter{}},
		{"assignee by name",
			FilterSpec{Assignee: "Partner"},
			TaskFilter{AssigneeGID: "u2"}},
		{"assignee gid not seen yet",
			FilterSpec{Assignee: "1209999"},
			TaskFilter{AssigneeGID: "1209999"}},
		{"relative due range",
			FilterSpec{DueAfter: "today", DueBefore: "+7d"},
			TaskFilter{DueAfter: "2026-11-13", DueBefore: "2026-11-20"}},
		{"completion completed",
			FilterSpec{Completion: "completed", DefaultCompleted: open},
			TaskFilter{Completed: done}},
		{"completion all",
			FilterSpec{Completion: "all", DefaultCompleted: open},
			TaskFilter{}},
		{"completion open",
			FilterSpec{Completion: "open"},
			TaskFilter{Completed: open}},
		{"overdue: before today, incomplete, overrides due_before",
			FilterSpec{Overdue: true, DueBefore: "+7d", DueAfter: "2026-11-01"},
			TaskFilter{DueBefore: "2026-11-12", DueAfter: "2026-11-01", Completed: open}},
		{"overdue overrides completion all",
			FilterSpec{Overdue: true, Completion: "all"},
			TaskFilter{DueBefore: "2026-11-12", Completed: open}},
		{"overdue overrides completion completed",
			FilterSpec{Overdue: true, Completion: "completed"},
			TaskFilter{DueBefore: "2026-11-12", Completed: open}},
		{"modified within includes completed by default",
			FilterSpec{ModifiedWithinDays: 3, DefaultCompleted: open},
			TaskFilter{ModifiedAfter: modifiedAfter, SortByModified: true}},
		{"modified within keeps an explicit completion",
			FilterSpec{ModifiedWithinDays: 3, Completion: "open"},
			TaskFilter{ModifiedAfter: modifiedAfter, SortByModified: true, Completed: open}},
		{"tag, limit and query pass through",
			FilterSpec{Tag: "買い物", Limit: 5, Query: "ソファ"},
			TaskFilter{Tag: "買い物", Limit: 5, Query: "ソファ"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.BuildFilter(ctx, tc.spec, filterNow)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got  %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestBuildFilterErrors(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	for _, tc := range []struct {
		name  string
		spec  FilterSpec
		field string // FilterError.Field; "" for a resolve error
		is    error
	}{
		{"section without project", FilterSpec{Section: "家具"}, "section", ErrSectionWithoutProject},
		{"bad due_before", FilterSpec{DueBefore: "someday"}, "due_before", nil},
		{"bad due_after", FilterSpec{DueAfter: "2026-13-01"}, "due_after", nil},
		{"bad completion", FilterSpec{Completion: "done"}, "completion", nil},
		{"unassigned is not a filter", FilterSpec{Assignee: "none"}, "assignee", nil},
		{"unknown user name", FilterSpec{Assignee: "誰か"}, "assignee", ErrNotFound},
		{"unknown project", FilterSpec{Project: "ない"}, "", ErrNotFound},
		{"ambiguous project", FilterSpec{Project: "[家族]"}, "", ErrAmbiguous},
		{"unknown section", FilterSpec{Project: "p1", Section: "ない"}, "", ErrNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.BuildFilter(ctx, tc.spec, filterNow)
			if err == nil {
				t.Fatal("want an error")
			}
			var fe *FilterError
			if got := errors.As(err, &fe); got != (tc.field != "") || (got && fe.Field != tc.field) {
				t.Fatalf("err = %v (%T), want field %q", err, err, tc.field)
			}
			if tc.is != nil && !errors.Is(err, tc.is) {
				t.Fatalf("err = %v, want %v", err, tc.is)
			}
		})
	}
}

// Overdue means due before today: a task due today is not overdue yet.
func TestOverdueBoundary(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	me := &asana.Ref{GID: "me", Name: "Me"}
	if err := s.UpsertTasks(ctx, []asana.Task{
		{GID: "today", Name: "今日締切", DueOn: "2026-11-13", Assignee: me},
		{GID: "yesterday", Name: "昨日締切", DueOn: "2026-11-12", Assignee: me},
		{GID: "done", Name: "昨日締切・完了", DueOn: "2026-11-12", Completed: true, Assignee: me},
	}); err != nil {
		t.Fatal(err)
	}
	f, err := s.BuildFilter(ctx, FilterSpec{DefaultAssignee: "me", Overdue: true}, filterNow)
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := s.ListTasks(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	got := gids(tasks)
	slices.Sort(got)
	if want := []string{"t2", "yesterday"}; !slices.Equal(got, want) {
		t.Fatalf("overdue = %v, want %v", got, want)
	}
}
