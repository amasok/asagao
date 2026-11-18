package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/readmodel"
)

var me = UserRef{GID: "me", Name: "Me"}

func timeEntries(t *testing.T, s *Store, task string) readmodel.TimeEntries {
	t.Helper()
	es, err := s.TimeEntries(context.Background(), task)
	must(t, err)
	return es
}

// seedTimeEntries stores t1's entries from Asana: e1 (mine, 30m) and e2 (someone else's, 1h).
func seedTimeEntries(t *testing.T, s *Store) {
	t.Helper()
	must(t, s.ReplaceTimeEntries(context.Background(), "t1", []asana.TimeEntry{
		{GID: "e1", DurationMinutes: 30, EnteredOn: "2026-11-12", CreatedBy: &asana.Ref{GID: "me", Name: "Me"},
			Assignee: &asana.Ref{GID: "me", Name: "Me"}, Categories: []asana.Ref{{GID: "c1", Name: "開発"}}},
		{GID: "e2", DurationMinutes: 60, EnteredOn: "2026-11-13", CreatedBy: &asana.Ref{GID: "u2", Name: "誰か"},
			AttributableTo: &asana.Ref{GID: "p1", Name: "新居"}},
	}, testNow.Add(-time.Hour)))
}

// A fetched list is shown newest day first with its total and when it was read.
func TestReplaceTimeEntries(t *testing.T) {
	s := newTestStore(t)
	seedTimeEntries(t, s)
	es := timeEntries(t, s, "t1")
	if len(es.Entries) != 2 || es.Entries[0].GID != "e2" || es.Entries[1].GID != "e1" {
		t.Fatalf("entries %+v", es.Entries)
	}
	if es.TotalMinutes != 90 || es.Total != "1h30m" || es.FetchedAt == "" {
		t.Errorf("total %d %q fetched %q", es.TotalMinutes, es.Total, es.FetchedAt)
	}
	if e := es.Entries[1]; e.Duration != "30m" || e.CreatedBy == nil || e.CreatedBy.Name != "Me" ||
		len(e.Categories) != 1 || e.Categories[0] != "開発" || e.Pending {
		t.Errorf("e1 %+v", e)
	}
	if es.Entries[0].Project != "新居" {
		t.Errorf("e2 project %q", es.Entries[0].Project)
	}
	// Fetched again without e2: it is gone.
	must(t, s.ReplaceTimeEntries(context.Background(), "t1", []asana.TimeEntry{{GID: "e1", DurationMinutes: 30, EnteredOn: "2026-11-12"}}, testNow))
	if es := timeEntries(t, s, "t1"); len(es.Entries) != 1 || es.TotalMinutes != 30 {
		t.Errorf("after refresh %+v", es)
	}
}

// Adding an entry shows it at once (pending) and queues one add_time_entry; bad values are
// refused before anything is stored.
func TestAddTimeEntryLocal(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	gid, err := s.AddTimeEntryLocal(ctx, "t1", 45, "2026-11-13", me, testNow)
	must(t, err)
	es := timeEntries(t, s, "t1")
	if len(es.Entries) != 1 || es.Entries[0].GID != gid || !es.Entries[0].Pending || es.TotalMinutes != 45 {
		t.Fatalf("entries %+v", es)
	}
	ops := opsOfKind(t, s, OpAddTimeEntry)
	if len(ops) != 1 || ops[0].TaskGID != "t1" || ops[0].Payload["entry"] != gid ||
		intValue(ops[0].Payload["duration_minutes"]) != 45 || ops[0].Payload["entered_on"] != "2026-11-13" {
		t.Fatalf("ops %+v", ops)
	}
	for _, bad := range []struct {
		task string
		min  int
		on   string
	}{{"t1", 0, "2026-11-13"}, {"t1", -5, "2026-11-13"}, {"t1", 24*60 + 1, "2026-11-13"}, {"t1", 10, ""},
		{"t1", 10, "2026-13-01"}, {"t1", 10, "tomorrow"}, {"nope", 10, "2026-11-13"}} {
		if _, err := s.AddTimeEntryLocal(ctx, bad.task, bad.min, bad.on, me, testNow); err == nil {
			t.Errorf("AddTimeEntryLocal(%+v) succeeded", bad)
		}
	}
	if _, err := s.AddTimeEntryLocal(ctx, "nope", 10, "2026-11-13", me, testNow); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown task: %v", err)
	}
	if n := len(opsOfKind(t, s, OpAddTimeEntry)); n != 1 {
		t.Errorf("%d adds queued", n)
	}
}

