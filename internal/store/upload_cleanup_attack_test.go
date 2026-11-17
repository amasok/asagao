package store

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// Commit itself fails (a deferred foreign key checked at COMMIT): the rows are rolled
// back, so the cached copy must go.
func TestUploadAttachmentLocalCommitFailureRemovesCopy(t *testing.T) {
	s := newTestStore(t)
	for _, q := range []string{
		`CREATE TABLE zz_chk(x INTEGER REFERENCES zz_none(id) DEFERRABLE INITIALLY DEFERRED)`,
		`CREATE TABLE zz_none(id INTEGER PRIMARY KEY)`,
		`CREATE TRIGGER zz_t AFTER INSERT ON attachments BEGIN INSERT INTO zz_chk VALUES(1); END`,
	} {
		_, err := s.db.Exec(q)
		must(t, err)
	}
	dir := t.TempDir()
	src := filepath.Join(t.TempDir(), "a.txt")
	must(t, os.WriteFile(src, []byte("x"), 0o600))
	if _, err := s.UploadAttachmentLocal(context.Background(), "t1", src, dir, testNow); err == nil {
		t.Fatal("upload should fail at commit")
	}
	if got := dirNames(t, dir); len(got) != 0 {
		t.Errorf("cache = %v, want empty", got)
	}
	if n := rowCount(t, s, "attachments") + rowCount(t, s, "outbox"); n != 0 {
		t.Errorf("%d rows left", n)
	}
}

func TestUploadAttachmentLocalCancelledAndBadCache(t *testing.T) {
	s := newTestStore(t)
	src := filepath.Join(t.TempDir(), "a.txt")
	must(t, os.WriteFile(src, []byte("x"), 0o600))

	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.UploadAttachmentLocal(ctx, "t1", src, dir, testNow); err == nil {
		t.Error("cancelled context: want an error")
	}
	if got := dirNames(t, dir); len(got) != 0 {
		t.Errorf("cancelled: cache = %v, want empty", got)
	}

	// the cache directory cannot be made (a file is in the way)
	blocker := filepath.Join(t.TempDir(), "f")
	must(t, os.WriteFile(blocker, []byte("f"), 0o600))
	if _, err := s.UploadAttachmentLocal(context.Background(), "t1", src, filepath.Join(blocker, "sub"), testNow); err == nil {
		t.Error("bad cache dir: want an error")
	}
	if b, err := os.ReadFile(blocker); err != nil || string(b) != "f" {
		t.Errorf("blocker = %q %v", b, err)
	}

	// the cache directory is read-only: nothing may be created or left
	ro := t.TempDir()
	must(t, os.Chmod(ro, 0o500))
	defer os.Chmod(ro, 0o700)
	if _, err := s.UploadAttachmentLocal(context.Background(), "t1", src, ro, testNow); err == nil && os.Getuid() != 0 {
		t.Error("read-only cache: want an error")
	}
	if n := rowCount(t, s, "attachments") + rowCount(t, s, "outbox"); n != 0 {
		t.Errorf("%d rows left", n)
	}
}

// Many uploads of the same file at once: each gets its own copy and every one is kept.
func TestUploadAttachmentLocalConcurrent(t *testing.T) {
	s := newTestStore(t)
	dir := t.TempDir()
	src := filepath.Join(t.TempDir(), "a.txt")
	must(t, os.WriteFile(src, []byte("x"), 0o600))
	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = s.UploadAttachmentLocal(context.Background(), "t1", src, dir, testNow)
		}()
	}
	wg.Wait()
	ok := 0
	for _, e := range errs {
		if e == nil {
			ok++
		}
	}
	if got := dirNames(t, dir); len(got) != ok {
		t.Errorf("%d copies for %d successful uploads: %v (errs %v)", len(got), ok, got, errs)
	}
	if rows := rowCount(t, s, "attachments"); rows != ok {
		t.Errorf("%d rows for %d successes", rows, ok)
	}
}
