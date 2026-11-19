package sync

import (
	"context"
	"fmt"
	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/store"
	"net/http"
	stdsync "sync"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/readmodel"
)

// A failure that is not a 402 (500, 429) records nothing for My Tasks' sections.
func TestMySectionsOtherFailuresRecordNothing(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusTooManyRequests, http.StatusForbidden, http.StatusNotFound} {
		f := &myTasksAsana{section: "m2", listIn: map[string]int{"w1": status}}
		s, st, _ := setupMyTasks(t, f)
		ctx := context.Background()
		_ = s.syncMySections(ctx, "w1")
		for _, ws := range []string{"w1", "w2", ""} {
			if v, _ := st.SyncState(ctx, readmodel.PlanKey(readmodel.PlanMySection, ws)); v != "" {
				t.Errorf("status %d: recorded for %q = %q", status, ws, v)
			}
		}
		if v, _ := st.SyncState(ctx, readmodel.KeyPlanUnavailable+readmodel.PlanMySection); v != "" {
			t.Errorf("status %d: global record = %q", status, v)
		}
	}
}

// A 402 in w1 and an answer in w2 at the same time: w2's answer must not forget w1's 402,
// and concurrent syncs of both workspaces are race-free.
func TestMySectionsConcurrentWorkspaces(t *testing.T) {
	f := &myTasksAsana{section: "m2", listIn: map[string]int{"w1": http.StatusPaymentRequired}}
	s, st, now := setupMyTasks(t, f)
	ctx := context.Background()
	var wg stdsync.WaitGroup
	for i := 0; i < 8; i++ {
		for _, ws := range []string{"w1", "w2"} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_ = s.syncMySections(ctx, ws)
			}()
		}
	}
	wg.Wait()
	if v, _ := st.SyncState(ctx, readmodel.PlanKey(readmodel.PlanMySection, "w1")); v != now.Format(time.RFC3339) {
		t.Fatalf("w1 record = %q", v)
	}
	if v, _ := st.SyncState(ctx, readmodel.PlanKey(readmodel.PlanMySection, "w2")); v != "" {
		t.Fatalf("w2 record = %q", v)
	}
	sections := &app.Sections{Store: st, Clock: func() time.Time { return *now }}
	if secs, _ := sections.MySections(ctx, "w1"); len(secs) != 0 {
		t.Fatalf("w1 sections shown: %v", secs)
	}
	if secs, _ := sections.MySections(ctx, "w2"); len(secs) != 2 {
		t.Fatalf("w2 sections = %v", secs)
	}
}

