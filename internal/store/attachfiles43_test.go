package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	asana "github.com/amasok/asagao/internal/asana/model"
)

// #43: a reset deletes the recorded attachment files only inside the attachments folder.
func TestResetKeepsFilesOutsideTheAttachmentsFolder(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	cache, other := t.TempDir(), t.TempDir()
	s.SetAttachmentDir(cache)
	inside := filepath.Join(cache, "a1.png")
	outside := filepath.Join(other, "大事なファイル.txt")
	for _, f := range []string{inside, outside} {
		must(t, os.WriteFile(f, []byte("x"), 0o600))
	}
	must(t, s.ReplaceAttachments(ctx, "t1", []asana.Attachment{{GID: "a1", Name: "a"}, {GID: "a2", Name: "b"}}))
	must(t, s.SetAttachmentPath(ctx, "a1", inside))
	must(t, s.SetAttachmentPath(ctx, "a2", outside))
	if _, err := s.Reset(ctx, true, now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(inside); !os.IsNotExist(err) {
		t.Errorf("the cached file stays: %v", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("a file outside the attachments folder is gone: %v", err)
	}
}

// #43: once a delete of a task is sent, the local files of its and its subtasks'
// attachments go too: downloaded ones, the copy of an upload not sent, and the copy a
// queued attachment delete kept (only inside the attachments folder).
func TestCompleteDeleteRemovesAttachmentFiles(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	cache, other := t.TempDir(), t.TempDir()
	s.SetAttachmentDir(cache)
	write := func(p string) string {
		must(t, os.WriteFile(p, []byte("x"), 0o600))
		return p
	}
	downloaded := write(filepath.Join(cache, "a1.png"))
	subDownloaded := write(filepath.Join(cache, "a2.png"))
	outside := write(filepath.Join(other, "大事なファイル.txt"))
	must(t, s.ReplaceAttachments(ctx, "t1", []asana.Attachment{{GID: "a1", Name: "a"}, {GID: "a3", Name: "c"}}))
	must(t, s.ReplaceAttachments(ctx, "t1a", []asana.Attachment{{GID: "a2", Name: "b"}}))
	must(t, s.SetAttachmentPath(ctx, "a1", downloaded))
	must(t, s.SetAttachmentPath(ctx, "a2", subDownloaded))
	must(t, s.SetAttachmentPath(ctx, "a3", outside))
	// an upload not sent yet (its copy in the cache)
	src := write(filepath.Join(other, "原本.txt"))
	local, err := s.UploadAttachmentLocal(ctx, "t1", src, cache, now)
	must(t, err)
	up, err := s.GetAttachment(ctx, local)
	must(t, err)
	// an attachment delete whose copy waits for an upload being checked: simulated by a
	// queued delete_attachment that kept a path
	kept := write(filepath.Join(cache, "kept.txt"))
	_, err = s.db.Exec(`INSERT INTO outbox(kind, task_gid, payload, base, base_modified_at, state, conflict, created_at)
		VALUES('delete_attachment', 't1', json_object('attachment', 'local:x', 'path', ?), '{}', '', 'pending', '{}', '')`, kept)
	must(t, err)
	if _, err := s.DeleteTaskLocal(ctx, "t1", now); err != nil {
		t.Fatal(err)
	}
	var id int64
	must(t, s.db.QueryRow("SELECT id FROM outbox WHERE kind = 'delete_task'").Scan(&id))
	must(t, s.CompleteDelete(ctx, id, "t1"))
	for _, f := range []string{downloaded, subDownloaded, up.LocalPath, kept} {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Errorf("%s stays: %v", f, err)
		}
	}
	for _, f := range []string{outside, src} {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("%s is gone: %v", f, err)
		}
	}
}
