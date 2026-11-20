package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/domain/outbox"
)

// Discarding an entry of any kind undoes its local change: every kind says how, and
// nothing else is registered.
func TestEveryKindHasAnUndo(t *testing.T) {
	kinds := outbox.AllKinds()
	for _, k := range kinds {
		if f, ok := undoers[k]; !ok || f == nil {
			t.Errorf("no undo for %s", k)
		}
	}
	if len(undoers) != len(kinds) {
		t.Errorf("%d undo functions for %d kinds", len(undoers), len(kinds))
	}
}

func TestRegisterUndoRefusesMistakes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		register func()
	}{
		{"twice", func() { registerUndo(OpUpdateTask, nothingToUndo[outbox.UpdateTask]) }},
		{"unknown kind", func() { registerUndo("made_up", nothingToUndo[outbox.UpdateTask]) }},
		{"nil", func() { registerUndo[outbox.UpdateTask](OpUpdateTask, nil) }},
		{"another kind's payload", func() { registerUndo(OpAddTag, nothingToUndo[outbox.RemoveTag]) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := len(undoers)
			defer func() {
				if recover() == nil {
					t.Error("registered")
				}
				if len(undoers) != before {
					t.Errorf("undoers changed: %d → %d", before, len(undoers))
				}
			}()
			tc.register()
		})
	}
}

// An entry of a kind this version does not know (written by a newer one) is dropped
// without touching anything else.
func TestDiscardUnknownKind(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO outbox(kind, task_gid, payload, state, created_at)
		VALUES('from_the_future', 't1', '{}', 'pending', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	ops, err := s.ListOps(ctx)
	if err != nil || len(ops) != 1 || ops[0].Kind != "from_the_future" || ops[0].Kind.Known() {
		t.Fatalf("ops %+v %v", ops, err)
	}
	if err := s.DiscardOp(ctx, ops[0].ID); err != nil {
		t.Fatal(err)
	}
	if ops, err := s.ListOps(ctx); err != nil || len(ops) != 0 {
		t.Fatalf("left %+v %v", ops, err)
	}
}

// An entry this version cannot send (its payload has a key or a type of a newer version)
// is still undone as far as its payload reads when it is discarded.
func TestDiscardUnreadablePayloadUndoes(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	if err := s.UpsertProjects(ctx, "w1", []model.Project{{GID: "p1", Name: "P"}}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertTasks(ctx, []model.Task{{GID: "t1", Name: "A", Workspace: &model.Ref{GID: "w1"}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertTags(ctx, []model.Tag{{GID: "g1", Name: "タグ", Workspace: &model.Ref{GID: "w1"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddTagLocal(ctx, "t1", "g1", "", now); err != nil {
		t.Fatal(err)
	}
	local, err := s.CreateTaskLocal(ctx, NewTask{WorkspaceGID: "w1", ProjectGID: "p1", Name: "新しい"}, now)
	if err != nil {
		t.Fatal(err)
	}
	// as a newer version would have written them
	if _, err := s.db.ExecContext(ctx, `UPDATE outbox SET payload = json_set(payload, '$.due_at', '2026-11-20T10:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	ops, err := s.ListOps(ctx)
	if err != nil || len(ops) != 2 {
		t.Fatalf("ops %+v %v", ops, err)
	}
	for _, op := range ops {
		if outbox.Readable(op) == nil {
			t.Fatalf("%s still reads: %v", op.Kind, op.Payload)
		}
		if err := s.DiscardOp(ctx, op.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.GetTask(ctx, local); !errors.Is(err, ErrNotFound) {
		t.Errorf("the local task of a discarded create is still there: %v", err)
	}
	if d, err := s.GetTask(ctx, "t1"); err != nil || len(d.Tags) != 0 {
		t.Errorf("the tag of a discarded add_tag is still on the task: %+v, %v", d, err)
	}
}
