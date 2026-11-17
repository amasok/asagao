package store

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	must(t, err)
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}

// Issue #16: when recording the upload fails, the copy made in the cache must not stay.
func TestUploadAttachmentLocalFailureRemovesCachedCopy(t *testing.T) {
	cases := map[string]func(t *testing.T, s *Store){
		"attachment row refused": func(t *testing.T, s *Store) {
			_, err := s.db.Exec(`CREATE TRIGGER fail_att BEFORE INSERT ON attachments BEGIN SELECT RAISE(ABORT, 'boom'); END`)
			must(t, err)
		},
		// The attachment row is written first; the rollback must take it away too.
		"outbox row refused": func(t *testing.T, s *Store) {
			_, err := s.db.Exec(`CREATE TRIGGER fail_op BEFORE INSERT ON outbox BEGIN SELECT RAISE(ABORT, 'boom'); END`)
			must(t, err)
		},
		// Both rows are written, then Commit fails on a deferred foreign key: the
		// row is confirmed absent after the error, so the copy goes too.
		"commit refused": func(t *testing.T, s *Store) {
			_, err := s.db.Exec(`CREATE TABLE zz(x TEXT REFERENCES tasks(gid) DEFERRABLE INITIALLY DEFERRED)`)
			must(t, err)
			_, err = s.db.Exec(`CREATE TRIGGER zz_t AFTER INSERT ON outbox BEGIN INSERT INTO zz VALUES('nope'); END`)
			must(t, err)
		},
		"database closed": func(t *testing.T, s *Store) {
			must(t, s.Close())
		},
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			s := newTestStore(t)
			ctx := context.Background()
			dir := t.TempDir()
			src := filepath.Join(t.TempDir(), "図面 (1).png")
			must(t, os.WriteFile(src, []byte("png"), 0o600))
			// A file already in the cache (a downloaded attachment) must stay as it is.
			old := filepath.Join(dir, "77-img.png")
			must(t, os.WriteFile(old, []byte("old"), 0o600))
			breakIt(t, s)

			if _, err := s.UploadAttachmentLocal(ctx, "t1", src, dir, testNow); err == nil {
				t.Fatal("upload should fail")
			}
			if got := dirNames(t, dir); !slices.Equal(got, []string{"77-img.png"}) {
				t.Errorf("cache = %v, want only the file that was there", got)
			}
			if b, err := os.ReadFile(old); err != nil || string(b) != "old" {
				t.Errorf("existing file = %q %v", b, err)
			}
			if b, err := os.ReadFile(src); err != nil || string(b) != "png" {
				t.Errorf("the user's file = %q %v", b, err)
			}
			if name != "database closed" {
				if n := rowCount(t, s, "attachments") + rowCount(t, s, "outbox"); n != 0 {
					t.Errorf("%d rows left", n)
				}
			}
		})
	}
}

func TestUploadAttachmentLocalSuccessKeepsCachedCopy(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	dir := t.TempDir()
	src := filepath.Join(t.TempDir(), "a.txt")
	must(t, os.WriteFile(src, []byte("x"), 0o600))
	if _, err := s.UploadAttachmentLocal(ctx, "t1", src, dir, testNow); err != nil {
		t.Fatal(err)
	}
	path, _ := onlyOp(t, s).Payload["path"].(string)
	if b, err := os.ReadFile(path); err != nil || string(b) != "x" {
		t.Fatalf("cached copy %s = %q %v", path, b, err)
	}
}

// The copy is removed only when it is certain nothing refers to it.
func TestDiscardUnrecordedCopy(t *testing.T) {
	ctx := context.Background()
	write := func(t *testing.T) string {
		f := filepath.Join(t.TempDir(), "x")
		must(t, os.WriteFile(f, []byte("x"), 0o600))
		return f
	}
	exists := func(f string) bool { _, err := os.Stat(f); return err == nil }

	t.Run("no row: removed", func(t *testing.T) {
		s, f := newTestStore(t), write(t)
		s.discardUnrecordedCopy("local:a", f)
		if exists(f) {
			t.Error("file kept")
		}
	})
	t.Run("row there (committed after all): kept", func(t *testing.T) {
		s, f := newTestStore(t), write(t)
		_, err := s.db.ExecContext(ctx, `INSERT INTO attachments(gid, parent_gid, name, local_path) VALUES('local:a', 't1', 'x', ?)`, f)
		must(t, err)
		s.discardUnrecordedCopy("local:a", f)
		if !exists(f) {
			t.Error("file removed while the row refers to it")
		}
	})
	t.Run("cannot check: kept", func(t *testing.T) {
		s, f := newTestStore(t), write(t)
		must(t, s.Close())
		s.discardUnrecordedCopy("local:a", f)
		if !exists(f) {
			t.Error("file removed without knowing")
		}
	})
}

// copyFile never writes over, or removes, a file already at dst.
func TestCopyFileKeepsExistingDestination(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src")
	must(t, os.WriteFile(src, []byte("new"), 0o600))
	info, err := os.Stat(src)
	must(t, err)
	dst := filepath.Join(t.TempDir(), "dst")
	must(t, os.WriteFile(dst, []byte("old"), 0o600))
	if _, err := copyFile(src, dst, info, MaxUploadSize); err == nil {
		t.Error("copy over an existing file should fail")
	}
	if b, err := os.ReadFile(dst); err != nil || string(b) != "old" {
		t.Errorf("dst = %q %v", b, err)
	}
}
