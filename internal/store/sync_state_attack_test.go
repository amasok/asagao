package store

import (
	"context"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/domain/plan"
	"github.com/amasok/asagao/internal/readmodel"
)

// TestSyncStateAttackLegacyRawInsertCompatibility verifies that a database populated with
// raw legacy sync_state keys directly via SQL can be opened, read, updated, and cleared
// through the new typed *Store methods.
func TestSyncStateAttackLegacyRawInsertCompatibility(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "legacy.db")

	s, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	tRef := time.Date(2026, 11, 18, 12, 34, 56, 0, time.UTC)
	tStr := tRef.Format(time.RFC3339)

	rawEntries := map[string]string{
		"plan_unavailable:tags:w1":     tStr,
		"events:p1":                    "sync_tok_legacy",
		"ui:collapsed:111":             `["sec1"]`,
		"ui:collapsed:my":              `["sec_my"]`,
		"ui:collapsed:inbox":           `["sec_in"]`,
		"ui:layout":                    `{"pane":42}`,
		"custom_id:PROJ-99":            "t99",
		"me_gid":                       "u_legacy",
		"last_sync":                    tStr,
		"last_full_sync":               tStr,
		"last_poll":                    tStr,
		"field_options_locked:f1":      tStr,
		"project_fields_locked:p1":     tStr,
		"project_tasks:p1":             readmodel.TasksOpen,
		"my_tasks:w1":                  readmodel.TasksAll,
		"task_order:p1":                "t1 t2",
		"subtask_order":                "t1a t1b",
		"goals:w1":                     tStr,
		"portfolios:w1":                tStr,
		"teams:w1":                     tStr,
		"custom_fields:p1":             tStr,
		"custom_types:p1":              tStr,
		"workspace_custom_fields:w1":   tStr,
		"user_task_list:w1":            "utl_legacy",
		"gid:local:t1":                 "real_t1",
		"story:local:s1":               "real_s1",
		"att:local:a1":                 "real_a1",
		"status:local:st1":             "real_st1",
		"tag:local:tg1":                "real_tg1",
		"section:local:sc1":            "real_sc1",
		"time:local:tm1":               "real_tm1",
		"enum_option:local:eo1":        "real_eo1",
	}
	for k, v := range rawEntries {
		if _, err := s.db.ExecContext(ctx, "INSERT OR REPLACE INTO sync_state(key, value) VALUES (?, ?)", k, v); err != nil {
			t.Fatalf("insert %q: %v", k, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// Re-open the existing DB with a fresh Store instance.
	s, err = Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if at, ok, err := s.PlanRefusedAt(ctx, plan.Tags, "w1"); err != nil || !ok || !at.Equal(tRef) {
		t.Fatalf("PlanRefusedAt = (%v, %v, %v), want (%v, true, nil)", at, ok, err, tRef)
	}
	if err := s.ClearPlanRefusal(ctx, plan.Tags, "w1"); err != nil {
		t.Fatal(err)
	}
	if at, ok, err := s.PlanRefusedAt(ctx, plan.Tags, "w1"); err != nil || ok || !at.IsZero() {
		t.Fatalf("PlanRefusedAt after Clear = (%v, %v, %v), want zero/false", at, ok, err)
	}

	if got, err := s.SyncedMeGID(ctx); err != nil || got != "u_legacy" {
		t.Fatalf("SyncedMeGID = (%q, %v)", got, err)
	}
	if got, err := s.LastSyncAt(ctx); err != nil || !got.Equal(tRef) {
		t.Fatalf("LastSyncAt = (%v, %v)", got, err)
	}
	if got, err := s.LastFullSyncAt(ctx); err != nil || !got.Equal(tRef) {
		t.Fatalf("LastFullSyncAt = (%v, %v)", got, err)
	}
	if got, err := s.LastPollAt(ctx); err != nil || !got.Equal(tRef) {
		t.Fatalf("LastPollAt = (%v, %v)", got, err)
	}
	if got, err := s.EventsToken(ctx, "p1"); err != nil || got != "sync_tok_legacy" {
		t.Fatalf("EventsToken = (%q, %v)", got, err)
	}
	if got, err := s.CollapsedGroups(ctx, "111"); err != nil || got != `["sec1"]` {
		t.Fatalf("CollapsedGroups(111) = (%q, %v)", got, err)
	}
	if got, err := s.CollapsedGroups(ctx, "my"); err != nil || got != `["sec_my"]` {
		t.Fatalf("CollapsedGroups(my) = (%q, %v)", got, err)
	}
	if got, err := s.CollapsedGroups(ctx, "inbox"); err != nil || got != `["sec_in"]` {
		t.Fatalf("CollapsedGroups(inbox) = (%q, %v)", got, err)
	}
	if got, err := s.LayoutState(ctx); err != nil || got != `{"pane":42}` {
		t.Fatalf("LayoutState = (%q, %v)", got, err)
	}
	if got, err := s.CustomIDTaskGID(ctx, "PROJ-99"); err != nil || got != "t99" {
		t.Fatalf("CustomIDTaskGID = (%q, %v)", got, err)
	}
	if got, err := s.FieldOptionsLocked(ctx, "f1"); err != nil || !got {
		t.Fatalf("FieldOptionsLocked = (%v, %v)", got, err)
	}
	if got, err := s.ProjectFieldsLocked(ctx, "p1"); err != nil || !got {
		t.Fatalf("ProjectFieldsLocked = (%v, %v)", got, err)
	}
	if states, err := s.StoredProjectTaskStates(ctx); err != nil || !reflect.DeepEqual(states, map[string]string{"p1": readmodel.TasksOpen}) {
		t.Fatalf("StoredProjectTaskStates = (%v, %v)", states, err)
	}
	if st, err := s.MyTaskState(ctx, "w1"); err != nil || st != readmodel.TasksAll {
		t.Fatalf("MyTaskState = (%q, %v)", st, err)
	}
	if states, err := s.MyTaskStates(ctx); err != nil || !reflect.DeepEqual(states, map[string]string{"w1": readmodel.TasksAll}) {
		t.Fatalf("MyTaskStates = (%v, %v)", states, err)
	}
	if got, err := s.PendingSectionOrders(ctx, "p1"); err != nil || got != "t1 t2" {
		t.Fatalf("PendingSectionOrders = (%q, %v)", got, err)
	}
	if got, err := s.PendingSubtaskOrders(ctx); err != nil || got != "t1a t1b" {
		t.Fatalf("PendingSubtaskOrders = (%q, %v)", got, err)
	}

	fetchedChecks := map[string]func() (string, error){
		"goals:w1":                   func() (string, error) { return s.GoalsFetchedAt(ctx, "w1") },
		"portfolios:w1":              func() (string, error) { return s.PortfoliosFetchedAt(ctx, "w1") },
		"teams:w1":                   func() (string, error) { return s.WorkspaceTeamsFetchedAt(ctx, "w1") },
		"custom_fields:p1":           func() (string, error) { return s.ProjectCustomFieldsFetchedAt(ctx, "p1") },
		"custom_types:p1":            func() (string, error) { return s.ProjectCustomTypesFetchedAt(ctx, "p1") },
		"workspace_custom_fields:w1": func() (string, error) { return s.WorkspaceCustomFieldsFetchedAt(ctx, "w1") },
	}
	for name, fn := range fetchedChecks {
		if got, err := fn(); err != nil || got != tStr {
			t.Errorf("%s = (%q, %v), want %q", name, got, err, tStr)
		}
	}

	// Verify all 8 ResultKind values read legacy prefixes accurately.
	resultCases := []struct {
		kind  readmodel.ResultKind
		local string
		want  string
	}{
		{readmodel.ResultTask, "local:t1", "real_t1"},
		{readmodel.ResultStory, "local:s1", "real_s1"},
		{readmodel.ResultAttachment, "local:a1", "real_a1"},
		{readmodel.ResultStatus, "local:st1", "real_st1"},
		{readmodel.ResultTag, "local:tg1", "real_tg1"},
		{readmodel.ResultSection, "local:sc1", "real_sc1"},
		{readmodel.ResultTimeEntry, "local:tm1", "real_tm1"},
		{readmodel.ResultEnumOption, "local:eo1", "real_eo1"},
	}
	for _, tc := range resultCases {
		real, waiting, err := s.LocalResult(ctx, tc.kind, tc.local)
		if err != nil || waiting || real != tc.want {
			t.Errorf("LocalResult(%q, %q) = (%q, %v, %v), want (%q, false, nil)", tc.kind, tc.local, real, waiting, err, tc.want)
		}
		rec, err := s.RecordedResults(ctx, tc.kind)
		if err != nil || !rec[tc.want] || len(rec) != 1 {
			t.Errorf("RecordedResults(%q) = (%v, %v), want map[%q:true]", tc.kind, rec, err, tc.want)
		}
	}

	// Update and clear legacy string-valued keys via typed methods.
	must(t, s.SetEventsToken(ctx, "p1", "tok_v2"))
	if got, err := s.EventsToken(ctx, "p1"); err != nil || got != "tok_v2" {
		t.Errorf("updated EventsToken = (%q, %v)", got, err)
	}
	must(t, s.SetLayoutState(ctx, `{"pane":80}`))
	if got, err := s.LayoutState(ctx); err != nil || got != `{"pane":80}` {
		t.Errorf("updated LayoutState = (%q, %v)", got, err)
	}
	must(t, s.SetCollapsedGroups(ctx, "my", ""))
	if got, err := s.CollapsedGroups(ctx, "my"); err != nil || got != "" {
		t.Errorf("cleared CollapsedGroups = (%q, %v)", got, err)
	}
	must(t, s.SetCustomIDTaskGID(ctx, "PROJ-99", ""))
	if got, err := s.CustomIDTaskGID(ctx, "PROJ-99"); err != nil || got != "" {
		t.Errorf("cleared CustomIDTaskGID = (%q, %v)", got, err)
	}
	must(t, s.SetPendingSectionOrders(ctx, "p1", ""))
	if got, err := s.PendingSectionOrders(ctx, "p1"); err != nil || got != "" {
		t.Errorf("cleared PendingSectionOrders = (%q, %v)", got, err)
	}
	must(t, s.SetPendingSubtaskOrders(ctx, ""))
	if got, err := s.PendingSubtaskOrders(ctx); err != nil || got != "" {
		t.Errorf("cleared PendingSubtaskOrders = (%q, %v)", got, err)
	}
	must(t, s.SetMyTaskState(ctx, "w1", readmodel.TasksOpen))
	if got, err := s.MyTaskState(ctx, "w1"); err != nil || got != readmodel.TasksOpen {
		t.Errorf("updated MyTaskState = (%q, %v)", got, err)
	}
	must(t, s.SetProjectTaskState(ctx, "p1", readmodel.TasksAll))
	if got, err := s.StoredProjectTaskStates(ctx); err != nil || got["p1"] != readmodel.TasksAll {
		t.Errorf("updated StoredProjectTaskStates = (%v, %v)", got, err)
	}
}

// TestSyncStateAttackPruneProjectsAndWorkspaces verifies PruneProjects and PruneWorkspaces
// delete per-project and per-workspace legacy sync_state keys while preserving global keys
// and non-project ui:collapsed scopes ("my", "inbox").
func TestSyncStateAttackPruneProjectsAndWorkspaces(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "prune.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if err := st.UpsertWorkspaces(ctx, []asana.Workspace{{GID: "w1", Name: "W1"}, {GID: "w2", Name: "W2"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertProjects(ctx, "w1", []asana.Project{{GID: "101"}, {GID: "102"}}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertProjects(ctx, "w2", []asana.Project{{GID: "201"}}, "me"); err != nil {
		t.Fatal(err)
	}

	seed := map[string]string{
		// Project 101 (kept) and 102 (pruned)
		"events:101":                "e1",
		"events:102":                "e2",
		"custom_fields:101":         "2026-11-18T10:00:00Z",
		"custom_fields:102":         "2026-11-18T10:00:00Z",
		"project_tasks:101":         "open",
		"project_tasks:102":         "open",
		"task_order:101":            "1 2",
		"task_order:102":            "3 4",
		"ui:collapsed:101":          "[]",
		"ui:collapsed:102":          "[]",
		// Non-project collapsed scopes & global UI/sync keys (must survive)
		"ui:collapsed:my":           `["a"]`,
		"ui:collapsed:inbox":        `["b"]`,
		"ui:collapsed:custom_scope": `["c"]`,
		"ui:layout":                 `{"w":50}`,
		"subtask_order":             "s1 s2",
		"me_gid":                    "me",
		"last_sync":                 "2026-11-18T10:00:00Z",
		"plan_unavailable:tags":     "2026-11-18T10:00:00Z", // legacy workspace-less key must not match glob "*:w2"
		// Workspace w1 (kept) and w2 (pruned)
		"plan_unavailable:tags:w1":     "2026-11-18T10:00:00Z",
		"plan_unavailable:tags:w2":     "2026-11-18T10:00:00Z",
		"plan_unavailable:sections:w2": "2026-11-18T10:00:00Z",
		"goals:w1":                     "2026-11-18T10:00:00Z",
		"goals:w2":                     "2026-11-18T10:00:00Z",
		"portfolios:w1":                "2026-11-18T10:00:00Z",
		"portfolios:w2":                "2026-11-18T10:00:00Z",
		"teams:w1":                     "2026-11-18T10:00:00Z",
		"teams:w2":                     "2026-11-18T10:00:00Z",
		"my_tasks:w1":                  "all",
		"my_tasks:w2":                  "all",
		"events:201":                   "e201",
		"ui:collapsed:201":             "[]",
	}
	for k, v := range seed {
		if err := st.SetSyncState(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}

	if err := st.PruneProjects(ctx, "w1", []asana.Project{{GID: "101"}}); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"events:102", "custom_fields:102", "project_tasks:102", "task_order:102", "ui:collapsed:102"} {
		if v, _ := st.SyncState(ctx, k); v != "" {
			t.Errorf("PruneProjects left %q = %q, want deleted", k, v)
		}
	}
	for _, k := range []string{"events:101", "custom_fields:101", "project_tasks:101", "task_order:101", "ui:collapsed:101",
		"ui:collapsed:my", "ui:collapsed:inbox", "ui:collapsed:custom_scope", "ui:layout", "subtask_order", "me_gid", "last_sync"} {
		if v, _ := st.SyncState(ctx, k); v == "" {
			t.Errorf("PruneProjects deleted %q, want kept", k)
		}
	}

	if err := st.PruneWorkspaces(ctx, []asana.Workspace{{GID: "w1"}}); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"plan_unavailable:tags:w2", "plan_unavailable:sections:w2", "goals:w2", "portfolios:w2", "teams:w2", "my_tasks:w2", "events:201", "ui:collapsed:201"} {
		if v, _ := st.SyncState(ctx, k); v != "" {
			t.Errorf("PruneWorkspaces left %q = %q, want deleted", k, v)
		}
	}
	for _, k := range []string{"plan_unavailable:tags:w1", "plan_unavailable:tags", "goals:w1", "portfolios:w1", "teams:w1", "my_tasks:w1",
		"ui:collapsed:my", "ui:collapsed:inbox", "ui:layout"} {
		if v, _ := st.SyncState(ctx, k); v == "" {
			t.Errorf("PruneWorkspaces deleted %q, want kept", k)
		}
	}

	// Newer per-project / per-workspace keys should also be cleaned up when their project/workspace is pruned.
	t.Run("newer project and workspace keys", func(t *testing.T) {
		s2, err := Open(ctx, filepath.Join(t.TempDir(), "prune_newer.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer s2.Close()
		must(t, s2.UpsertWorkspaces(ctx, []asana.Workspace{{GID: "w1", Name: "W1"}, {GID: "w2", Name: "W2"}}))
		must(t, s2.UpsertProjects(ctx, "w1", []asana.Project{{GID: "101"}, {GID: "102"}}, "me"))
		for k, v := range map[string]string{
			"custom_types:102":           "2026-11-18T10:00:00Z",
			"project_fields_locked:102":  "2026-11-18T10:00:00Z",
			"workspace_custom_fields:w2": "2026-11-18T10:00:00Z",
			"user_task_list:w2":          "utl2",
		} {
			must(t, s2.SetSyncState(ctx, k, v))
		}
		must(t, s2.PruneProjects(ctx, "w1", []asana.Project{{GID: "101"}}))
		must(t, s2.PruneWorkspaces(ctx, []asana.Workspace{{GID: "w1"}}))
		for _, k := range []string{"custom_types:102", "project_fields_locked:102", "workspace_custom_fields:w2", "user_task_list:w2"} {
			if v, _ := s2.SyncState(ctx, k); v != "" {
				t.Errorf("key %q leaked after prune = %q, want deleted", k, v)
			}
		}
	})
}

// TestSyncStateAttackPruneResultsAllPrefixes verifies that all 8 legacy result prefixes
// are recognized by PruneResults and deleted after ResultGrace when unreferenced.
func TestSyncStateAttackPruneResultsAllPrefixes(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)

	prefixes := []struct {
		kind   readmodel.ResultKind
		prefix string
	}{
		{readmodel.ResultTask, "gid:"},
		{readmodel.ResultStory, "story:"},
		{readmodel.ResultAttachment, "att:"},
		{readmodel.ResultStatus, "status:"},
		{readmodel.ResultTag, "tag:"},
		{readmodel.ResultSection, "section:"},
		{readmodel.ResultTimeEntry, "time:"},
		{readmodel.ResultEnumOption, "enum_option:"},
	}

	for _, tc := range prefixes {
		t.Run(string(tc.kind), func(t *testing.T) {
			st := resultsStore(t)
			key := tc.prefix + "local:x1"
			must(t, st.SetSyncState(ctx, key, "real_1"))

			real, waiting, err := st.LocalResult(ctx, tc.kind, "local:x1")
			if err != nil || waiting || real != "real_1" {
				t.Fatalf("LocalResult = (%q, %v, %v)", real, waiting, err)
			}
			rec, err := st.RecordedResults(ctx, tc.kind)
			if err != nil || !rec["real_1"] {
				t.Fatalf("RecordedResults = (%v, %v)", rec, err)
			}

			// First prune records result_seen; second prune after ResultGrace deletes it.
			if n, err := st.PruneResults(ctx, now); err != nil || n != 0 {
				t.Fatalf("first PruneResults = (%d, %v)", n, err)
			}
			if n, err := st.PruneResults(ctx, now.Add(ResultGrace)); err != nil || n != 1 {
				t.Fatalf("second PruneResults(%q) deleted = %d (err=%v), want 1", key, n, err)
			}
			if v, _ := st.SyncState(ctx, key); v != "" {
				t.Fatalf("after PruneResults, %q = %q, want deleted", key, v)
			}
		})
	}
}

// TestSyncStateAttackCorruptValuesAndFutureTimestamps ensures corrupt, empty, non-RFC3339,
// or future timestamps in sync_state never lock features forever or return parse errors.
func TestSyncStateAttackCorruptValuesAndFutureTimestamps(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)

	badValues := []string{
		"",
		"corrupt",
		"2026-11-18",
		"2026/11/18 10:00:00",
		"1700000000",
		"2026-11-18T10:00:00",
		"9999-99-99T00:00:00Z",
		"\x00\x1b",
	}
	for _, bad := range badValues {
		must(t, st.SetSyncState(ctx, "plan_unavailable:tags:w1", bad))
		if at, ok, err := st.PlanRefusedAt(ctx, plan.Tags, "w1"); err != nil || ok || !at.IsZero() {
			t.Errorf("PlanRefusedAt(%q) = (%v, %v, %v), want zero/false/nil", bad, at, ok, err)
		}
		must(t, st.SetSyncState(ctx, "last_sync", bad))
		if at, err := st.LastSyncAt(ctx); err != nil || !at.IsZero() {
			t.Errorf("LastSyncAt(%q) = (%v, %v), want zero/nil", bad, at, err)
		}
		must(t, st.SetSyncState(ctx, "last_full_sync", bad))
		if at, err := st.LastFullSyncAt(ctx); err != nil || !at.IsZero() {
			t.Errorf("LastFullSyncAt(%q) = (%v, %v), want zero/nil", bad, at, err)
		}
		must(t, st.SetSyncState(ctx, "last_poll", bad))
		if at, err := st.LastPollAt(ctx); err != nil || !at.IsZero() {
			t.Errorf("LastPollAt(%q) = (%v, %v), want zero/nil", bad, at, err)
		}
		if stats, err := st.Stats(ctx); err != nil || stats.LastSync != nil {
			t.Errorf("Stats(%q) = (%+v, %v), want LastSync=nil", bad, stats, err)
		}
	}

	// Future timestamp in plan_unavailable must not permanently lock via plan.OffAt.
	future := now.Add(48 * time.Hour)
	must(t, st.RecordPlanRefusal(ctx, plan.Tags, "w1", future))
	at, ok, err := st.PlanRefusedAt(ctx, plan.Tags, "w1")
	if err != nil || !ok || !at.Equal(future) {
		t.Fatalf("PlanRefusedAt(future) = (%v, %v, %v)", at, ok, err)
	}
	if plan.OffAt(at, now) {
		t.Fatal("future PlanRefusedAt locked feature under plan.OffAt")
	}

	// RFC3339Nano and non-UTC RFC3339 stored by external/older writers parse cleanly.
	jst := time.FixedZone("JST", 9*3600)
	tNano := time.Date(2026, 11, 18, 9, 0, 0, 123456789, jst)
	must(t, st.SetSyncState(ctx, "plan_unavailable:goals:w1", tNano.Format(time.RFC3339Nano)))
	if got, ok, err := st.PlanRefusedAt(ctx, plan.Goals, "w1"); err != nil || !ok || !got.Equal(tNano) {
		t.Fatalf("RFC3339Nano PlanRefusedAt = (%v, %v, %v), want %v", got, ok, err, tNano)
	}

	// Legacy boolean/timestamp lock keys: non-empty means locked, empty means unlocked.
	must(t, st.SetSyncState(ctx, "field_options_locked:f1", "1"))
	if locked, err := st.FieldOptionsLocked(ctx, "f1"); err != nil || !locked {
		t.Fatalf("FieldOptionsLocked(legacy '1') = (%v, %v)", locked, err)
	}
	must(t, st.SetFieldOptionsLocked(ctx, "f1", time.Time{}))
	if locked, err := st.FieldOptionsLocked(ctx, "f1"); err != nil || locked {
		t.Fatalf("FieldOptionsLocked(cleared) = (%v, %v)", locked, err)
	}
	must(t, st.SetSyncState(ctx, "project_fields_locked:p1", "1"))
	if locked, err := st.ProjectFieldsLocked(ctx, "p1"); err != nil || !locked {
		t.Fatalf("ProjectFieldsLocked(legacy '1') = (%v, %v)", locked, err)
	}
	must(t, st.SetProjectFieldsLocked(ctx, "p1", time.Time{}))
	if locked, err := st.ProjectFieldsLocked(ctx, "p1"); err != nil || locked {
		t.Fatalf("ProjectFieldsLocked(cleared) = (%v, %v)", locked, err)
	}
}

