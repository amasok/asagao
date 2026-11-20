package store

import (
	"context"
	"testing"

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
		name string
		kind Kind
		f    undoFunc
	}{
		{"twice", OpUpdateTask, nothingToUndo},
		{"unknown kind", "made_up", nothingToUndo},
		{"nil", OpUpdateTask, nil},
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
			registerUndo(tc.kind, tc.f)
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
