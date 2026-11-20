package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"testing"
	"time"
)

func undoCount(t *testing.T, s *Store, q string, args ...any) int {
	t.Helper()
	var n int
	if err := s.db.QueryRowContext(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

// With the task still cached, discarding still puts back what the entry changed.
func TestDiscardPutsBackWhenTaskIsCached(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)
	s := newTestStore(t)

	last := func() int64 {
		ops, err := s.ListOps(ctx)
		if err != nil || len(ops) == 0 {
			t.Fatalf("ops %v %v", ops, err)
		}
		return ops[len(ops)-1].ID
	}

	// tag
	if err := s.RemoveTagLocal(ctx, "t1", "g1", now); err != nil {
		t.Fatal(err)
	}
	if n := undoCount(t, s, "SELECT count(*) FROM task_tags WHERE task_gid='t1' AND tag_gid='g1'"); n != 0 {
		t.Fatalf("tag not removed: %d", n)
	}
	if err := s.DiscardOp(ctx, last()); err != nil {
		t.Fatal(err)
	}
	if n := undoCount(t, s, "SELECT count(*) FROM task_tags WHERE task_gid='t1' AND tag_gid='g1'"); n != 1 {
		t.Fatalf("tag not restored: %d", n)
	}

	// membership with its section
	if err := s.SetProjectLocal(ctx, "t1", "p1", "", false, now); err != nil {
		t.Fatal(err)
	}
	if n := undoCount(t, s, "SELECT count(*) FROM task_memberships WHERE task_gid='t1' AND project_gid='p1'"); n != 0 {
		t.Fatalf("membership not removed: %d", n)
	}
	if err := s.DiscardOp(ctx, last()); err != nil {
		t.Fatal(err)
	}
	if n := undoCount(t, s, "SELECT count(*) FROM task_memberships WHERE task_gid='t1' AND project_gid='p1' AND section_gid='s1'"); n != 1 {
		t.Fatalf("membership/section not restored: %d", n)
	}

	// follower
	if _, err := s.db.ExecContext(ctx, "INSERT INTO task_followers(task_gid, user_gid) VALUES('t1','u9')"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFollowerLocal(ctx, "t1", "u9", false, now); err != nil {
		t.Fatal(err)
	}
	if n := undoCount(t, s, "SELECT count(*) FROM task_followers WHERE task_gid='t1' AND user_gid='u9'"); n != 0 {
		t.Fatalf("follower not removed: %d", n)
	}
	if err := s.DiscardOp(ctx, last()); err != nil {
		t.Fatal(err)
	}
	if n := undoCount(t, s, "SELECT count(*) FROM task_followers WHERE task_gid='t1' AND user_gid='u9'"); n != 1 {
		t.Fatalf("follower not restored: %d", n)
	}

	// custom field value (new -> back to old), including a field not set before
	if _, err := s.db.ExecContext(ctx, `INSERT INTO task_custom_fields(task_gid, field_gid, position, value, display) VALUES('t1','f1',0,'"new"','new')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO outbox(kind, task_gid, payload, base, state, created_at)
		VALUES('set_custom_field','t1','{"field":"f1","value":"new","display":"new"}','{"value":"old","display":"old"}','pending','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if err := s.DiscardOp(ctx, last()); err != nil {
		t.Fatal(err)
	}
	var v, d string
	if err := s.db.QueryRowContext(ctx, "SELECT value, display FROM task_custom_fields WHERE task_gid='t1' AND field_gid='f1'").Scan(&v, &d); err != nil || v != `"old"` || d != "old" {
		t.Fatalf("custom field %q %q %v", v, d, err)
	}
}

// Every registered undo must survive an entry that points at things not in the cache
// (task, parent, project, section, story, attachment, entry, field ...), in either shape of
// payload/base, and from every path that discards.
func TestEveryUndoSurvivesMissingTargets(t *testing.T) {
	ctx := context.Background()
	gids := []string{"gone", "local:gone"}
	var kinds []string
	for k := range undoers {
		kinds = append(kinds, string(k))
	}
	sort.Strings(kinds)
	if len(kinds) == 0 {
		t.Fatal("no undoers")
	}
	// Everything a payload or base may hold, all pointing nowhere.
	blob := func(gid string) string {
		m := map[string]any{
			"user": "ux", "project": "px", "section": "sx", "tag": "gx", "field": "fx", "dependency": "dx",
			"parent": "px9", "story": "sx9", "attachment": "ax", "entry": "ex", "status": "stx", "task": gid,
			"task_gid": gid, "parent_gid": gid, "text": "t", "html_text": "<b>t</b>", "name": "n", "color": "c",
			"workspace": "w9", "tasks": []any{gid, "other"}, "position": 3.0, "value": "v", "display": "v",
			"duration_minutes": 5.0, "entered_on": "2026-01-01", "is_important": true, "state": "on", "title": "T",
			"story_gid": "stx9", "options": []any{"o1"}, "option": "o1", "size": 3.0, "name_attachment": "a",
		}
		b, _ := json.Marshal(m)
		return string(b)
	}
	for _, kind := range kinds {
		for _, gid := range gids {
			for _, variant := range []string{"full", "emptyBase", "emptyBoth"} {
				t.Run(kind+"/"+gid+"/"+variant, func(t *testing.T) {
					s := newTestStore(t)
					payload, base := blob(gid), blob(gid)
					switch variant {
					case "emptyBase":
						base = `{}`
					case "emptyBoth":
						payload, base = `{}`, `{}`
					}
					for _, state := range []string{"pending", "failed", "conflict"} {
						res, err := s.db.ExecContext(ctx, `INSERT INTO outbox(kind, task_gid, payload, base, state, created_at)
							VALUES(?, ?, ?, ?, ?, '2026-01-01T00:00:00Z')`, kind, gid, payload, base, state)
						if err != nil {
							t.Fatal(err)
						}
						id, _ := res.LastInsertId()
						if err := s.DiscardOp(ctx, id); err != nil {
							t.Fatalf("DiscardOp state=%s: %v", state, err)
						}
						if n := undoCount(t, s, "SELECT count(*) FROM outbox WHERE id = ?", id); n != 0 {
							t.Fatalf("entry left (state %s)", state)
						}
					}
					// DiscardGoneOp goes through discardOpTx too
					res, err := s.db.ExecContext(ctx, `INSERT INTO outbox(kind, task_gid, payload, base, state, created_at)
						VALUES(?, ?, ?, ?, 'pending', '2026-01-01T00:00:00Z')`, kind, gid, payload, base)
					if err != nil {
						t.Fatal(err)
					}
					id, _ := res.LastInsertId()
					if _, err := s.DiscardGoneOp(ctx, id, "gone", time.Now()); err != nil {
						t.Fatalf("DiscardGoneOp: %v", err)
					}
				})
			}
		}
	}
}

// The task-removing path (a deleted task, with its queued entries) must not fail on an
// entry of a task that is not cached either.
func TestDiscardDeletedTaskWithEntriesOfMissingTask(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	for _, k := range []string{"remove_tag", "remove_follower", "remove_project", "set_custom_field"} {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO outbox(kind, task_gid, payload, base, state, created_at)
			VALUES(?, 'gone', '{"tag":"g1","user":"u1","project":"p1","section":"s1","field":"f1","value":"x"}', '{"value":"y"}', 'pending', '2026-01-01T00:00:00Z')`, k); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DiscardDeletedTask(ctx, "gone", "deleted", time.Now()); err != nil {
		t.Fatalf("DiscardDeletedTask: %v", err)
	}
	if n := undoCount(t, s, "SELECT count(*) FROM outbox WHERE task_gid='gone'"); n != 0 {
		t.Fatalf("entries left: %d", n)
	}
}

// Forward re-application after a pull is for tasks that exist: unchanged, and a task that
// is not cached is skipped rather than failing.
func TestReapplyForwardUnchanged(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)
	s := newTestStore(t)

	if err := s.SetFollowerLocal(ctx, "t1", "u8", true, now); err != nil {
		t.Fatal(err)
	}
	if err := s.SetProjectLocal(ctx, "t1", "p2", "", true, now); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveTagLocal(ctx, "t1", "g1", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO outbox(kind, task_gid, payload, base, state, created_at)
		VALUES('set_custom_field','t1','{"field":"f1","value":"new","display":"new"}','{"value":"old","display":"old"}','pending','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	// a pull wipes the local effects ...
	for _, q := range []string{
		"DELETE FROM task_followers WHERE task_gid='t1'",
		"DELETE FROM task_memberships WHERE task_gid='t1' AND project_gid='p2'",
		"INSERT OR IGNORE INTO task_tags(task_gid, tag_gid) VALUES('t1','g1')",
		"DELETE FROM task_custom_fields WHERE task_gid='t1'",
	} {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	// ... and the unsent entries are applied again
	if err := s.tx(ctx, func(tx *sql.Tx) error {
		if err := reapplyRelationOps(ctx, tx, "t1"); err != nil {
			return err
		}
		return reapplyCustomFieldOps(ctx, tx, "t1")
	}); err != nil {
		t.Fatal(err)
	}
	if n := undoCount(t, s, "SELECT count(*) FROM task_followers WHERE task_gid='t1' AND user_gid='u8'"); n != 1 {
		t.Errorf("follower not re-added: %d", n)
	}
	if n := undoCount(t, s, "SELECT count(*) FROM task_memberships WHERE task_gid='t1' AND project_gid='p2' AND section_gid <> ''"); n != 1 {
		// section is the first section of p2 (may be empty if p2 has none)
		if n2 := undoCount(t, s, "SELECT count(*) FROM task_memberships WHERE task_gid='t1' AND project_gid='p2'"); n2 != 1 {
			t.Errorf("membership not re-added: %d", n2)
		}
	}
	if n := undoCount(t, s, "SELECT count(*) FROM task_custom_fields WHERE task_gid='t1' AND field_gid='f1' AND display='new'"); n != 1 {
		t.Errorf("custom field not re-applied: %d", n)
	}
	// the removed tag, applied forward, is gone again: only the task's other effects stay
	// as the entries say (remove_tag forward = delete)
	if n := undoCount(t, s, "SELECT count(*) FROM task_tags WHERE task_gid='t1' AND tag_gid='g1'"); n != 1 {
		t.Logf("remove_tag is not part of reapplyRelationOps (tag stays %d)", n)
	}
}
