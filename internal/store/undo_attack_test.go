package store

import (
	"context"
	"testing"

	"github.com/amasok/asagao/internal/domain/outbox"
)

// A store.db written before #70 holds the kinds as plain strings and payloads as JSON.
// Every kind of such a row can be read, and discarded (with empty, partial or wrongly
// typed payload and base) without an error and without leaving the entry behind.
func TestAttackDiscardLegacyRowsOfEveryKind(t *testing.T) {
	ctx := context.Background()
	jsons := [][2]string{
		{`{}`, `{}`},
		{`{"story_gid":"c1","status":"x","attachment":"a1","tag":"g1","project":"p1","section":"s1","user":"u","dependency":"t2","parent":"t2"}`,
			`{"section":"s1","name":"old","read":true,"parent":"t2","completed":false,"value":null}`},
		{`{"story_gid":5,"tag":7,"project":[1],"section":null}`, `{"section":3,"name":9,"read":"yes","parent":false}`},
		{`null`, `null`},
	}
	for _, k := range outbox.AllKinds() {
		for i, pj := range jsons {
			s := newTestStore(t)
			for _, gid := range []string{"t1", "t2", "t1a"} {
				if _, err := s.db.ExecContext(ctx, `INSERT INTO outbox(kind, task_gid, payload, base, base_modified_at, state, conflict, created_at)
					VALUES(?, ?, ?, ?, '', 'pending', '{}', '2026-01-01T00:00:00Z')`, string(k), gid, pj[0], pj[1]); err != nil {
					t.Fatal(err)
				}
			}
			ops, err := s.ListOps(ctx)
			if err != nil || len(ops) != 3 {
				t.Fatalf("%s #%d: %d ops, %v", k, i, len(ops), err)
			}
			for _, op := range ops {
				if op.Kind != k || !op.Kind.Known() {
					t.Fatalf("read back kind %q", op.Kind)
				}
				if err := s.DiscardOp(ctx, op.ID); err != nil {
					t.Errorf("%s #%d gid %q: discard: %v", k, i, op.TaskGID, err)
				}
			}
			if ops, err := s.ListOps(ctx); err != nil || len(ops) != 0 {
				t.Errorf("%s #%d: %d entries left, %v", k, i, len(ops), err)
			}
		}
	}
}

// Kinds from a newer version (including ones that look like old ones) are listed and
// dropped without touching the cache.
func TestAttackUnknownKindsLeaveTheCacheAlone(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	before, err := s.GetTask(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"from_the_future", "", "UPDATE_TASK", "update_task ", "update-task"} {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO outbox(kind, task_gid, payload, base, state, created_at)
			VALUES(?, 't1', '{"name":"zzz"}', '{"name":"base"}', 'pending', '2026-01-01T00:00:00Z')`, k); err != nil {
			t.Fatal(err)
		}
	}
	ops, err := s.ListOps(ctx)
	if err != nil || len(ops) != 5 {
		t.Fatalf("%d ops %v", len(ops), err)
	}
	for _, op := range ops {
		if op.Kind.Known() {
			t.Errorf("%q is known", op.Kind)
		}
		if err := s.DiscardOp(ctx, op.ID); err != nil {
			t.Errorf("discard %q: %v", op.Kind, err)
		}
	}
	after, err := s.GetTask(ctx, "t1")
	if err != nil || after.Name != before.Name {
		t.Errorf("task changed: %q -> %q (%v)", before.Name, after.Name, err)
	}
}

// Discarding an update_task puts the base back; a kind used as a SQL argument matches
// rows written as plain text.
func TestAttackKindArgumentsMatchStoredText(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO outbox(kind, task_gid, payload, base, state, created_at)
		VALUES('create_task', 'local:n', '{"name":"n"}', '{}', 'pending', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.CreateQueued(ctx, "local:n"); err != nil || !ok {
		t.Errorf("CreateQueued = %v, %v", ok, err)
	}
	if ok, err := s.CreateQueued(ctx, "local:other"); err != nil || ok {
		t.Errorf("CreateQueued(other) = %v, %v", ok, err)
	}
}
