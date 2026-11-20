package sync

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/store"
)

func TestBatchEntryForIntentWrites(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s := &Syncer{Store: st}

	cases := []struct {
		name        string
		op          outbox.Op
		wantOK      bool
		wantWrite   model.BatchWrite
		wantCheck   bool
		wantRefresh bool
		wantProject string
		wantSection string
	}{
		{
			name: "update task",
			op: outbox.Op{
				Kind:    outbox.KindUpdateTask,
				TaskGID: "t1",
				Payload: map[string]any{"completed": true},
			},
			wantOK:    true,
			wantWrite: model.BatchUpdateTask("t1", map[string]any{"completed": true}),
			wantCheck: true,
		},
		{
			name: "set custom field",
			op: outbox.Op{
				Kind:    outbox.KindSetCustomField,
				TaskGID: "t1",
				Payload: map[string]any{"field": "cf1", "type": "text", "value": "hi"},
			},
			wantOK:    true,
			wantWrite: model.BatchUpdateTask("t1", map[string]any{"custom_fields": map[string]any{"cf1": "hi"}}),
			wantCheck: true,
		},
		{
			name: "move task",
			op: outbox.Op{
				Kind:    outbox.KindMoveTask,
				TaskGID: "t1",
				Payload: map[string]any{"project": "p1", "section": "s2"},
			},
			wantOK:      true,
			wantWrite:   model.BatchMoveTaskToSection("s2", "t1"),
			wantRefresh: true,
			wantProject: "p1",
			wantSection: "s2",
		},
		{
			name: "add tag",
			op: outbox.Op{
				Kind:    outbox.KindAddTag,
				TaskGID: "t1",
				Payload: map[string]any{"tag": "tag1"},
			},
			wantOK:    true,
			wantWrite: model.BatchAddTag("t1", "tag1"),
		},
		{
			name: "remove tag",
			op: outbox.Op{
				Kind:    outbox.KindRemoveTag,
				TaskGID: "t1",
				Payload: map[string]any{"tag": "tag1"},
			},
			wantOK:    true,
			wantWrite: model.BatchRemoveTag("t1", "tag1"),
		},
		{
			name: "add follower",
			op: outbox.Op{
				Kind:    outbox.KindAddFollower,
				TaskGID: "t1",
				Payload: map[string]any{"user": "u1"},
			},
			wantOK:    true,
			wantWrite: model.BatchAddFollower("t1", "u1"),
		},
		{
			name: "remove follower",
			op: outbox.Op{
				Kind:    outbox.KindRemoveFollower,
				TaskGID: "t1",
				Payload: map[string]any{"user": "u1"},
			},
			wantOK:    true,
			wantWrite: model.BatchRemoveFollower("t1", "u1"),
		},
		{
			name: "add project without section",
			op: outbox.Op{
				Kind:    outbox.KindAddProject,
				TaskGID: "t1",
				Payload: map[string]any{"project": "p1"},
			},
			wantOK:    true,
			wantWrite: model.BatchAddProject("t1", "p1", ""),
		},
		{
			name: "add project with section",
			op: outbox.Op{
				Kind:    outbox.KindAddProject,
				TaskGID: "t1",
				Payload: map[string]any{"project": "p1", "section": "s1"},
			},
			wantOK:    true,
			wantWrite: model.BatchAddProject("t1", "p1", "s1"),
		},
		{
			name: "remove project",
			op: outbox.Op{
				Kind:    outbox.KindRemoveProject,
				TaskGID: "t1",
				Payload: map[string]any{"project": "p1"},
			},
			wantOK:    true,
			wantWrite: model.BatchRemoveProject("t1", "p1"),
		},
		{
			name: "add dependency",
			op: outbox.Op{
				Kind:    outbox.KindAddDependency,
				TaskGID: "t1",
				Payload: map[string]any{"dependency": "d1"},
			},
			wantOK:    true,
			wantWrite: model.BatchAddDependency("t1", "d1"),
		},
		{
			name: "remove dependency",
			op: outbox.Op{
				Kind:    outbox.KindRemoveDependency,
				TaskGID: "t1",
				Payload: map[string]any{"dependency": "d1"},
			},
			wantOK:    true,
			wantWrite: model.BatchRemoveDependency("t1", "d1"),
		},
		{
			name: "local task gid excluded from batch",
			op: outbox.Op{
				Kind:    outbox.KindUpdateTask,
				TaskGID: "local:1",
				Payload: map[string]any{"completed": true},
			},
			wantOK: false,
		},
		{
			name: "positioned move excluded from batch",
			op: outbox.Op{
				Kind:    outbox.KindMoveTask,
				TaskGID: "t1",
				Payload: map[string]any{"project": "p1", "section": "s2", "insert_before": "t2"},
			},
			wantOK: false,
		},
		{
			name: "create task excluded from batch",
			op: outbox.Op{
				Kind:    outbox.KindCreateTask,
				TaskGID: "local:1",
			},
			wantOK: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, ok, err := s.Pusher().batchEntryFor(ctx, tc.op)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if !reflect.DeepEqual(e.write, tc.wantWrite) {
				t.Fatalf("write = %#v, want %#v", e.write, tc.wantWrite)
			}
			if (e.check != nil) != tc.wantCheck || e.refresh != tc.wantRefresh || e.project != tc.wantProject || e.section != tc.wantSection {
				t.Fatalf("entry flags/meta = %+v", e)
			}
		})
	}
}
