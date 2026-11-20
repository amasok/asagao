package store

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/domain/plan"
	"github.com/amasok/asagao/internal/readmodel"
)

// All existing SQLite sync_state keys and result prefixes must preserve their exact
// persisted string values for store.db compatibility.
func TestSyncStateKeyCompatibility(t *testing.T) {
	wantKeys := map[string]string{
		keyMeGID:                 "me_gid",
		keyLastSync:              "last_sync",
		keyLastFullSync:          "last_full_sync",
		keyLastPoll:              "last_poll",
		keyEvents:                "events:",
		keyPlanUnavailable:       "plan_unavailable:",
		keyCustomFields:          "custom_fields:",
		keyCustomTypes:           "custom_types:",
		keyUserTaskList:          "user_task_list:",
		keyWorkspaceCustomFields: "workspace_custom_fields:",
		keyFieldOptionsLocked:    "field_options_locked:",
		keyProjectFieldsLocked:   "project_fields_locked:",
		keyTeams:                 "teams:",
		keyTimeEntries:           "time_entries:",
		keyCustomID:              "custom_id:",
		keyProjectTasks:          "project_tasks:",
		keyMyTasks:               "my_tasks:",
		keyTaskOrder:             "task_order:",
		keySubtaskOrder:          "subtask_order",
		keyGoals:                 "goals:",
		keyPortfolios:            "portfolios:",
		keyCollapsed:             "ui:collapsed:",
		keyLayout:                "ui:layout",
	}
	if len(wantKeys) != 23 {
		t.Fatalf("expected 23 distinct keys, got %d", len(wantKeys))
	}
	for got, want := range wantKeys {
		if got != want {
			t.Errorf("key constant = %q, want %q", got, want)
		}
	}

	wantPrefixes := map[readmodel.ResultKind]string{
		readmodel.ResultTask:       "gid:",
		readmodel.ResultStory:      "story:",
		readmodel.ResultAttachment: "att:",
		readmodel.ResultStatus:     "status:",
		readmodel.ResultTag:        "tag:",
		readmodel.ResultSection:    "section:",
		readmodel.ResultTimeEntry:  "time:",
		readmodel.ResultEnumOption: "enum_option:",
	}
	for kind, want := range wantPrefixes {
		got, err := resultPrefix(kind)
		if err != nil {
			t.Fatalf("resultPrefix(%q) err = %v", kind, err)
		}
		if got != want {
			t.Errorf("resultPrefix(%q) = %q, want %q", kind, got, want)
		}
	}
	if _, err := resultPrefix(readmodel.ResultKind("unknown")); err == nil {
		t.Fatal("resultPrefix(unknown) succeeded, want error")
	}
}

func TestPlanKeyDistinct(t *testing.T) {
	if planKey(plan.Tags, "w1") != "plan_unavailable:tags:w1" {
		t.Fatalf("planKey = %q, want legacy format", planKey(plan.Tags, "w1"))
	}
	if planKey(plan.Tags, "w1") == planKey(plan.Tags, "w2") || planKey(plan.Tags, "w1") == planKey(plan.Sections, "w1") {
		t.Fatal("keys collide")
	}
	if planKey(plan.Tags, "w1") == keyPlanUnavailable+plan.Tags {
		t.Fatal("workspace plan key collides with legacy global key")
	}
}