// A record that a clock ahead wrote (in the future) must not hide a feature for good: the
// sync asks again, and an answer forgets it.
func TestMySectionsFutureRecordAsksAgain(t *testing.T) {
	f := &myTasksAsana{section: "m2"}
	s, st, now := setupMyTasks(t, f)
	ctx := context.Background()
	key := readmodel.PlanKey(readmodel.PlanMySection, "w1")
	if err := st.SetSyncState(ctx, key, now.Add(48*time.Hour).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if err := s.syncMySections(ctx, "w1"); err != nil {
		t.Fatal(err)
	}
	if f.listsIn("w1") != 1 {
		t.Fatal("not asked with a record in the future")
	}
	if v, _ := st.SyncState(ctx, key); v != "" {
		t.Fatalf("future record kept after an answer: %q", v)
	}
}

// A garbage record (not a time) is treated as no record.
func TestMySectionsGarbageRecordAsksAgain(t *testing.T) {
	f := &myTasksAsana{section: "m2"}
	s, st, _ := setupMyTasks(t, f)
	ctx := context.Background()
	if err := st.SetSyncState(ctx, readmodel.PlanKey(readmodel.PlanMySection, "w1"), "1"); err != nil {
		t.Fatal(err)
	}
	if err := s.syncMySections(ctx, "w1"); err != nil {
		t.Fatal(err)
	}
	if f.listsIn("w1") != 1 {
		t.Fatal("not asked with a garbage record")
	}
}

// A 402 recorded for a workspace asks Asana again exactly at the day, not a second before.
func TestMySectionsRecheckBoundary(t *testing.T) {
	f := &myTasksAsana{section: "m2", listIn: map[string]int{"w1": http.StatusPaymentRequired}}
	s, _, now := setupMyTasks(t, f)
	ctx := context.Background()
	_ = s.syncMySections(ctx, "w1")
	*now = now.Add(readmodel.PlanRecheckAfter - time.Nanosecond)
	_ = s.syncMySections(ctx, "w1")
	if n := f.listsIn("w1"); n != 1 {
		t.Fatalf("asked %d times 1ns before the day", n)
	}
	*now = now.Add(time.Nanosecond)
	_ = s.syncMySections(ctx, "w1")
	if n := f.listsIn("w1"); n != 2 {
		t.Fatalf("asked %d times at the day", n)
	}
	// Still 402: recorded again from the new time, so it hides for another day.
	*now = now.Add(time.Hour)
	_ = s.syncMySections(ctx, "w1")
	if n := f.listsIn("w1"); n != 2 {
		t.Fatalf("asked %d times right after a new 402", n)
	}
}

// setupTwoWorkspaces adds to setupSections a project p2 with section s9, and a task t9 with
// tag g9, in workspace w2.
func setupTwoWorkspaces(t *testing.T) (*sectionServer, *Syncer, *store.Store) {
	t.Helper()
	f, s, st := setupSections(t)
	ctx := context.Background()
	for _, err := range []error{
		st.UpsertProjects(ctx, "w2", []asana.Project{{GID: "p2", Name: "Q"}}, "me"),
		st.ReplaceSections(ctx, "p2", []asana.Section{{GID: "s9", Name: "Inbox"}, {GID: "s8", Name: "Later"}}),
		st.UpsertTasks(ctx, []asana.Task{{GID: "t9", Name: "B", Workspace: &asana.Ref{GID: "w2"},
			Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p2"}, Section: &asana.Ref{GID: "s9"}}},
			Tags:        []asana.Ref{{GID: "g9", Name: "後で"}}}}),
		st.UpsertTags(ctx, []asana.Tag{{GID: "g9", Name: "後で"}}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	return f, s, st
}

func refuseAll(status int) func(http.ResponseWriter, *http.Request, map[string]any) bool {
	return func(rw http.ResponseWriter, r *http.Request, _ map[string]any) bool {
		if r.Method == http.MethodGet {
			return false
		}
		rw.WriteHeader(status)
		fmt.Fprint(rw, `{"errors":[{"message":"no"}]}`)
		return true
	}
}

func planRecords(t *testing.T, st *store.Store, features ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, f := range features {
		for _, ws := range []string{"w1", "w2", ""} {
			if v, _ := st.SyncState(context.Background(), readmodel.PlanKey(f, ws)); v != "" {
				out[f+"@"+ws] = v
			}
		}
		if v, _ := st.SyncState(context.Background(), readmodel.KeyPlanUnavailable+f); v != "" {
			out[f+"@global"] = v
		}
	}
	return out
}

// Deleting a tag whose tasks are in w2 records a 402 for w2 only (the tag is gone from the
// cache by then; its tasks are in the entry).
func TestPushDeleteTag402RecordsItsWorkspace(t *testing.T) {
	f, s, st := setupTwoWorkspaces(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return at }
	tags := &app.Tags{Store: st, Clock: func() time.Time { return at }}
	if err := tags.Delete(ctx, "g9"); err != nil {
		t.Fatal(err)
	}
	f.hook = refuseAll(http.StatusPaymentRequired)
	if _, err := s.Push(ctx); err != nil {
		t.Fatal(err)
	}
	got := planRecords(t, st, readmodel.PlanTags)
	if len(got) != 1 || got[readmodel.PlanTags+"@w2"] == "" {
		t.Fatalf("records = %v, want only tags@w2", got)
	}
}

// 500 and 429 to a section or tag change record nothing.
func TestPushOtherFailuresRecordNothing(t *testing.T) {
	for _, status := range []int{500, 429, 403, 400} {
		f, s, st := setupTwoWorkspaces(t)
		ctx := context.Background()
		at := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
		s.now = func() time.Time { return at }
		clock := func() time.Time { return at }
		sections := &app.Sections{Store: st, Clock: clock}
		tags := &app.Tags{Store: st, Clock: clock}
		if err := sections.ReorderHere(ctx, "s2", "s1", ""); err != nil {
			t.Fatal(err)
		}
		if err := tags.UpdateHere(ctx, "g1", map[string]any{"name": "x"}); err != nil {
			t.Fatal(err)
		}
		f.hook = refuseAll(status)
		_, _ = s.Push(ctx)
		if got := planRecords(t, st, readmodel.PlanSections, readmodel.PlanTags); len(got) != 0 {
			t.Errorf("status %d: records = %v", status, got)
		}
	}
}

// An accepted change in w2 does not forget w1's 402.
func TestPushSuccessInOtherWorkspaceKeepsRecord(t *testing.T) {
	f, s, st := setupTwoWorkspaces(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return at }
	for _, feature := range []string{readmodel.PlanSections, readmodel.PlanTags} {
		if err := st.SetSyncState(ctx, readmodel.PlanKey(feature, "w1"), at.Add(-time.Hour).Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
	}
	// Queue the changes straight into the store (the use cases would refuse w1).
	clock := func() time.Time { return at }
	sections := &app.Sections{Store: st, Clock: clock}
	tags := &app.Tags{Store: st, Clock: clock}
	f.hook = nil
	if err := sections.ReorderHere(ctx, "s8", "s9", ""); err != nil {
		t.Fatal(err)
	}
	if err := tags.UpdateHere(ctx, "g9", map[string]any{"name": "あとで2"}); err != nil {
		t.Fatal(err)
	}
	f.hook = func(rw http.ResponseWriter, r *http.Request, _ map[string]any) bool {
		if r.Method != http.MethodGet { // w2's gids are not in the fake: it answers 200
			rw.Header().Set("Content-Type", "application/json")
			fmt.Fprint(rw, `{"data":{}}`)
			return true
		}
		return false
	}
	if _, err := s.Push(ctx); err != nil {
		t.Fatal(err)
	}
	got := planRecords(t, st, readmodel.PlanSections, readmodel.PlanTags)
	if got[readmodel.PlanSections+"@w1"] == "" || got[readmodel.PlanTags+"@w1"] == "" {
		t.Fatalf("w1's records were forgotten by w2's answers: %v", got)
	}
}

// Concurrent pushes of 402s in both workspaces with menu checks are race-free.
func TestPlanChecksConcurrentWithPush(t *testing.T) {
	f, s, st := setupTwoWorkspaces(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return at }
	clock := func() time.Time { return at }
	sections := &app.Sections{Store: st, Clock: clock}
	tags := &app.Tags{Store: st, Clock: clock}
	if err := sections.ReorderHere(ctx, "s2", "s1", ""); err != nil {
		t.Fatal(err)
	}
	if err := sections.ReorderHere(ctx, "s8", "s9", ""); err != nil {
		t.Fatal(err)
	}
	f.hook = refuseAll(http.StatusPaymentRequired)
	var wg stdsync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); _ = sections.Available(ctx); _ = tags.Available(ctx) }()
		go func() { defer wg.Done(); _, _ = s.Push(ctx) }()
	}
	wg.Wait()
	if got := planRecords(t, st, readmodel.PlanSections); len(got) != 2 {
		t.Fatalf("records = %v, want sections@w1 and @w2", got)
	}
	if sections.Available(ctx) || !tags.Available(ctx) {
		t.Fatalf("Available sections=%v tags=%v", sections.Available(ctx), tags.Available(ctx))
	}
}
