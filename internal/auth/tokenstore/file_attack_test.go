package tokenstore

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Save does not reuse a fixed temporary file: a leftover "token.json.tmp" (here 0644) does
// not lend its mode to the token file, and is removed. (#37)
func TestFileSaveIgnoresLeftoverTemp(t *testing.T) {
	dir := t.TempDir()
	f := &File{Path: filepath.Join(dir, "token.json")}
	if err := os.WriteFile(f.Path+".tmp", []byte(`{"refresh_token":"old"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.Save(context.Background(), &Credentials{RefreshToken: "rt"}); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(f.Path); err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
		t.Fatalf("token file: %v %v", fi, err)
	}
	if _, err := os.Lstat(f.Path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("leftover temp file still there: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("files left in the folder: %v", entries)
	}
}

// Save does not follow symbolic links: neither a link at the old temporary name nor the
// token file being a link lets it write to the file the link points to. (#37)
func TestFileSaveDoesNotFollowSymlinks(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(outside, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &File{Path: filepath.Join(dir, "token.json")}
	if err := os.Symlink(outside, f.Path+".tmp"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, f.Path); err != nil {
		t.Fatal(err)
	}
	if err := f.Save(context.Background(), &Credentials{RefreshToken: "secret"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(outside); string(b) != "keep" {
		t.Fatalf("file behind the link was written: %q", b)
	}
	if fi, err := os.Lstat(outside); err != nil || fi.Mode().Perm() != 0o644 {
		t.Fatalf("file behind the link changed: %v %v", fi, err)
	}
	fi, err := os.Lstat(f.Path)
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
		t.Fatalf("token file should be a new 0600 file: %v %v", fi, err)
	}
	if c, err := f.Load(context.Background()); err != nil || c.RefreshToken != "secret" {
		t.Fatalf("load: %+v %v", c, err)
	}
}

// A failed save leaves no temporary file behind.
func TestFileSaveFailureCleansUp(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "token.json")
	if err := os.Mkdir(target, 0o700); err != nil { // rename over a directory fails
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "x"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (&File{Path: target}).Save(context.Background(), &Credentials{}); err == nil {
		t.Fatal("save over a directory should fail")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("files left in the folder: %v", entries)
	}
}