func TestSyncStateTypedPlanRefusal(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)

	// Empty state returns false.
	if at, ok, err := s.PlanRefusedAt(ctx, plan.Tags, "w1"); err != nil || ok || !at.IsZero() {
		t.Fatalf("empty = (%v, %v, %v)", at, ok, err)
	}

	// Record writes exact legacy key & RFC3339 UTC.
	if err := s.RecordPlanRefusal(ctx, plan.Tags, "w1", now.In(time.FixedZone("JST", 9*3600))); err != nil {
		t.Fatal(err)
	}
	if raw, err := s.SyncState(ctx, "plan_unavailable:tags:w1"); err != nil || raw != now.Format(time.RFC3339) {
		t.Fatalf("raw key = %q, %v", raw, err)
	}
	at, ok, err := s.PlanRefusedAt(ctx, plan.Tags, "w1")
	if err != nil || !ok || !at.Equal(now) {
		t.Fatalf("PlanRefusedAt = (%v, %v, %v), want %v/true", at, ok, err, now)
	}

	// Corrupt values must not lock the feature forever.
	for _, corrupt := range []string{"", "not-a-time", "2026-11-18"} {
		if err := s.SetSyncState(ctx, "plan_unavailable:tags:w1", corrupt); err != nil {
			t.Fatal(err)
		}
		if at, ok, err := s.PlanRefusedAt(ctx, plan.Tags, "w1"); err != nil || ok || !at.IsZero() {
			t.Fatalf("corrupt %q: got (%v, %v, %v), want zero/false/nil", corrupt, at, ok, err)
		}
	}

	// Clear clears the legacy key.
	if err := s.RecordPlanRefusal(ctx, plan.Tags, "w1", now); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearPlanRefusal(ctx, plan.Tags, "w1"); err != nil {
		t.Fatal(err)
	}
	if raw, _ := s.SyncState(ctx, "plan_unavailable:tags:w1"); raw != "" {
		t.Fatalf("after Clear: raw = %q, want empty", raw)
	}
}

