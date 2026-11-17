package app

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/domain/task"
)

// UpdateTaskLocal makes fakeStore a TaskStore; the Create tests never reach it.
func (f *fakeStore) UpdateTaskLocal(_ context.Context, gid string, changes map[string]any, now time.Time) error {
	f.log("UpdateTaskLocal %s %v", gid, changes)
	return nil
}

// updateStore records the updates and can fail them.
type updateStore struct {
	*fakeStore
	err     error
	gids    []string
	changes []map[string]any
	at      []time.Time
}

func (f *updateStore) UpdateTaskLocal(ctx context.Context, gid string, changes map[string]any, now time.Time) error {
	f.log("UpdateTaskLocal %s %s", gid, strings.Join(slices.Sorted(maps.Keys(changes)), ","))
	f.gids, f.changes, f.at = append(f.gids, gid), append(f.changes, maps.Clone(changes)), append(f.at, now)
	return f.err
}

func newUpdate() (*updateStore, *Tasks) {
	st := &updateStore{fakeStore: newFake()}
	return st, &Tasks{Store: st, Text: &fakeText{}, Clock: func() time.Time { return now }}
}

func ptr[T any](v T) *T { return &v }

func TestUpdateSendsEachFieldAsUpdateTaskLocalTakesIt(t *testing.T) {
	st, tasks := newUpdate()
	u, err := tasks.Update(context.Background(), "123", UpdateTaskInput{
		Name: ptr("新しい名前"), Notes: ptr("**説明**"), Due: ptr("tomorrow"), Assignee: ptr("花子"),
		Subtype: ptr("承認"), Approval: ptr("approve"), Completed: ptr(true), Liked: ptr(false),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"name": "新しい名前", "notes": "plain:**説明**", "html_notes": "<body>**説明**</body>",
		"due_on": "2026-11-17", "assignee": "u-hanako", "resource_subtype": "approval", "approval_status": "approved",
		"completed": true, "liked": false,
	}
	if len(st.changes) != 1 || !maps.Equal(st.changes[0], want) {
		t.Fatalf("changes = %v, want %v", st.changes, want)
	}
	if st.gids[0] != "123" || !st.at[0].Equal(now) {
		t.Errorf("UpdateTaskLocal(%s, at %v), want 123 at %v", st.gids[0], st.at[0], now)
	}
	// The resolved values come back, so that the entry point can say what it set.
	if *u.Due != "2026-11-17" || *u.Assignee != "u-hanako" || *u.Subtype != "approval" || *u.Approval != "approved" || *u.Notes != "**説明**" {
		t.Errorf("resolved = %+v", u)
	}
	if u.Count() != 8 {
		t.Errorf("Count = %d, want 8 (notes and html_notes are one field)", u.Count())
	}
}

func TestUpdateClearsWithEmptyValues(t *testing.T) {
	st, tasks := newUpdate()
	if _, err := tasks.Update(context.Background(), "123", UpdateTaskInput{Due: ptr(""), Assignee: ptr("")}); err != nil {
		t.Fatal(err)
	}
	if want := map[string]any{"due_on": "", "assignee": ""}; !maps.Equal(st.changes[0], want) {
		t.Errorf("changes = %v, want %v", st.changes[0], want)
	}
}

func TestUpdateLeavesNilFields(t *testing.T) {
	st, tasks := newUpdate()
	if _, err := tasks.Update(context.Background(), "123", UpdateTaskInput{Completed: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	if want := map[string]any{"completed": false}; !maps.Equal(st.changes[0], want) {
		t.Errorf("changes = %v, want %v", st.changes[0], want)
	}
	if got := strings.Join(st.calls, "; "); got != "UpdateTaskLocal 123 completed" {
		t.Errorf("calls = %s (no name lookup without an assignee)", got)
	}
	if tasks.Text.(*fakeText).calls != 0 {
		t.Error("notes converted without Notes")
	}
}

func TestUpdateWithNothingWritesNothing(t *testing.T) {
	st, tasks := newUpdate()
	u, err := tasks.Update(context.Background(), "123", UpdateTaskInput{})
	if err != nil || u.Count() != 0 || len(st.calls) != 0 {
		t.Errorf("Update({}) = %+v, %v; calls %v", u, err, st.calls)
	}
}

func TestUpdateInvalidInputWritesNothing(t *testing.T) {
	bad := UpdateTaskInput{Subtype: ptr("bug"), Approval: ptr("maybe"), Due: ptr("someday"), Assignee: ptr("誰か")}
	_, errSubtype := task.ParseSubtype("bug")
	for _, tc := range []struct {
		name  string
		in    UpdateTaskInput
		field string
	}{
		// The first invalid field, in this order, is the one reported.
		{"all bad", bad, "resource_subtype"},
		{"approval, due, assignee", UpdateTaskInput{Approval: bad.Approval, Due: bad.Due, Assignee: bad.Assignee}, "approval_status"},
		{"due, assignee", UpdateTaskInput{Due: bad.Due, Assignee: bad.Assignee, Name: ptr("x")}, "due"},
		{"assignee", UpdateTaskInput{Assignee: bad.Assignee, Completed: ptr(true)}, "assignee"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, tasks := newUpdate()
			_, err := tasks.Update(context.Background(), "123", tc.in)
			var fe *FieldError
			if !errors.As(err, &fe) || fe.Field != tc.field {
				t.Fatalf("err = %v, want a *FieldError for %s", err, tc.field)
			}
			if len(st.changes) != 0 {
				t.Errorf("wrote %v", st.changes)
			}
		})
	}
	// The error inside is the parser's or the store's, as it is, so that an entry point
	// that does not name the field (the CLI) can return it unchanged.
	_, tasks := newUpdate()
	_, err := tasks.Update(context.Background(), "1", UpdateTaskInput{Subtype: ptr("bug")})
	if fe := (*FieldError)(nil); !errors.As(err, &fe) || fe.Err.Error() != errSubtype.Error() {
		t.Errorf("err = %v, want %v inside", err, errSubtype)
	}
	_, err = tasks.Update(context.Background(), "1", UpdateTaskInput{Assignee: ptr("誰か")})
	if !errors.Is(err, errNotFound) || err.Error() != `assignee: user "誰か": not found` {
		t.Errorf("err = %v", err)
	}
	_, err = tasks.Update(context.Background(), "1", UpdateTaskInput{Due: ptr("someday")})
	if _, want := ParseDue("someday", now); err.Error() != "due: "+want.Error() {
		t.Errorf("err = %v, want due: %v", err, want)
	}
}

func TestUpdateReturnsTheStoreErrorAsItIs(t *testing.T) {
	st, tasks := newUpdate()
	st.err = errors.New("タスク名が空です")
	if _, err := tasks.Update(context.Background(), "1", UpdateTaskInput{Name: ptr("")}); err != st.err {
		t.Errorf("err = %v, want the store's", err)
	}
}

func TestUpdateReadsTheDueDateFromTheClock(t *testing.T) {
	st := &updateStore{fakeStore: newFake()}
	clockAt := time.Date(2026, 1, 31, 23, 0, 0, 0, time.UTC)
	tasks := &Tasks{Store: st, Text: &fakeText{}, Clock: func() time.Time { return clockAt }}
	u, err := tasks.ResolveUpdate(context.Background(), UpdateTaskInput{Due: ptr("+1d")})
	if err != nil || *u.Due != "2026-02-01" {
		t.Errorf("+1d = %v, %v", u.Due, err)
	}
	if len(st.calls) != 0 {
		t.Errorf("ResolveUpdate wrote: %v", st.calls)
	}
}

func TestUpdateHereTakesResolvedValuesAsTheyAre(t *testing.T) {
	st, tasks := newUpdate()
	// Not names or relative dates: nothing is looked up or read.
	err := tasks.UpdateHere(context.Background(), "9", TaskUpdate{Due: ptr("tomorrow"), Assignee: ptr("花子"), Subtype: ptr("milestone")})
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]any{"due_on": "tomorrow", "assignee": "花子", "resource_subtype": "milestone"}; !maps.Equal(st.changes[0], want) {
		t.Errorf("changes = %v, want %v", st.changes[0], want)
	}
	if got := fmt.Sprint(st.calls); got != "[UpdateTaskLocal 9 assignee,due_on,resource_subtype]" {
		t.Errorf("calls = %s", got)
	}
}

func TestUpdateReadsTheClockOnce(t *testing.T) {
	st := &updateStore{fakeStore: newFake()}
	// Each read is a day later: a clock that crosses midnight between two reads.
	reads := 0
	start := time.Date(2026, 11, 16, 23, 59, 59, 0, time.UTC)
	tasks := &Tasks{Store: st, Text: &fakeText{}, Clock: func() time.Time {
		reads++
		return start.AddDate(0, 0, reads-1)
	}}
	if _, err := tasks.Update(context.Background(), "1", UpdateTaskInput{Due: ptr("today")}); err != nil {
		t.Fatal(err)
	}
	if reads != 1 {
		t.Errorf("the clock was read %d times, want 1", reads)
	}
	if got := st.changes[0]["due_on"]; got != "2026-11-16" || !st.at[0].Equal(start) {
		t.Errorf("due_on = %v at %v, want 2026-11-16 at %v", got, st.at[0], start)
	}
}