// Editing an entry from Asana queues one update whose base is Asana's values; more edits
// join it and an edit back to them drops it. Discarding it puts the values back.
func TestUpdateTimeEntryLocal(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	seedTimeEntries(t, s)
	must(t, s.UpdateTimeEntryLocal(ctx, "e1", 50, "2026-11-12", testNow))
	must(t, s.UpdateTimeEntryLocal(ctx, "e1", 70, "2026-11-11", testNow))
	ops := opsOfKind(t, s, OpUpdateTimeEntry)
	if len(ops) != 1 || intValue(ops[0].Payload["duration_minutes"]) != 70 || ops[0].Payload["entered_on"] != "2026-11-11" ||
		intValue(ops[0].Base["duration_minutes"]) != 30 || ops[0].Base["entered_on"] != "2026-11-12" {
		t.Fatalf("ops %+v", ops)
	}
	es := timeEntries(t, s, "t1")
	if e := find(es, "e1"); e == nil || e.DurationMinutes != 70 || e.EnteredOn != "2026-11-11" || !e.Pending {
		t.Fatalf("e1 %+v", e)
	}
	// A refresh read before the edit was sent keeps showing the edit.
	seedTimeEntries(t, s)
	if e := find(timeEntries(t, s, "t1"), "e1"); e == nil || e.DurationMinutes != 70 {
		t.Errorf("after refresh e1 %+v", e)
	}
	must(t, s.DiscardOp(ctx, ops[0].ID))
	if e := find(timeEntries(t, s, "t1"), "e1"); e == nil || e.DurationMinutes != 30 || e.EnteredOn != "2026-11-12" || e.Pending {
		t.Errorf("after discard e1 %+v", e)
	}
	// Back to Asana's values: nothing to send.
	must(t, s.UpdateTimeEntryLocal(ctx, "e1", 40, "2026-11-12", testNow))
	must(t, s.UpdateTimeEntryLocal(ctx, "e1", 30, "2026-11-12", testNow))
	if n := len(opsOfKind(t, s, OpUpdateTimeEntry)); n != 0 {
		t.Errorf("%d updates after editing back", n)
	}
	if err := s.UpdateTimeEntryLocal(ctx, "nope", 30, "2026-11-12", testNow); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown entry: %v", err)
	}
}

func find(es readmodel.TimeEntries, gid string) *readmodel.TimeEntryView {
	for i := range es.Entries {
		if es.Entries[i].GID == gid {
			return &es.Entries[i]
		}
	}
	return nil
}

// Deleting an entry hides it (also after a refresh) and discarding the delete brings it back
// as it was.
func TestDeleteTimeEntryLocal(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	seedTimeEntries(t, s)
	must(t, s.DeleteTimeEntryLocal(ctx, "e2", testNow))
	if e := find(timeEntries(t, s, "t1"), "e2"); e != nil {
		t.Fatalf("e2 still shown %+v", e)
	}
	seedTimeEntries(t, s)
	if e := find(timeEntries(t, s, "t1"), "e2"); e != nil {
		t.Fatalf("e2 back after refresh %+v", e)
	}
	ops := opsOfKind(t, s, OpDeleteTimeEntry)
	if len(ops) != 1 || ops[0].Payload["entry"] != "e2" || intValue(ops[0].Payload["duration_minutes"]) != 60 {
		t.Fatalf("ops %+v", ops)
	}
	must(t, s.DiscardOp(ctx, ops[0].ID))
	e := find(timeEntries(t, s, "t1"), "e2")
	if e == nil || e.DurationMinutes != 60 || e.Project != "新居" || e.CreatedBy == nil || e.CreatedBy.Name != "誰か" {
		t.Errorf("after discard e2 %+v", e)
	}
}