// TestSyncStateAttackUnknownResultKindAndZeroClear tests error handling for invalid
// ResultKind values (even with non-local GIDs) and clearing behavior with zero time.Time{}.
func TestSyncStateAttackUnknownResultKindAndZeroClear(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	badKinds := []readmodel.ResultKind{"", ":gid", "gid:", "unknown", "TASK", "time", "stories", "att"}
	for _, kind := range badKinds {
		for _, gid := range []string{"local:1", "12345", ""} {
			if _, _, err := st.LocalResult(ctx, kind, gid); err == nil {
				t.Errorf("LocalResult(%q, %q) succeeded, want error", kind, gid)
			}
		}
		if _, err := st.RecordedResults(ctx, kind); err == nil {
			t.Errorf("RecordedResults(%q) succeeded, want error", kind)
		}
	}

	// Zero time.Time{} clears existing timestamp sync_state entries.
	t1 := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	setAndClear := []struct {
		name string
		set  func(time.Time) error
		raw  string
	}{
		{"last_sync", func(at time.Time) error { return st.SetLastSyncAt(ctx, at) }, "last_sync"},
		{"last_full_sync", func(at time.Time) error { return st.SetLastFullSyncAt(ctx, at) }, "last_full_sync"},
		{"last_poll", func(at time.Time) error { return st.SetLastPollAt(ctx, at) }, "last_poll"},
		{"teams", func(at time.Time) error { return st.SetWorkspaceTeamsFetchedAt(ctx, "w1", at) }, "teams:w1"},
		{"custom_fields", func(at time.Time) error { return st.SetProjectCustomFieldsFetchedAt(ctx, "p1", at) }, "custom_fields:p1"},
		{"custom_types", func(at time.Time) error { return st.SetProjectCustomTypesFetchedAt(ctx, "p1", at) }, "custom_types:p1"},
		{"workspace_custom_fields", func(at time.Time) error { return st.SetWorkspaceCustomFieldsFetchedAt(ctx, "w1", at) }, "workspace_custom_fields:w1"},
		{"goals", func(at time.Time) error { return st.SetGoalsFetchedAt(ctx, "w1", at) }, "goals:w1"},
		{"portfolios", func(at time.Time) error { return st.SetPortfoliosFetchedAt(ctx, "w1", at) }, "portfolios:w1"},
		{"plan_unavailable", func(at time.Time) error { return st.RecordPlanRefusal(ctx, plan.Tags, "w1", at) }, "plan_unavailable:tags:w1"},
	}
	for _, tc := range setAndClear {
		must(t, tc.set(t1))
		if v, _ := st.SyncState(ctx, tc.raw); v == "" {
			t.Fatalf("%s: expected non-empty value after set", tc.name)
		}
		must(t, tc.set(time.Time{}))
		if v, _ := st.SyncState(ctx, tc.raw); v != "" {
			t.Errorf("%s: after zero set = %q, want empty", tc.name, v)
		}
	}
}

// TestSyncStateAttackConcurrentAccess exercises concurrent typed sync_state reads/writes
// under -race to ensure SQLite connection pooling and busy handling remain race-free.
func TestSyncStateAttackConcurrentAccess(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				at := now.Add(time.Duration(i) * time.Minute)
				if err := st.RecordPlanRefusal(ctx, plan.Tags, "w1", at); err != nil {
					t.Errorf("g=%d RecordPlanRefusal: %v", id, err)
				}
				if _, _, err := st.PlanRefusedAt(ctx, plan.Tags, "w1"); err != nil {
					t.Errorf("g=%d PlanRefusedAt: %v", id, err)
				}
				if err := st.SetLastSyncAt(ctx, at); err != nil {
					t.Errorf("g=%d SetLastSyncAt: %v", id, err)
				}
				if _, err := st.LastSyncAt(ctx); err != nil {
					t.Errorf("g=%d LastSyncAt: %v", id, err)
				}
				if i%5 == 0 {
					if err := st.ClearPlanRefusal(ctx, plan.Tags, "w1"); err != nil {
						t.Errorf("g=%d ClearPlanRefusal: %v", id, err)
					}
				}
			}
		}(g)
	}
	wg.Wait()
}
