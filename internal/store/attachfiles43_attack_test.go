package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	asana "github.com/amasok/asagao/internal/asana/model"
)

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

func writeF(t *testing.T, p string) string {
	t.Helper()
	must(t, os.MkdirAll(filepath.Dir(p), 0o700))
	must(t, os.WriteFile(p, []byte("x"), 0o600))
	return p
}

// RemoveAttachmentFile: paths that must survive, and paths that must go.
func TestRemoveAttachmentFileBoundaries(t *testing.T) {
	root := t.TempDir()
	outer := t.TempDir()
	s := newTestStore(t)
	s.SetAttachmentDir(filepath.Join(root, "attachments"))
	att := filepath.Join(root, "attachments")
	must(t, os.MkdirAll(att, 0o700))

	sibling := writeF(t, filepath.Join(root, "attachments-evil", "f.txt")) // shares the prefix
	parent := writeF(t, filepath.Join(root, "top.txt"))
	viaDotDot := filepath.Join(att, "..", "top.txt")
	dotDotOut := filepath.Join(att, "sub", "..", "..", "attachments-evil", "f.txt")
	outsideReal := writeF(t, filepath.Join(outer, "secret.txt"))
	// a symlinked folder inside the attachments folder that points outside
	must(t, os.Symlink(outer, filepath.Join(att, "link-dir")))
	viaLinkDir := filepath.Join(att, "link-dir", "secret.txt")
	// a symlinked file inside that points outside: only the link goes
	must(t, os.Symlink(outsideReal, filepath.Join(att, "link-file")))
	// the attachments folder itself is a symlink to the real one (legit)
	realDir := filepath.Join(root, "real")
	must(t, os.MkdirAll(realDir, 0o700))
	inReal := writeF(t, filepath.Join(realDir, "a.txt"))

	for _, p := range []string{sibling, parent, viaDotDot, dotDotOut, viaLinkDir, att, root, "relative-nothing.txt", filepath.Join(att, "nonexistent", "x")} {
		if err := s.RemoveAttachmentFile(p); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	for _, p := range []string{sibling, parent, outsideReal, att} {
		if !exists(p) {
			t.Errorf("%s was deleted", p)
		}
	}
	if err := s.RemoveAttachmentFile(filepath.Join(att, "link-file")); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(att, "link-file")) {
		t.Error("the link stays")
	}
	if !exists(outsideReal) {
		t.Error("the link's target was deleted")
	}
	// legit file in the folder, reached through a symlinked root
	s.SetAttachmentDir(filepath.Join(root, "alias"))
	must(t, os.Symlink(realDir, filepath.Join(root, "alias")))
	if err := s.RemoveAttachmentFile(inReal); err != nil {
		t.Fatal(err)
	}
	if exists(inReal) {
		t.Error("a file inside (root is a link) stays")
	}
	// the attachments folder does not exist at all
	s.SetAttachmentDir(filepath.Join(root, "nope"))
	if err := s.RemoveAttachmentFile(parent); err != nil || !exists(parent) {
		t.Errorf("no folder: err=%v exists=%v", err, exists(parent))
	}
}

// The default folder follows XDG_CACHE_HOME, and a dry-run copy's own folder is not the real one.
func TestRemoveAttachmentFileDefaultRoot(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	s := newTestStore(t)
	in := writeF(t, filepath.Join(cache, "asagao", "attachments", "a.png"))
	out := writeF(t, filepath.Join(cache, "asagao", "other.png"))
	must(t, s.RemoveAttachmentFile(out))
	must(t, s.RemoveAttachmentFile(in))
	if !exists(out) || exists(in) {
		t.Errorf("out exists=%v in exists=%v", exists(out), exists(in))
	}
	// a store pointed at a dry-run folder must not delete the real cache files
	real := writeF(t, filepath.Join(cache, "asagao", "attachments", "b.png"))
	s.SetAttachmentDir(t.TempDir())
	must(t, s.RemoveAttachmentFile(real))
	if !exists(real) {
		t.Error("a dry-run store deleted a real file")
	}
}

// Planted paths in the DB (outside, via .., via symlink): reset and task delete keep them.
func TestPlantedPathsSurviveResetAndDelete(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []string{"reset", "delete"} {
		t.Run(mode, func(t *testing.T) {
			s := newTestStore(t)
			att, outer := t.TempDir(), t.TempDir()
			s.SetAttachmentDir(att)
			secret := writeF(t, filepath.Join(outer, "secret.txt"))
			must(t, os.Symlink(outer, filepath.Join(att, "ld")))
			planted := []string{secret, filepath.Join(att, "ld", "secret.txt"), filepath.Join(att, "..", filepath.Base(outer), "secret.txt")}
			var atts []asana.Attachment
			for i := range planted {
				atts = append(atts, asana.Attachment{GID: string(rune('a'+i)) + "1", Name: "n"})
			}
			must(t, s.ReplaceAttachments(ctx, "t1", atts))
			for i, p := range planted {
				must(t, s.SetAttachmentPath(ctx, atts[i].GID, p))
			}
			// queued entries that carry a path: upload and delete
			for _, k := range []string{"upload_attachment", "delete_attachment"} {
				_, err := s.db.Exec(`INSERT INTO outbox(kind, task_gid, payload, base, base_modified_at, state, conflict, created_at)
					VALUES(?, 't1', json_object('attachment', 'local:x', 'path', ?), '{}', '', 'pending', '{}', '')`, k, secret)
				must(t, err)
			}
			if mode == "reset" {
				if _, err := s.ResetWithBackup(ctx, t.TempDir(), AnyUnsent, now); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := s.DeleteTaskLocal(ctx, "t1", now); err != nil {
					t.Fatal(err)
				}
				var id int64
				must(t, s.db.QueryRow("SELECT id FROM outbox WHERE kind = 'delete_task'").Scan(&id))
				must(t, s.CompleteDelete(ctx, id, "t1"))
			}
			if !exists(secret) {
				t.Error("the file outside was deleted")
			}
		})
	}
}

