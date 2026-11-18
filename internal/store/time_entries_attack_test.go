package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	stdsync "sync"
	"testing"

	"github.com/amasok/asagao/internal/asana"
)

func TestAttackAddTimeEntryBoundaries(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for _, c := range []struct {
		min  int
		on   string
		fail bool
	}{
		{1, "2026-11-12", false}, {1440, "2026-11-12", false},
		{0, "2026-11-12", true}, {-1, "2026-11-12", true}, {1441, "2026-11-12", true},
		{30, "", true}, {30, "2026-13-01", true}, {30, "2026-02-30", true}, {30, "2026-1-1", true},
		{30, "2026-11-12T00:00:00Z", true}, {30, " 2026-11-12", true}, {30, "2024-02-29", false},
	} {
		_, err := s.AddTimeEntryLocal(ctx, "t1", c.min, c.on, me, testNow)
		if (err != nil) != c.fail {
			t.Errorf("Add(%d, %q) err = %v, want fail=%v", c.min, c.on, err, c.fail)
		}
	}
	// refused ones leave nothing behind: 3 good ones only
	if es := timeEntries(t, s, "t1"); len(es.Entries) != 3 {
		t.Errorf("%d entries", len(es.Entries))
	}
	if ops, _ := s.ListOps(ctx); len(ops) != 3 {
		t.Errorf("%d ops", len(ops))
	}
}

func TestAttackTimeEntryUnknown(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.AddTimeEntryLocal(ctx, "nope", 5, "2026-11-12", me, testNow); !errors.Is(err, ErrNotFound) {
		t.Errorf("add: %v", err)
	}
	if err := s.UpdateTimeEntryLocal(ctx, "nope", 5, "2026-11-12", testNow); !errors.Is(err, ErrNotFound) {
		t.Errorf("update: %v", err)
	}
	if err := s.DeleteTimeEntryLocal(ctx, "nope", testNow); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete: %v", err)
	}
	if _, err := s.TimeEntryTask(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("task: %v", err)
	}
	if ops, _ := s.ListOps(ctx); len(ops) != 0 {
		t.Errorf("ops %+v", ops)
	}
}

// Edit then edit back, and delete twice: no leftover ops, second delete is ErrNotFound.
func TestAttackTimeEntryEditBackAndDoubleDelete(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedTimeEntries(t, s)
	must(t, s.UpdateTimeEntryLocal(ctx, "e1", 45, "2026-11-12", testNow))
	must(t, s.UpdateTimeEntryLocal(ctx, "e1", 50, "2026-11-14", testNow))
	if ops, _ := s.ListOps(ctx); len(ops) != 1 {
		t.Fatalf("edits not merged: %+v", ops)
	}
	must(t, s.UpdateTimeEntryLocal(ctx, "e1", 30, "2026-11-12", testNow))
	if ops, _ := s.ListOps(ctx); len(ops) != 0 {
		t.Fatalf("edit back left %+v", ops)
	}
	must(t, s.DeleteTimeEntryLocal(ctx, "e1", testNow))
	if err := s.DeleteTimeEntryLocal(ctx, "e1", testNow); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
	if ops, _ := s.ListOps(ctx); len(ops) != 1 {
		t.Errorf("ops %+v", ops)
	}
}

// A local entry added, edited and deleted before any push leaves nothing at all.
func TestAttackTimeEntryLocalLifecycleLeavesNothing(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	g, err := s.AddTimeEntryLocal(ctx, "t1", 30, "2026-11-12", me, testNow)
	must(t, err)
	must(t, s.UpdateTimeEntryLocal(ctx, g, 31, "2026-11-12", testNow))
	must(t, s.DeleteTimeEntryLocal(ctx, g, testNow))
	if ops, _ := s.ListOps(ctx); len(ops) != 0 {
		t.Errorf("ops %+v", ops)
	}
	if es := timeEntries(t, s, "t1"); len(es.Entries) != 0 {
		t.Errorf("entries %+v", es.Entries)
	}
}

// Names from Asana are cleaned of terminal control sequences on the way out.
func TestAttackTimeEntryTermsafe(t *testing.T) {
	s := newTestStore(t)
	must(t, s.ReplaceTimeEntries(context.Background(), "t1", []asana.TimeEntry{{GID: "e1", DurationMinutes: 5, EnteredOn: "2026-11-01",
		CreatedBy:      &asana.Ref{GID: "u", Name: "a\x1b[2Jb"},
		Assignee:       &asana.Ref{GID: "u", Name: "c\x1b]0;x\x07d"},
		AttributableTo: &asana.Ref{GID: "p", Name: "e\x1b[31mf"},
		Categories:     []asana.Ref{{GID: "c", Name: "g\x1b[0mh"}}}}, testNow))
	e := timeEntries(t, s, "t1").Entries[0]
	all := e.CreatedBy.Name + e.Assignee.Name + e.Project + strings.Join(e.Categories, "")
	if strings.ContainsAny(all, "\x1b\x07") {
		t.Errorf("control characters: %q", all)
	}
}

func TestAttackTimeEntryConcurrentAdds(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	var wg stdsync.WaitGroup
	errs := make(chan error, 30)
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.AddTimeEntryLocal(ctx, "t1", 1+i, "2026-11-12", me, testNow)
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	es := timeEntries(t, s, "t1")
	if len(es.Entries) != 30 || es.TotalMinutes != 30*31/2 {
		t.Errorf("%d entries, total %d", len(es.Entries), es.TotalMinutes)
	}
	if ops, _ := s.ListOps(ctx); len(ops) != 30 {
		t.Errorf("%d ops", len(ops))
	}
	_ = fmt.Sprint
}
