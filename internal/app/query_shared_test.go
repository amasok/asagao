package app

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/domain/plan"
	"github.com/amasok/asagao/internal/readmodel"
)

// sharedReadStore is a QueryStore for the reads Query adds to the store's (#71): what it
// returns is set per test, and the filters asked for are recorded.
type sharedReadStore struct {
	fakeQueryStore
	state     readmodel.InboxState
	tasks     []readmodel.TaskSummary
	taskF     *readmodel.TaskFilter
	notes     []readmodel.Notification
	noteF     *readmodel.NotificationFilter
	unread    int
	buildErr  error
	workspace []string
	refused   map[string]time.Time // workspace → 402
	planErr   error
}

func (s *sharedReadStore) InboxState(context.Context) (readmodel.InboxState, error) {
	return s.state, nil
}
func (s *sharedReadStore) ListTasks(_ context.Context, f readmodel.TaskFilter) ([]readmodel.TaskSummary, error) {
	s.taskF = &f
	return s.tasks, nil
}
func (s *sharedReadStore) ListNotifications(_ context.Context, f readmodel.NotificationFilter) ([]readmodel.Notification, error) {
	s.noteF = &f
	return s.notes, nil
}
func (s *sharedReadStore) UnreadNotifications(context.Context) (int, error) { return s.unread, nil }
func (s *sharedReadStore) BuildFilter(_ context.Context, spec readmodel.FilterSpec, _ time.Time) (readmodel.TaskFilter, error) {
	return readmodel.TaskFilter{Query: spec.Query}, s.buildErr
}
func (s *sharedReadStore) WorkspaceGIDs(context.Context) ([]string, error) { return s.workspace, nil }
func (s *sharedReadStore) PlanRefusedAt(_ context.Context, _, ws string) (time.Time, bool, error) {
	at, ok := s.refused[ws]
	return at, ok, s.planErr
}

func TestQueryInboxReadsTheNotifications(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	st := &sharedReadStore{state: readmodel.InboxState{Status: readmodel.InboxOK}, notes: qNotes, unread: 4}
	v, err := Query{QueryStore: st}.Inbox(context.Background(), true, 20, now)
	if err != nil {
		t.Fatal(err)
	}
	if st.noteF == nil || !st.noteF.UnreadOnly || st.noteF.Limit != 20 {
		t.Errorf("notification filter = %+v, want unread only, limit 20", st.noteF)
	}
	if st.taskF != nil {
		t.Errorf("recent tasks were read although the inbox can be read: %+v", st.taskF)
	}
	if len(v.Notifications) != 1 || v.Unread != 4 || !v.State.Available() || v.Recent != nil {
		t.Errorf("view = %+v", v)
	}
}

func TestQueryInboxFallsBackToRecentUpdates(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.FixedZone("JST", 9*3600))
	st := &sharedReadStore{state: readmodel.InboxState{Status: readmodel.InboxForbidden}, tasks: qTasks}
	v, err := Query{QueryStore: st}.Inbox(context.Background(), true, 5, now)
	if err != nil {
		t.Fatal(err)
	}
	want := readmodel.TaskFilter{ModifiedAfter: "2026-03-03T03:00:00Z", SortByModified: true, Limit: 5}
	if st.taskF == nil || !reflect.DeepEqual(*st.taskF, want) {
		t.Errorf("recent filter = %+v, want %+v", st.taskF, want)
	}
	if st.noteF != nil {
		t.Errorf("notifications were read although the inbox cannot be: %+v", st.noteF)
	}
	if v.State.Available() || len(v.Recent) != 1 || v.Notifications != nil || v.Unread != 0 {
		t.Errorf("view = %+v", v)
	}
	if got := RecentSince(now); got != want.ModifiedAfter {
		t.Errorf("RecentSince = %s, want %s", got, want.ModifiedAfter)
	}
}

func TestQuerySavedSearchFilterNamesTheSearch(t *testing.T) {
	boom := errors.New("担当者が見つかりません")
	st := &sharedReadStore{buildErr: boom}
	_, err := Query{QueryStore: st}.SavedSearchFilter(context.Background(), "今週", readmodel.FilterSpec{}, time.Now())
	var se *SearchError
	if !errors.As(err, &se) || se.Name != "今週" || !errors.Is(err, boom) {
		t.Errorf("err = %#v, want a SearchError for 今週 wrapping the store's", err)
	}
	st.buildErr = nil
	f, err := Query{QueryStore: st}.SavedSearchFilter(context.Background(), "今週", readmodel.FilterSpec{Query: "x"}, time.Now())
	if err != nil || f.Query != "x" {
		t.Errorf("filter = %+v, %v", f, err)
	}
}

func TestQueryPlanOffEverywhere(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	early, late := now.Add(-2*time.Hour), now.Add(-time.Hour)
	q := func(st *sharedReadStore) Query { return Query{QueryStore: st} }
	ctx := context.Background()

	st := &sharedReadStore{workspace: []string{"w1", "w2"}, refused: map[string]time.Time{"w1": early, "w2": late}}
	from, until, off := q(st).PlanOffEverywhere(ctx, plan.Tags, now)
	if !off || !from.Equal(late) || !until.Equal(early.Add(plan.RecheckAfter)) {
		t.Errorf("both refused: [%v, %v) %v, want [%v, %v) true", from, until, off, late, early.Add(plan.RecheckAfter))
	}
	if _, _, off := q(st).PlanOffIn(ctx, plan.Tags, "w2", now); !off {
		t.Error("w2 refused an hour ago is not off")
	}

	st.refused = map[string]time.Time{"w1": early} // w2 offers it
	if _, _, off := q(st).PlanOffEverywhere(ctx, plan.Tags, now); off {
		t.Error("off everywhere although w2 offers it")
	}

	st.refused = map[string]time.Time{"w1": early, "w2": late}
	st.planErr = errors.New("db locked") // a refusal that cannot be read hides nothing
	if _, _, off := q(st).PlanOffEverywhere(ctx, plan.Tags, now); off {
		t.Error("off although the refusals cannot be read")
	}

	st = &sharedReadStore{} // no workspace known
	if _, _, off := q(st).PlanOffEverywhere(ctx, plan.Tags, now); off {
		t.Error("off although no workspace is known")
	}
}
