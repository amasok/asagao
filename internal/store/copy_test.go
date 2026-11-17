package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana/model"
)

// A copy holds the data, and a write to it leaves the original (and its outbox) alone.
func TestCopyToIsIndependent(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := Open(ctx, filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SetSyncState(ctx, "k", "original"); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "copy.db")
	if err := st.CopyTo(ctx, dst); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(dst); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("copy: %v %v", fi, err)
	}
	cp, err := Open(ctx, dst)
	if err != nil {
		t.Fatal(err)
	}
	defer cp.Close()
	if v, _ := cp.SyncState(ctx, "k"); v != "original" {
		t.Fatalf("copy lacks the data: %q", v)
	}
	if err := cp.SetSyncState(ctx, "k", "changed"); err != nil {
		t.Fatal(err)
	}
	if err := cp.ReplaceSections(ctx, "p1", []model.Section{{GID: "s1", Name: "家具"}}); err != nil {
		t.Fatal(err)
	}
	if err := cp.RenameSectionLocal(ctx, "s1", "大型家具", time.Now()); err != nil {
		t.Fatal(err)
	}
	if ops, _ := cp.ListOps(ctx); len(ops) != 1 {
		t.Fatalf("the copy's outbox: %d entries", len(ops))
	}
	if v, _ := st.SyncState(ctx, "k"); v != "original" {
		t.Errorf("a write to the copy reached the original: %q", v)
	}
	if ops, _ := st.ListOps(ctx); len(ops) != 0 {
		t.Errorf("the original's outbox got %d entries", len(ops))
	}
}

// An existing file is not overwritten.
func TestCopyToRefusesExisting(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := Open(ctx, filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	dst := filepath.Join(dir, "x")
	if err := os.WriteFile(dst, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := st.CopyTo(ctx, dst); err == nil {
		t.Fatal("overwrote an existing file")
	}
	if b, _ := os.ReadFile(dst); string(b) != "keep" {
		t.Errorf("existing file changed: %q", b)
	}
}
