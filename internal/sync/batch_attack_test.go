package sync

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/store"
)

func openAttackStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestBatchAttackEntryForIntentAndResolvedGIDs(t *testing.T) {
	ctx := context.Background()
	st := openAttackStore(t)
	if err := st.SetSyncState(ctx, store.ResultTag+"local:tag1", "real-tag-1"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSyncState(ctx, "gid:local:dep1", "real-dep-1"); err != nil {
		t.Fatal(err)
	}
	s := &Syncer{Store: st}

	cases := []struct {
		name        string
		op          outbox.Op
		wantWrite   model.BatchWrite
		wantCheck   bool
		wantRefresh bool
		wantProject string
		wantSection string
	}{
		{
			name: "update task milestone subtype is batchable",
			op: outbox.Op{
				Kind:    outbox.KindUpdateTask,
				TaskGID: "t1",
				Payload: map[string]any{"resource_subtype": "milestone", "name": "M1"},
			},
			wantWrite: model.BatchUpdateTask("t1", map[string]any{"resource_subtype": "milestone", "name": "M1"}),
			wantCheck: true,
		},
		{
			name: "set custom field enum",
			op: outbox.Op{
				Kind:    outbox.KindSetCustomField,
				TaskGID: "t1",
				Payload: map[string]any{"field": "cf_enum", "type": "enum", "value": "opt1"},
			},
			wantWrite: model.BatchUpdateTask("t1", map[string]any{"custom_fields": map[string]any{"cf_enum": "opt1"}}),
			wantCheck: true,
		},
		{
			name: "set custom field number",
			op: outbox.Op{
				Kind:    outbox.KindSetCustomField,
				TaskGID: "t1",
				Payload: map[string]any{"field": "cf_num", "type": "number", "value": float64(42)},
			},
			wantWrite: model.BatchUpdateTask("t1", map[string]any{"custom_fields": map[string]any{"cf_num": float64(42)}}),
			wantCheck: true,
		},
		{
			name: "add tag resolves mapped local gid",
			op: outbox.Op{
				Kind:    outbox.KindAddTag,
				TaskGID: "t1",
				Payload: map[string]any{"tag": "local:tag1"},
			},
			wantWrite: model.BatchAddTag("t1", "real-tag-1"),
		},
		{
			name: "remove tag resolves mapped local gid",
			op: outbox.Op{
				Kind:    outbox.KindRemoveTag,
				TaskGID: "t1",
				Payload: map[string]any{"tag": "local:tag1"},
			},
			wantWrite: model.BatchRemoveTag("t1", "real-tag-1"),
		},
		{
			name: "add dependency resolves mapped local gid",
			op: outbox.Op{
				Kind:    outbox.KindAddDependency,
				TaskGID: "t1",
				Payload: map[string]any{"dependency": "local:dep1"},
			},
			wantWrite: model.BatchAddDependency("t1", "real-dep-1"),
		},
		{
			name: "remove dependency resolves mapped local gid",
			op: outbox.Op{
				Kind:    outbox.KindRemoveDependency,
				TaskGID: "t1",
				Payload: map[string]any{"dependency": "local:dep1"},
			},
			wantWrite: model.BatchRemoveDependency("t1", "real-dep-1"),
		},
		{
			name: "move task without project still sets section and refresh",
			op: outbox.Op{
				Kind:    outbox.KindMoveTask,
				TaskGID: "t1",
				Payload: map[string]any{"section": "s9"},
			},
			wantWrite:   model.BatchMoveTaskToSection("s9", "t1"),
			wantRefresh: true,
			wantSection: "s9",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, ok, err := s.batchEntryFor(ctx, tc.op)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !ok {
				t.Fatal("expected ok=true, got false")
			}
			if !reflect.DeepEqual(e.write, tc.wantWrite) {
				t.Fatalf("write = %#v, want %#v", e.write, tc.wantWrite)
			}
			if e.check != tc.wantCheck || e.refresh != tc.wantRefresh || e.project != tc.wantProject || e.section != tc.wantSection {
				t.Fatalf("entry meta mismatch: %+v", e)
			}
		})
	}
}

