package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	asana "github.com/amasok/asagao/internal/asana/model"
)

// A recorded path outside the attachments folder is not deleted, also when a symbolic
// link makes it look inside; inside, the file goes (a link only as a link).
func TestRemoveAttachmentFileStaysInsideTheFolder(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	cache, other := t.TempDir(), t.TempDir()
	s.SetAttachmentDir(cache)
	mine := filepath.Join(other, "大事なファイル.txt")
	if err := os.WriteFile(mine, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// a folder inside the cache that is a link to the user's folder
	if err := os.Symlink(other, filepath.Join(cache, "link")); err != nil {
		t.Fatal(err)
	}
	// a file inside the cache that is a link to the user's file
	if err := os.Symlink(mine, filepath.Join(cache, "file-link")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{mine, filepath.Join(cache, "..", filepath.Base(other), "大事なファイル.txt"), filepath.Join(cache, "link", "大事なファイル.txt")} {
		if err := s.RemoveAttachmentFile(p); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	if err := s.RemoveAttachmentFile(filepath.Join(cache, "file-link")); err != nil {
		t.Errorf("link inside: %v", err)
	}
	if _, err := os.Stat(mine); err != nil {
		t.Fatalf("the user's file is gone: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(cache, "file-link")); !os.IsNotExist(err) {
		t.Errorf("the link inside stays: %v", err)
	}
	if err := s.RemoveAttachmentFile(filepath.Join(cache, "gone.txt")); err != nil {
		t.Errorf("a missing file: %v", err)
	}

	// through a delete: the row goes, the file outside stays
	if err := s.ReplaceAttachments(ctx, "t1", []asana.Attachment{{GID: "a1", Name: "x"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAttachmentPath(ctx, "a1", mine); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAttachmentLocal(ctx, "a1", now); err != nil {
		t.Errorf("delete: %v", err)
	}
	if _, err := os.Stat(mine); err != nil {
		t.Fatalf("the user's file is gone: %v", err)
	}
}
