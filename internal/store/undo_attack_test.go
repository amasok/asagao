package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/domain/outbox"
)

// Discarding an entry whose payload does not read (every value of the wrong type, a key of
// a newer version, nothing at all) neither panics nor fails, whatever the kind: the entry
// goes, with the local change it can be undone by.
func TestAttackDiscardUnreadableEveryKind(t *testing.T) {
	ctx := context.Background()
	payloads := []string{
		`{}`,
		`{"future":1}`,
		`{"name":5,"project":["x"],"section":{"a":1},"tasks":"t1","tag":true,"user":null,"position":"2","read":"yes","existing":[1,2],"custom_fields":[],"value":{"a":1},"duration_minutes":1.5,"entry":7,"story":[],"attachment":{},"field":0,"workspace":false,"completed":"x","due_on":1}`,
	}
	for _, kind := range append(outbox.AllKinds(), "from_the_future") {
		for i, p := range payloads {
			s := newTestStore(t)
			res, err := s.db.ExecContext(ctx, `INSERT INTO outbox(kind, task_gid, payload, state, created_at) VALUES(?, 't1', ?, 'pending', '2026-11-01T00:00:00Z')`, string(kind), p)
			if err != nil {
				t.Fatal(err)
			}
			id, _ := res.LastInsertId()
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Errorf("%s payload %d: panic %v", kind, i, r)
					}
				}()
				if err := s.DiscardOp(ctx, id); err != nil {
					t.Errorf("%s payload %d: %v", kind, i, err)
				}
			}()
			if _, err := s.GetOp(ctx, id); err == nil {
				t.Errorf("%s payload %d: entry still queued", kind, i)
			}
		}
	}
}

// A key of a newer version in an update_task changes nothing about how the edit is undone.
func TestAttackDiscardUpdateWithFutureKeyUndoesTheRest(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	before, err := s.GetTask(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "変えた", "notes": "メモ"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	ops, _ := s.PendingOps(ctx)
	if len(ops) != 1 {
		t.Fatalf("%d ops", len(ops))
	}
	ops[0].Payload["from_the_future"] = []any{1}
	b, _ := json.Marshal(ops[0].Payload)
	if _, err := s.db.ExecContext(ctx, "UPDATE outbox SET payload = ? WHERE id = ?", string(b), ops[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DiscardOp(ctx, ops[0].ID); err != nil {
		t.Fatal(err)
	}
	after, err := s.GetTask(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if after.Name != before.Name || after.Notes != before.Notes {
		t.Errorf("after discard: %q %q, want %q %q", after.Name, after.Notes, before.Name, before.Notes)
	}
}
