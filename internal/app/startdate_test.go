package app

import (
	"context"
	"errors"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/domain/plan"
	"github.com/amasok/asagao/internal/readmodel"
)

// #57: the start date is read like the due date, by every entry point through the use case.

func TestUpdateStartOn(t *testing.T) {
	st, tasks := newUpdate()
	u, err := tasks.Update(context.Background(), "123", UpdateTaskInput{Start: ptr("today"), Due: ptr("+7d")})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"start_on": "2026-11-16", "due_on": "2026-11-23"}
	if len(st.changes) != 1 || !maps.Equal(st.changes[0], want) {
		t.Fatalf("changes = %v, want %v", st.changes, want)
	}
	if u.Count() != 2 || *u.Start != "2026-11-16" {
		t.Fatalf("update = %+v", u)
	}
	// "" clears it.
	st, tasks = newUpdate()
	if _, err := tasks.Update(context.Background(), "123", UpdateTaskInput{Start: ptr("")}); err != nil {
		t.Fatal(err)
	}
	if v, ok := st.changes[0]["start_on"]; !ok || v != "" {
		t.Fatalf("changes = %v", st.changes)
	}
	// A date that cannot be read is a start_on FieldError, and nothing is written.
	st, tasks = newUpdate()
	_, err = tasks.Update(context.Background(), "123", UpdateTaskInput{Start: ptr("someday")})
	var fe *FieldError
	if !errors.As(err, &fe) || fe.Field != "start_on" {
		t.Fatalf("err = %v, want a start_on FieldError", err)
	}
	if len(st.changes) != 0 {
		t.Fatalf("wrote %v", st.changes)
	}
}

func TestCreateStartOn(t *testing.T) {
	st := newFake()
	if _, err := newTasks(st, &fakeText{}).Create(context.Background(), CreateTaskInput{Name: "a", Start: "today", Due: "+3d"}); err != nil {
		t.Fatal(err)
	}
	if nt := st.created[0]; nt.StartOn != "2026-11-16" || nt.DueOn != "2026-11-19" {
		t.Fatalf("created %+v", nt)
	}
	for _, in := range []CreateTaskInput{
		{Name: "a", Start: "today"},                 // no due date
		{Name: "a", Start: "+3d", Due: "today"},     // after the due date
		{Name: "a", Start: "someday", Due: "today"}, // not a date
	} {
		st := newFake()
		_, err := newTasks(st, &fakeText{}).Create(context.Background(), in)
		var fe *FieldError
		if !errors.As(err, &fe) || fe.Field != "start_on" {
			t.Errorf("%+v: err = %v, want a start_on FieldError", in, err)
		}
		if w := st.writes(); len(w) != 0 {
			t.Errorf("%+v: wrote %q", in, w)
		}
	}
}

// planStore is the sync_state and workspaces StartDateOffered reads.
type planStore map[string]time.Time

func (p planStore) PlanRefusedAt(_ context.Context, feature, workspaceGID string) (time.Time, bool, error) {
	at, ok := p[feature+":"+workspaceGID]
	return at, ok, nil
}
func (p planStore) TaskWorkspace(_ context.Context, gid string) (string, error) {
	if gid == "gone" {
		return "", readmodel.ErrNotFound
	}
	return strings.TrimPrefix(gid, "t-"), nil
}

func TestStartDateOffered(t *testing.T) {
	ctx := context.Background()
	st := planStore{plan.StartOn + ":w1": now.Add(-time.Hour)}
	if StartDateOffered(ctx, st, "t-w1", now) {
		t.Error("offered in w1 after a 402")
	}
	if !StartDateOffered(ctx, st, "t-w2", now) {
		t.Error("w1's 402 hid it in w2")
	}
	if !StartDateOffered(ctx, st, "t-w1", now.Add(plan.RecheckAfter)) {
		t.Error("not offered again after plan.RecheckAfter")
	}
	if !StartDateOffered(ctx, st, "gone", now) {
		t.Error("a task without a known workspace is not offered it")
	}
}
