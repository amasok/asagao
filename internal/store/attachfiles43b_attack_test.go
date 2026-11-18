package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	asana "github.com/amasok/asagao/internal/asana/model"
)

// DiscardDeletedTask: an upload's copy and a delete_attachment's kept path point at the
// same file. The upload's copy must stay (it is shown for recovery).
func TestAttackDiscardDeletedTaskSameFileUploadAndDelete(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	cache, other := t.TempDir(), t.TempDir()
	s.SetAttachmentDir(cache)
	src := writeF(t, filepath.Join(other, "原本.txt"))
	local, err := s.UploadAttachmentLocal(ctx, "t1", src, cache, now)
	must(t, err)
	up, err := s.GetAttachment(ctx, local)
	must(t, err)
	_, err = s.db.Exec(`INSERT INTO outbox(kind, task_gid, payload, base, base_modified_at, state, conflict, created_at)
		VALUES('delete_attachment', 't1', json_object('attachment', 'local:x', 'path', ?), '{}', '', 'pending', '{}', '')`, up.LocalPath)
	must(t, err)
	if _, err := s.DiscardDeletedTask(ctx, "t1", "gone", now); err != nil {
		t.Fatal(err)
	}
	if !exists(up.LocalPath) {
		t.Error("the upload's copy was deleted through the delete_attachment's path")
	}
}

// Planted paths in a subtask's attachments and queued ops survive DiscardDeletedTask.
func TestAttackDiscardDeletedTaskKeepsOutside(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	att, outer := t.TempDir(), t.TempDir()
	s.SetAttachmentDir(att)
	secret := writeF(t, filepath.Join(outer, "secret.txt"))
	must(t, os.Symlink(outer, filepath.Join(att, "ld")))
	_, err := s.db.Exec(`INSERT OR IGNORE INTO tasks(gid, name, parent_gid) VALUES('t1a','sub','t1')`)
	must(t, err)
	must(t, s.ReplaceAttachments(ctx, "t1a", []asana.Attachment{{GID: "a1", Name: "n"}}))
	must(t, s.SetAttachmentPath(ctx, "a1", filepath.Join(att, "ld", "secret.txt")))
	_, err = s.db.Exec(`INSERT INTO outbox(kind, task_gid, payload, base, base_modified_at, state, conflict, created_at)
		VALUES('delete_attachment', 't1a', json_object('attachment', 'x', 'path', ?), '{}', '', 'pending', '{}', '')`, secret)
	must(t, err)
	if _, err := s.DiscardDeletedTask(ctx, "t1", "gone", now); err != nil {
		t.Fatal(err)
	}
	if !exists(secret) {
		t.Error("a file outside the attachments folder was deleted")
	}
}

// A section holding a task whose delete is queued can be deleted; undoing the task's
// delete then must not leave a task invisible or panic; the order cannot be reversed.
func TestAttackSectionDeleteOrdering(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	// reverse order: the task is still there, so the section is refused and nothing is queued
	if err := s.DeleteSectionLocal(ctx, "s1", now); !errors.Is(err, ErrSectionNotEmpty) {
		t.Fatalf("section with a live task: %v", err)
	}
	if ops, _ := s.ListOps(ctx); len(ops) != 0 {
		t.Fatalf("outbox %+v", ops)
	}
	if _, err := s.DeleteTaskLocal(ctx, "t1", now); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSectionLocal(ctx, "s1", now); err != nil {
		t.Fatalf("section with only a deleted task: %v", err)
	}
	// undo the task's delete: the entry goes, the section's delete stays
	ops, _ := s.ListOps(ctx)
	if len(ops) != 2 || ops[0].Kind != OpDeleteTask {
		t.Fatalf("outbox %+v", ops)
	}
	if err := s.DiscardOp(ctx, ops[0].ID); err != nil {
		t.Fatal(err)
	}
	ops, _ = s.ListOps(ctx)
	if len(ops) != 1 || ops[0].Kind != OpDeleteSection {
		t.Fatalf("after undo: %+v", ops)
	}
}

// A section created offline, its task deleted: forgetting the section must not strand the delete.
func TestAttackLocalSectionWithDeletedTask(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	g, err := s.CreateSectionLocal(ctx, "p1", "新", "", "", now)
	if err != nil {
		t.Skipf("cannot create a section here: %v", err)
	}
	_, err = s.db.Exec("UPDATE task_memberships SET section_gid = ? WHERE task_gid = 't1' AND project_gid = 'p1'", g)
	must(t, err)
	if _, err := s.DeleteTaskLocal(ctx, "t1", now); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSectionLocal(ctx, g, now); err != nil {
		t.Fatalf("delete local section: %v", err)
	}
}
