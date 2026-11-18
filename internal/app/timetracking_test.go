package app

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/readmodel"
)

// timeStore records the writes the time tracking use case makes.
type timeStore struct {
	state   map[string]string
	entries map[string]readmodel.TimeEntryView // gid -> entry (task t1)
	wrote   []string
}

func (f *timeStore) Me(context.Context) (readmodel.UserRef, error) {
	return readmodel.UserRef{GID: "me", Name: "Me"}, nil
}
func (f *timeStore) SyncState(_ context.Context, key string) (string, error) { return f.state[key], nil }
func (f *timeStore) TimeEntries(_ context.Context, task string) (readmodel.TimeEntries, error) {
	out := readmodel.TimeEntries{TaskGID: task}
	for _, e := range f.entries {
		out.Entries = append(out.Entries, e)
	}
	return out, nil
}
func (f *timeStore) TimeEntryTask(_ context.Context, gid string) (string, error) {
	if _, ok := f.entries[gid]; !ok {
		return "", readmodel.ErrNotFound
	}
	return "t1", nil
}
func (f *timeStore) AddTimeEntryLocal(_ context.Context, task string, minutes int, on string, me readmodel.UserRef, _ time.Time) (string, error) {
	f.wrote = append(f.wrote, fmt.Sprintf("add %s %d %s %s", task, minutes, on, me.GID))
	return "local:1", nil
}
func (f *timeStore) UpdateTimeEntryLocal(_ context.Context, gid string, minutes int, on string, _ time.Time) error {
	f.wrote = append(f.wrote, fmt.Sprintf("update %s %d %s", gid, minutes, on))
	return nil
}
func (f *timeStore) DeleteTimeEntryLocal(_ context.Context, gid string, _ time.Time) error {
	f.wrote = append(f.wrote, "delete "+gid)
	return nil
}

func newTimeTracking() (*TimeTracking, *timeStore) {
	st := &timeStore{state: map[string]string{}, entries: map[string]readmodel.TimeEntryView{
		"e1": {GID: "e1", DurationMinutes: 30, EnteredOn: "2026-11-10"},
	}}
	day := time.Date(2026, 11, 13, 9, 0, 0, 0, time.Local)
	return &TimeTracking{Store: st, Clock: func() time.Time { return day }}, st
}

// Durations and days are read as the user writes them; "" is today and keeps the old value
// in an edit.
func TestTimeTrackingWrites(t *testing.T) {
	ctx := context.Background()
	tt, st := newTimeTracking()
	if _, err := tt.Add(ctx, "t1", "1h30m", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := tt.Add(ctx, "t1", "45", "yesterday"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Edit(ctx, "e1", "", "2026-11-11"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Edit(ctx, "e1", "1:00", ""); err != nil {
		t.Fatal(err)
	}
	if err := tt.Delete(ctx, "e1"); err != nil {
		t.Fatal(err)
	}
	want := []string{"add t1 90 2026-11-13 me", "add t1 45 2026-11-12 me", "update e1 30 2026-11-11", "update e1 60 2026-11-10", "delete e1"}
	if fmt.Sprint(st.wrote) != fmt.Sprint(want) {
		t.Errorf("wrote %q\nwant  %q", st.wrote, want)
	}
	var fe *FieldError
	if _, err := tt.Add(ctx, "t1", "abc", ""); !errors.As(err, &fe) || fe.Field != "duration" {
		t.Errorf("bad duration: %v", err)
	}
	if _, err := tt.Add(ctx, "t1", "1h", "someday"); !errors.As(err, &fe) || fe.Field != "date" {
		t.Errorf("bad date: %v", err)
	}
	if err := tt.Edit(ctx, "nope", "1h", ""); !errors.Is(err, readmodel.ErrNotFound) {
		t.Errorf("unknown entry: %v", err)
	}
	if len(st.wrote) != len(want) {
		t.Errorf("a refused input was written: %q", st.wrote)
	}
}

// Once Asana refused time tracking (402), it is not offered and nothing is written.
func TestTimeTrackingPlanUnavailable(t *testing.T) {
	ctx := context.Background()
	tt, st := newTimeTracking()
	if !tt.Available(ctx) {
		t.Fatal("not available before a 402")
	}
	st.state[readmodel.KeyPlanUnavailable+readmodel.PlanTimeTracking] = "2026-11-13T00:00:00Z"
	if tt.Available(ctx) {
		t.Error("available after a 402")
	}
	_, err1 := tt.Add(ctx, "t1", "1h", "")
	err2 := tt.Edit(ctx, "e1", "1h", "")
	err3 := tt.Delete(ctx, "e1")
	for _, err := range []error{err1, err2, err3} {
		if !IsPlanUnavailable(err) {
			t.Errorf("err = %v", err)
		}
	}
	if len(st.wrote) != 0 {
		t.Errorf("wrote %q", st.wrote)
	}
}