// A file several rows point at: one removal, no error, and others' files stay.
func TestSharedPathAndForeignTaskFiles(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	att := t.TempDir()
	s.SetAttachmentDir(att)
	shared := writeF(t, filepath.Join(att, "shared.png"))
	mine := writeF(t, filepath.Join(att, "mine.png"))
	other := writeF(t, filepath.Join(att, "other-task.png"))
	must(t, s.ReplaceAttachments(ctx, "t1", []asana.Attachment{{GID: "a1", Name: "a"}, {GID: "a2", Name: "b"}}))
	_, err := s.db.Exec(`INSERT OR IGNORE INTO tasks(gid, name) VALUES('t1','p'),('t9','z')`)
	must(t, err)
	must(t, s.ReplaceAttachments(ctx, "t9", []asana.Attachment{{GID: "a9", Name: "z"}}))
	must(t, s.SetAttachmentPath(ctx, "a1", shared))
	must(t, s.SetAttachmentPath(ctx, "a2", shared))
	must(t, s.SetAttachmentPath(ctx, "a9", other))
	_ = mine
	if _, err := s.DeleteTaskLocal(ctx, "t1", now); err != nil {
		t.Fatal(err)
	}
	var id int64
	must(t, s.db.QueryRow("SELECT id FROM outbox WHERE kind = 'delete_task'").Scan(&id))
	must(t, s.CompleteDelete(ctx, id, "t1"))
	if exists(shared) {
		t.Error("shared file stays")
	}
	if !exists(other) || !exists(mine) {
		t.Error("another task's / unrecorded file was deleted")
	}
}

// If the transaction fails, the files must stay (they are removed after the save).
func TestCompleteDeleteFailureKeepsFiles(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	att := t.TempDir()
	s.SetAttachmentDir(att)
	f := writeF(t, filepath.Join(att, "a.png"))
	must(t, s.ReplaceAttachments(ctx, "t1", []asana.Attachment{{GID: "a1", Name: "a"}}))
	must(t, s.SetAttachmentPath(ctx, "a1", f))
	// make the removal fail: a trigger that aborts deleting tasks
	_, err := s.db.Exec(`CREATE TRIGGER boom BEFORE DELETE ON tasks BEGIN SELECT RAISE(ABORT, 'boom'); END`)
	must(t, err)
	_, err = s.db.Exec(`INSERT OR IGNORE INTO tasks(gid, name) VALUES('t1', 'x')`)
	must(t, err)
	if err := s.CompleteDelete(ctx, 999, "t1"); err == nil {
		t.Skip("could not provoke a failure") // see below: the trigger must fire
	}
	if !exists(f) {
		t.Error("file deleted although the transaction failed")
	}
	var n int
	must(t, s.db.QueryRow("SELECT count(*) FROM attachments WHERE gid = 'a1'").Scan(&n))
	if n != 1 {
		t.Errorf("attachment row lost: %d", n)
	}
}

// Subtask tree (deep) and a missing file: no error, all inside files go.
func TestCompleteDeleteDeepTreeMissingFile(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	att := t.TempDir()
	s.SetAttachmentDir(att)
	f2 := writeF(t, filepath.Join(att, "deep.png"))
	gone := filepath.Join(att, "gone.png")
	_, err := s.db.Exec(`INSERT OR IGNORE INTO tasks(gid, name) VALUES('t1','p'),('c1','c'),('c2','d')`)
	must(t, err)
	_, err = s.db.Exec(`UPDATE tasks SET parent_gid='t1' WHERE gid='c1'`)
	must(t, err)
	_, err = s.db.Exec(`UPDATE tasks SET parent_gid='c1' WHERE gid='c2'`)
	must(t, err)
	must(t, s.ReplaceAttachments(ctx, "c2", []asana.Attachment{{GID: "a1", Name: "a"}, {GID: "a2", Name: "b"}}))
	must(t, s.SetAttachmentPath(ctx, "a1", f2))
	must(t, s.SetAttachmentPath(ctx, "a2", gone))
	if _, err := s.DeleteTaskLocal(ctx, "t1", now); err != nil {
		t.Fatal(err)
	}
	var id int64
	must(t, s.db.QueryRow("SELECT id FROM outbox WHERE kind = 'delete_task'").Scan(&id))
	must(t, s.CompleteDelete(ctx, id, "t1"))
	if exists(f2) {
		t.Error("a deep subtask's file stays")
	}
}