// An entry never sent is rewritten by an edit and dropped by a delete (with nothing queued).
func TestTimeEntryNotSentYet(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	gid, err := s.AddTimeEntryLocal(ctx, "t1", 45, "2026-11-13", me, testNow)
	must(t, err)
	must(t, s.UpdateTimeEntryLocal(ctx, gid, 15, "2026-11-12", testNow))
	ops := opsOfKind(t, s, OpAddTimeEntry)
	if len(ops) != 1 || intValue(ops[0].Payload["duration_minutes"]) != 15 || ops[0].Payload["entered_on"] != "2026-11-12" {
		t.Fatalf("add %+v", ops)
	}
	if n := len(opsOfKind(t, s, OpUpdateTimeEntry)); n != 0 {
		t.Errorf("%d updates queued", n)
	}
	must(t, s.DeleteTimeEntryLocal(ctx, gid, testNow))
	all, err := s.ListOps(ctx)
	must(t, err)
	if len(all) != 0 || len(timeEntries(t, s, "t1").Entries) != 0 {
		t.Errorf("ops %+v entries %+v", all, timeEntries(t, s, "t1"))
	}
}

// Once the add is being sent, an edit and a delete wait for it (queued with the local gid),
// and the recorded result makes them apply to Asana's entry.
func TestTimeEntryChangedWhileSending(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	gid, err := s.AddTimeEntryLocal(ctx, "t1", 45, "2026-11-13", me, testNow)
	must(t, err)
	add := opsOfKind(t, s, OpAddTimeEntry)[0]
	if _, err := s.BeginSend(ctx, add.ID, testNow); err != nil {
		t.Fatal(err)
	}
	must(t, s.UpdateTimeEntryLocal(ctx, gid, 20, "2026-11-13", testNow))
	if real, waiting, err := s.LocalResult(ctx, ResultTimeEntry, gid); err != nil || real != "" || !waiting {
		t.Fatalf("LocalResult = %q %v %v", real, waiting, err)
	}
	// Asana created it with the values sent (45m); the queued edit is applied again.
	must(t, s.CompleteTimeEntry(ctx, add.ID, gid, "t1", asana.TimeEntry{GID: "e9", DurationMinutes: 45, EnteredOn: "2026-11-13",
		CreatedBy: &asana.Ref{GID: "me", Name: "Me"}, CreatedAt: testNow}))
	es := timeEntries(t, s, "t1")
	if len(es.Entries) != 1 || es.Entries[0].GID != "e9" || es.Entries[0].DurationMinutes != 20 || !es.Entries[0].Pending {
		t.Fatalf("entries %+v", es.Entries)
	}
	if real, waiting, err := s.LocalResult(ctx, ResultTimeEntry, gid); err != nil || real != "e9" || waiting {
		t.Errorf("LocalResult = %q %v %v", real, waiting, err)
	}
	// A refresh read before Asana created it keeps it.
	must(t, s.ReplaceTimeEntries(ctx, "t1", nil, testNow.Add(-time.Minute)))
	if es := timeEntries(t, s, "t1"); len(es.Entries) != 1 {
		t.Errorf("after an older refresh %+v", es.Entries)
	}
	must(t, s.DeleteTimeEntryLocal(ctx, "e9", testNow))
	if len(opsOfKind(t, s, OpDeleteTimeEntry)) != 1 || len(timeEntries(t, s, "t1").Entries) != 0 {
		t.Errorf("delete not queued")
	}
}

// Entries move with a task created offline when Asana gives it its gid, and go with the task.
func TestTimeEntriesFollowTask(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	task, err := s.CreateTaskLocal(ctx, NewTask{Name: "新しい", ProjectGID: "p1"}, testNow)
	must(t, err)
	if _, err := s.AddTimeEntryLocal(ctx, task, 30, "2026-11-13", me, testNow); err != nil {
		t.Fatal(err)
	}
	must(t, s.ReplaceLocalTaskGID(ctx, task, "t99"))
	if es := timeEntries(t, s, "t99"); len(es.Entries) != 1 {
		t.Fatalf("entries of t99 %+v", es)
	}
	if ops := opsOfKind(t, s, OpAddTimeEntry); len(ops) != 1 || ops[0].TaskGID != "t99" {
		t.Errorf("add %+v", ops)
	}
}