func TestBatchAttackEntryForExclusions(t *testing.T) {
	ctx := context.Background()
	st := openAttackStore(t)
	now := time.Date(2026, 11, 20, 3, 0, 0, 0, time.UTC)

	// Set up workspace/project/task/custom fields so we can test waitsForField exclusions.
	if err := st.UpsertProjects(ctx, "w1", []model.Project{{GID: "p1", Name: "P1"}}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertTasks(ctx, []model.Task{{
		GID:         "t1",
		Name:        "Task 1",
		Memberships: []model.Membership{{Project: &model.Ref{GID: "p1"}}},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceProjectCustomFields(ctx, "p1", []model.CustomFieldSetting{{
		CustomField: model.CustomField{
			GID:             "cf1",
			Name:            "Priority",
			ResourceSubtype: store.CFEnum,
			EnumOptions:     []model.EnumOption{{GID: "o1", Name: "High"}},
		},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceWorkspaceCustomFields(ctx, "w1", []model.CustomField{
		{GID: "cf1", Name: "Priority", ResourceSubtype: store.CFEnum, EnumOptions: []model.EnumOption{{GID: "o1", Name: "High"}}},
		{GID: "cf_pending", Name: "PendingField", ResourceSubtype: store.CFText},
	}); err != nil {
		t.Fatal(err)
	}
	localOpt, _, err := st.AddEnumOptionLocal(ctx, "cf1", "Urgent", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetProjectFieldLocal(ctx, "p1", "cf_pending", true, now); err != nil {
		t.Fatal(err)
	}

	s := &Syncer{Store: st}

	excluded := []struct {
		name string
		op   outbox.Op
	}{
		{"empty task gid", outbox.Op{Kind: outbox.KindUpdateTask, TaskGID: "", Payload: map[string]any{"name": "x"}}},
		{"local task gid on move", outbox.Op{Kind: outbox.KindMoveTask, TaskGID: "local:t1", Payload: map[string]any{"section": "s1"}}},
		{"local task gid on add tag", outbox.Op{Kind: outbox.KindAddTag, TaskGID: "local:t1", Payload: map[string]any{"tag": "tag1"}}},
		{"TouchesCustomType custom_type", outbox.Op{Kind: outbox.KindUpdateTask, TaskGID: "t1", Payload: map[string]any{"custom_type": "ct1"}}},
		{"TouchesCustomType custom_type_status_option", outbox.Op{Kind: outbox.KindUpdateTask, TaskGID: "t1", Payload: map[string]any{"name": "x", "custom_type_status_option": "opt1"}}},
		{"TouchesCustomType resource_subtype custom", outbox.Op{Kind: outbox.KindUpdateTask, TaskGID: "t1", Payload: map[string]any{"resource_subtype": "custom"}}},
		{"set_custom_field missing field", outbox.Op{Kind: outbox.KindSetCustomField, TaskGID: "t1", Payload: map[string]any{"type": "text", "value": "hi"}}},
		{"set_custom_field waits for queued enum option", outbox.Op{Kind: outbox.KindSetCustomField, TaskGID: "t1", Payload: map[string]any{"field": "cf1", "type": "enum", "value": localOpt}}},
		{"set_custom_field waits for queued field setting", outbox.Op{Kind: outbox.KindSetCustomField, TaskGID: "t1", Payload: map[string]any{"field": "cf_pending", "type": "text", "value": "hi"}}},
		{"move task missing section", outbox.Op{Kind: outbox.KindMoveTask, TaskGID: "t1", Payload: map[string]any{"project": "p1"}}},
		{"move task local section", outbox.Op{Kind: outbox.KindMoveTask, TaskGID: "t1", Payload: map[string]any{"project": "p1", "section": "local:s1"}}},
		{"move task insert_before", outbox.Op{Kind: outbox.KindMoveTask, TaskGID: "t1", Payload: map[string]any{"project": "p1", "section": "s1", "insert_before": "t2"}}},
		{"move task insert_after", outbox.Op{Kind: outbox.KindMoveTask, TaskGID: "t1", Payload: map[string]any{"project": "p1", "section": "s1", "insert_after": "t2"}}},
		{"add tag missing tag", outbox.Op{Kind: outbox.KindAddTag, TaskGID: "t1", Payload: map[string]any{}}},
		{"add tag unresolved local tag", outbox.Op{Kind: outbox.KindAddTag, TaskGID: "t1", Payload: map[string]any{"tag": "local:unmapped"}}},
		{"remove tag unresolved local tag", outbox.Op{Kind: outbox.KindRemoveTag, TaskGID: "t1", Payload: map[string]any{"tag": "local:unmapped"}}},
		{"add follower missing user", outbox.Op{Kind: outbox.KindAddFollower, TaskGID: "t1", Payload: map[string]any{}}},
		{"add follower local user", outbox.Op{Kind: outbox.KindAddFollower, TaskGID: "t1", Payload: map[string]any{"user": "local:u1"}}},
		{"remove follower local user", outbox.Op{Kind: outbox.KindRemoveFollower, TaskGID: "t1", Payload: map[string]any{"user": "local:u1"}}},
		{"add project missing project", outbox.Op{Kind: outbox.KindAddProject, TaskGID: "t1", Payload: map[string]any{"section": "s1"}}},
		{"add project local project", outbox.Op{Kind: outbox.KindAddProject, TaskGID: "t1", Payload: map[string]any{"project": "local:p1"}}},
		{"add project local section", outbox.Op{Kind: outbox.KindAddProject, TaskGID: "t1", Payload: map[string]any{"project": "p1", "section": "local:s1"}}},
		{"remove project missing project", outbox.Op{Kind: outbox.KindRemoveProject, TaskGID: "t1", Payload: map[string]any{}}},
		{"remove project local project", outbox.Op{Kind: outbox.KindRemoveProject, TaskGID: "t1", Payload: map[string]any{"project": "local:p1"}}},
		{"add dependency missing dep", outbox.Op{Kind: outbox.KindAddDependency, TaskGID: "t1", Payload: map[string]any{}}},
		{"add dependency unresolved local dep", outbox.Op{Kind: outbox.KindAddDependency, TaskGID: "t1", Payload: map[string]any{"dependency": "local:unmapped"}}},
		{"remove dependency unresolved local dep", outbox.Op{Kind: outbox.KindRemoveDependency, TaskGID: "t1", Payload: map[string]any{"dependency": "local:unmapped"}}},
		{"create task excluded", outbox.Op{Kind: outbox.KindCreateTask, TaskGID: "t1"}},
		{"add comment excluded", outbox.Op{Kind: outbox.KindAddComment, TaskGID: "t1"}},
		{"delete task excluded", outbox.Op{Kind: outbox.KindDeleteTask, TaskGID: "t1"}},
		{"unknown kind excluded", outbox.Op{Kind: "unknown", TaskGID: "t1"}},
	}

	for _, tc := range excluded {
		t.Run(tc.name, func(t *testing.T) {
			_, ok, err := s.batchEntryFor(ctx, tc.op)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ok {
				t.Fatalf("expected ok=false for %+v", tc.op)
			}
		})
	}
}

func TestBatchAttackGroupBoundaries(t *testing.T) {
	ctx := context.Background()
	st := openAttackStore(t)
	now := time.Date(2026, 11, 20, 3, 0, 0, 0, time.UTC)

	// Queue 12 tasks' updates: batchGroup must cap at model.MaxBatchActions (10).
	for i := 1; i <= 12; i++ {
		gid := "t" + string(rune('a'+i-1))
		if err := st.UpsertTasks(ctx, []model.Task{{GID: gid, Name: gid}}); err != nil {
			t.Fatal(err)
		}
		if err := st.UpdateTaskLocal(ctx, gid, map[string]any{"completed": true}, now); err != nil {
			t.Fatal(err)
		}
	}
	ops, err := st.PendingOps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s := &Syncer{Store: st}
	group, err := s.batchGroup(ctx, ops)
	if err != nil {
		t.Fatal(err)
	}
	if len(group) != model.MaxBatchActions {
		t.Fatalf("batchGroup len = %d, want %d", len(group), model.MaxBatchActions)
	}

	// Duplicate task in ops stops the group before the duplicate.
	dupOps := []outbox.Op{ops[0], ops[1], ops[0]}
	dupGroup, err := s.batchGroup(ctx, dupOps)
	if err != nil {
		t.Fatal(err)
	}
	if len(dupGroup) != 2 {
		t.Fatalf("dup batchGroup len = %d, want 2", len(dupGroup))
	}
}