func TestSyncStateTypedMethodsRoundTripAndLegacyKeys(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	t1 := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	t1Str := t1.Format(time.RFC3339)

	// Account & sync timestamps (newTestStore seeds keyMeGID = "me")
	if me, err := s.SyncedMeGID(ctx); err != nil || me != "me" {
		t.Fatalf("initial SyncedMeGID = %q, %v", me, err)
	}
	must(t, s.SetMeGID(ctx, "u1"))
	if me, err := s.SyncedMeGID(ctx); err != nil || me != "u1" {
		t.Fatalf("SyncedMeGID = %q, %v", me, err)
	}
	if raw, _ := s.SyncState(ctx, "me_gid"); raw != "u1" {
		t.Fatalf("me_gid raw = %q", raw)
	}

	must(t, s.SetLastSyncAt(ctx, t1))
	must(t, s.SetLastFullSyncAt(ctx, t1))
	must(t, s.SetLastPollAt(ctx, t1))
	if got, _ := s.LastSyncAt(ctx); !got.Equal(t1) {
		t.Fatalf("LastSyncAt = %v", got)
	}
	if got, _ := s.LastFullSyncAt(ctx); !got.Equal(t1) {
		t.Fatalf("LastFullSyncAt = %v", got)
	}
	if got, _ := s.LastPollAt(ctx); !got.Equal(t1) {
		t.Fatalf("LastPollAt = %v", got)
	}
	for k, want := range map[string]string{"last_sync": t1Str, "last_full_sync": t1Str, "last_poll": t1Str} {
		if raw, _ := s.SyncState(ctx, k); raw != want {
			t.Fatalf("%s raw = %q, want %q", k, raw, want)
		}
	}
	// Corrupt last_sync returns zero without error; zero Set clears it.
	must(t, s.SetSyncState(ctx, "last_sync", "broken"))
	if got, err := s.LastSyncAt(ctx); err != nil || !got.IsZero() {
		t.Fatalf("corrupt LastSyncAt = (%v, %v)", got, err)
	}
	must(t, s.SetLastSyncAt(ctx, time.Time{}))
	if raw, _ := s.SyncState(ctx, "last_sync"); raw != "" {
		t.Fatalf("zero SetLastSyncAt raw = %q", raw)
	}

	// Cursors / fetchedAt
	must(t, s.SetEventsToken(ctx, "p1", "tok1"))
	if tok, _ := s.EventsToken(ctx, "p1"); tok != "tok1" {
		t.Fatalf("EventsToken = %q", tok)
	}
	if raw, _ := s.SyncState(ctx, "events:p1"); raw != "tok1" {
		t.Fatalf("events:p1 = %q", raw)
	}

	must(t, s.SetUserTaskListGID(ctx, "w1", "utl1"))
	if raw, _ := s.SyncState(ctx, "user_task_list:w1"); raw != "utl1" {
		t.Fatalf("user_task_list:w1 = %q", raw)
	}

	must(t, s.SetProjectCustomFieldsFetchedAt(ctx, "p1", t1))
	must(t, s.SetProjectCustomTypesFetchedAt(ctx, "p1", t1))
	must(t, s.SetWorkspaceTeamsFetchedAt(ctx, "w1", t1))
	must(t, s.SetWorkspaceCustomFieldsFetchedAt(ctx, "w1", t1))
	must(t, s.SetGoalsFetchedAt(ctx, "w1", t1))
	must(t, s.SetPortfoliosFetchedAt(ctx, "w1", t1))
	for rawKey, gotFn := range map[string]func() (string, error){
		"custom_fields:p1":           func() (string, error) { return s.ProjectCustomFieldsFetchedAt(ctx, "p1") },
		"custom_types:p1":            func() (string, error) { return s.ProjectCustomTypesFetchedAt(ctx, "p1") },
		"teams:w1":                   func() (string, error) { return s.WorkspaceTeamsFetchedAt(ctx, "w1") },
		"workspace_custom_fields:w1": func() (string, error) { return s.WorkspaceCustomFieldsFetchedAt(ctx, "w1") },
		"goals:w1":                   func() (string, error) { return s.GoalsFetchedAt(ctx, "w1") },
		"portfolios:w1":              func() (string, error) { return s.PortfoliosFetchedAt(ctx, "w1") },
	} {
		if raw, _ := s.SyncState(ctx, rawKey); raw != t1Str {
			t.Fatalf("%s raw = %q, want %q", rawKey, raw, t1Str)
		}
		if got, err := gotFn(); err != nil || got != t1Str {
			t.Fatalf("%s getter = (%q, %v), want %q", rawKey, got, err, t1Str)
		}
	}
	must(t, s.SetWorkspaceTeamsFetchedAt(ctx, "w1", time.Time{}))
	if got, _ := s.WorkspaceTeamsFetchedAt(ctx, "w1"); got != "" {
		t.Fatalf("after zero SetWorkspaceTeamsFetchedAt = %q", got)
	}

	// 403 locks
	if locked, _ := s.FieldOptionsLocked(ctx, "f1"); locked {
		t.Fatal("initial FieldOptionsLocked = true")
	}
	must(t, s.SetFieldOptionsLocked(ctx, "f1", t1))
	if locked, _ := s.FieldOptionsLocked(ctx, "f1"); !locked {
		t.Fatal("FieldOptionsLocked = false")
	}
	if raw, _ := s.SyncState(ctx, "field_options_locked:f1"); raw != t1Str {
		t.Fatalf("field_options_locked:f1 = %q", raw)
	}
	if locked, _ := s.ProjectFieldsLocked(ctx, "p1"); locked {
		t.Fatal("initial ProjectFieldsLocked = true")
	}
	must(t, s.SetProjectFieldsLocked(ctx, "p1", t1))
	if locked, _ := s.ProjectFieldsLocked(ctx, "p1"); !locked {
		t.Fatal("ProjectFieldsLocked = false")
	}
	if raw, _ := s.SyncState(ctx, "project_fields_locked:p1"); raw != t1Str {
		t.Fatalf("project_fields_locked:p1 = %q", raw)
	}

	// Custom ID cache
	must(t, s.SetCustomIDTaskGID(ctx, "ENG-1", "t9"))
	if g, _ := s.CustomIDTaskGID(ctx, "ENG-1"); g != "t9" {
		t.Fatalf("CustomIDTaskGID = %q", g)
	}
	if raw, _ := s.SyncState(ctx, "custom_id:ENG-1"); raw != "t9" {
		t.Fatalf("custom_id:ENG-1 = %q", raw)
	}
	must(t, s.SetCustomIDTaskGID(ctx, "ENG-1", ""))
	if g, _ := s.CustomIDTaskGID(ctx, "ENG-1"); g != "" {
		t.Fatalf("cleared CustomIDTaskGID = %q", g)
	}

	// Lazy sync & order retry
	must(t, s.SetProjectTaskState(ctx, "p1", readmodel.TasksOpen))
	if raw, _ := s.SyncState(ctx, "project_tasks:p1"); raw != readmodel.TasksOpen {
		t.Fatalf("project_tasks:p1 = %q", raw)
	}
	if states, _ := s.StoredProjectTaskStates(ctx); !reflect.DeepEqual(states, map[string]string{"p1": readmodel.TasksOpen}) {
		t.Fatalf("StoredProjectTaskStates = %v", states)
	}
	must(t, s.SetMyTaskState(ctx, "w1", readmodel.TasksAll))
	if raw, _ := s.SyncState(ctx, "my_tasks:w1"); raw != readmodel.TasksAll {
		t.Fatalf("my_tasks:w1 = %q", raw)
	}
	if st, _ := s.MyTaskState(ctx, "w1"); st != readmodel.TasksAll {
		t.Fatalf("MyTaskState = %q", st)
	}
	if states, _ := s.MyTaskStates(ctx); !reflect.DeepEqual(states, map[string]string{"w1": readmodel.TasksAll}) {
		t.Fatalf("MyTaskStates = %v", states)
	}

	must(t, s.SetPendingSectionOrders(ctx, "p1", "s1 s2"))
	if raw, _ := s.SyncState(ctx, "task_order:p1"); raw != "s1 s2" {
		t.Fatalf("task_order:p1 = %q", raw)
	}
	if got, _ := s.PendingSectionOrders(ctx, "p1"); got != "s1 s2" {
		t.Fatalf("PendingSectionOrders = %q", got)
	}
	must(t, s.SetPendingSubtaskOrders(ctx, "t1 t2"))
	if raw, _ := s.SyncState(ctx, "subtask_order"); raw != "t1 t2" {
		t.Fatalf("subtask_order = %q", raw)
	}
	if got, _ := s.PendingSubtaskOrders(ctx); got != "t1 t2" {
		t.Fatalf("PendingSubtaskOrders = %q", got)
	}

	// TUI UI state
	must(t, s.SetCollapsedGroups(ctx, "my", `["g1"]`))
	if raw, _ := s.SyncState(ctx, "ui:collapsed:my"); raw != `["g1"]` {
		t.Fatalf("ui:collapsed:my = %q", raw)
	}
	if got, _ := s.CollapsedGroups(ctx, "my"); got != `["g1"]` {
		t.Fatalf("CollapsedGroups = %q", got)
	}
	must(t, s.SetLayoutState(ctx, `{"split":60}`))
	if raw, _ := s.SyncState(ctx, "ui:layout"); raw != `{"split":60}` {
		t.Fatalf("ui:layout = %q", raw)
	}
	if got, _ := s.LayoutState(ctx); got != `{"split":60}` {
		t.Fatalf("LayoutState = %q", got)
	}
}

