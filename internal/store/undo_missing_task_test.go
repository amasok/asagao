package store

import (
	"context"
	"testing"
)

// An entry whose task is no longer in the cache (removed by a pull, say) can still be
// discarded: there is nothing left to put back, so the undo is skipped instead of failing
// on the task's foreign key (#87).
func TestDiscardEntryOfTaskNotInCache(t *testing.T) {
	for _, tc := range []struct {
		kind          Kind
		payload, base string
	}{
		{OpAddFollower, `{"user":"u1"}`, `{}`},
		{OpRemoveFollower, `{"user":"u1"}`, `{}`},
		{OpAddProject, `{"project":"p1","section":"s1"}`, `{}`},
		{OpRemoveProject, `{"project":"p1","section":"s1"}`, `{}`},
		{OpAddTag, `{"tag":"g1"}`, `{}`},
		{OpRemoveTag, `{"tag":"g1"}`, `{}`},
		{OpSetCustomField, `{"field":"f1","value":"new","display":"new"}`, `{"value":"old","display":"old"}`},
		{OpAddDependency, `{"dependency":"d1"}`, `{}`},
		{OpRemoveDependency, `{"dependency":"d1"}`, `{}`},
		{OpUpdateTask, `{"name":"new"}`, `{"name":"old"}`},
		{OpMoveTask, `{"project":"p1","section":"s2"}`, `{"section":"s1"}`},
		{OpSetParent, `{"parent":"t2"}`, `{"parent":""}`},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			s := newTestStore(t)
			ctx := context.Background()
			if _, err := s.db.ExecContext(ctx, `INSERT INTO outbox(kind, task_gid, payload, base, state, created_at)
				VALUES(?, 'gone', ?, ?, 'pending', '2026-01-01T00:00:00Z')`, tc.kind, tc.payload, tc.base); err != nil {
				t.Fatal(err)
			}
			ops, err := s.ListOps(ctx)
			if err != nil || len(ops) != 1 {
				t.Fatalf("ops %+v %v", ops, err)
			}
			if err := s.DiscardOp(ctx, ops[0].ID); err != nil {
				t.Fatalf("discard: %v", err)
			}
			if ops, err := s.ListOps(ctx); err != nil || len(ops) != 0 {
				t.Fatalf("left %+v %v", ops, err)
			}
			for _, q := range []string{
				"SELECT count(*) FROM tasks WHERE gid = 'gone'",
				"SELECT count(*) FROM task_followers WHERE task_gid = 'gone'",
				"SELECT count(*) FROM task_memberships WHERE task_gid = 'gone'",
				"SELECT count(*) FROM task_tags WHERE task_gid = 'gone'",
				"SELECT count(*) FROM task_custom_fields WHERE task_gid = 'gone'",
			} {
				var n int
				if err := s.db.QueryRowContext(ctx, q).Scan(&n); err != nil || n != 0 {
					t.Errorf("%s: %d rows %v", q, n, err)
				}
			}
		})
	}
}