func TestResultsRejectUnknownResultKind(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if _, _, err := s.LocalResult(ctx, readmodel.ResultKind("bogus"), "local:1"); err == nil {
		t.Fatal("LocalResult(bogus, local) succeeded, want error")
	}
	// Unknown ResultKind must fail even when GID is not a local GID.
	if _, _, err := s.LocalResult(ctx, readmodel.ResultKind("bogus"), "123"); err == nil {
		t.Fatal("LocalResult(bogus, real) succeeded, want error")
	}
	if _, err := s.RecordedResults(ctx, readmodel.ResultKind("bogus")); err == nil {
		t.Fatal("RecordedResults(bogus) succeeded, want error")
	}

	must(t, s.SetSyncState(ctx, "story:local:c1", "c99"))
	if real, waiting, err := s.LocalResult(ctx, readmodel.ResultStory, "local:c1"); err != nil || waiting || real != "c99" {
		t.Fatalf("LocalResult(story) = (%q, %v, %v)", real, waiting, err)
	}
	must(t, s.SetSyncState(ctx, "gid:local:t1", "t99"))
	res, err := s.RecordedResults(ctx, readmodel.ResultTask)
	if err != nil || !res["t99"] {
		t.Fatalf("RecordedResults(task) = (%v, %v)", res, err)
	}
}
