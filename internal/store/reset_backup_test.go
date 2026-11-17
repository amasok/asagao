package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResetWithBackup(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)

	t.Run("unsent changes need confirmation; nothing deleted, nothing written", func(t *testing.T) {
		s := newTestStore(t)
		dir := t.TempDir()
		_ = s.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "ローカル"}, now)
		_, _ = s.AddCommentLocal(ctx, "t2", "大事なメモ", UserRef{GID: "me"}, now)
		res, err := s.ResetWithBackup(ctx, dir, 0, now)
		if !errors.Is(err, ErrUnsentChanges) || res.Unsent != 2 || res.BackupPath != "" {
			t.Fatalf("res = %+v, err = %v", res, err)
		}
		if n, _ := s.UnsentCount(ctx); n != 2 {
			t.Fatalf("nothing should be deleted: %d unsent", n)
		}
		if files, _ := os.ReadDir(dir); len(files) != 0 {
			t.Fatalf("no backup without confirmation: %v", files)
		}
	})

	t.Run("confirmed: backup first, then everything goes", func(t *testing.T) {
		s := newTestStore(t)
		dir := filepath.Join(t.TempDir(), "backups") // created on demand
		_, _ = s.AddCommentLocal(ctx, "t2", "大事なメモ\n2行目", UserRef{GID: "me"}, now)
		_ = s.UpdateTaskLocal(ctx, "t1", map[string]any{"completed": true}, now)
		if _, err := s.DiscardDeletedTask(ctx, "t1", "deleted", now); err != nil { // an earlier dropped change
			t.Fatal(err)
		}
		res, err := s.ResetWithBackup(ctx, dir, AnyUnsent, now)
		if err != nil || res.Unsent != 1 || !strings.HasPrefix(res.BackupPath, dir) {
			t.Fatalf("res = %+v, err = %v", res, err)
		}
		data, err := os.ReadFile(res.BackupPath)
		if err != nil {
			t.Fatal(err)
		}
		var b ResetBackup
		if err := json.Unmarshal(data, &b); err != nil {
			t.Fatal(err)
		}
		if b.Account != "me" || len(b.Outbox) != 1 || b.Outbox[0].Payload["text"] != "大事なメモ\n2行目" ||
			len(b.Discarded) != 1 || b.Discarded[0].Kind != OpUpdateTask {
			t.Fatalf("backup = %+v", b)
		}
		if info, _ := os.Stat(res.BackupPath); info.Mode().Perm() != 0o600 {
			t.Errorf("backup should be private: %v", info.Mode())
		}
		if _, err := s.GetTask(ctx, "t2"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("data should be gone: %v", err)
		}
		if n, _ := s.UnsentCount(ctx); n != 0 {
			t.Fatalf("outbox should be empty: %d", n)
		}
	})

	t.Run("nothing to keep: no backup file", func(t *testing.T) {
		s := newTestStore(t)
		dir := t.TempDir()
		res, err := s.ResetWithBackup(ctx, dir, 0, now)
		if err != nil || res.BackupPath != "" || res.Unsent != 0 {
			t.Fatalf("res = %+v, err = %v", res, err)
		}
		if files, _ := os.ReadDir(dir); len(files) != 0 {
			t.Fatalf("unexpected backup: %v", files)
		}
		if _, err := s.GetTask(ctx, "t1"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("data should be gone: %v", err)
		}
	})

	t.Run("backup fails: nothing deleted", func(t *testing.T) {
		s := newTestStore(t)
		file := filepath.Join(t.TempDir(), "not-a-dir")
		_ = os.WriteFile(file, nil, 0o600)
		_, _ = s.AddCommentLocal(ctx, "t2", "大事なメモ", UserRef{GID: "me"}, now)
		for _, dir := range []string{file, ""} {
			if _, err := s.ResetWithBackup(ctx, dir, AnyUnsent, now); err == nil || !strings.Contains(err.Error(), "初期化をやめました") {
				t.Fatalf("dir %q: err = %v", dir, err)
			}
		}
		if n, _ := s.UnsentCount(ctx); n != 1 {
			t.Fatalf("nothing should be deleted: %d unsent", n)
		}
		if _, err := s.GetTask(ctx, "t2"); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("another process is syncing: nothing deleted", func(t *testing.T) {
		s := newTestStore(t)
		unlock, err := s.TryLockPush()
		if err != nil {
			t.Fatal(err)
		}
		defer unlock()
		if _, err := s.ResetWithBackup(ctx, t.TempDir(), AnyUnsent, now); !errors.Is(err, ErrPushBusy) {
			t.Fatalf("err = %v", err)
		}
		if _, err := s.GetTask(ctx, "t1"); err != nil {
			t.Fatal(err)
		}
	})
}

// A reset deletes only the unsent changes the user saw: one queued after the confirmation
// stops it (nothing deleted, no backup) so the user can be asked again.
func TestResetDeletesOnlyConfirmedChanges(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	s := newTestStore(t)
	dir := t.TempDir()
	_, _ = s.AddCommentLocal(ctx, "t2", "確認したメモ", UserRef{GID: "me"}, now)
	n, newest, err := s.UnsentState(ctx)
	if err != nil || n != 1 || newest == 0 {
		t.Fatalf("UnsentState = %d, %d, %v", n, newest, err)
	}
	_, _ = s.AddCommentLocal(ctx, "t2", "あとから足したメモ", UserRef{GID: "me"}, now) // after the confirmation

	res, err := s.ResetWithBackup(ctx, dir, newest, now)
	if !errors.Is(err, ErrUnsentChanges) || res.Unsent != 2 || res.BackupPath != "" {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	if c, _ := s.UnsentCount(ctx); c != 2 {
		t.Fatalf("nothing should be deleted: %d", c)
	}
	if files, _ := os.ReadDir(dir); len(files) != 0 {
		t.Fatalf("no backup: %v", files)
	}
	// the store itself checks too (a change queued between the backup and the reset)
	if _, err := s.ResetUpTo(ctx, newest, now); !errors.Is(err, ErrUnsentChanges) {
		t.Fatalf("ResetUpTo err = %v", err)
	}
	if c, _ := s.UnsentCount(ctx); c != 2 {
		t.Fatalf("nothing should be deleted: %d", c)
	}

	// asked again: both are confirmed now
	_, newest, _ = s.UnsentState(ctx)
	res, err = s.ResetWithBackup(ctx, dir, newest, now)
	if err != nil || res.Unsent != 2 || res.BackupPath == "" {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	// a confirmed change that was sent meanwhile is simply not there: fine
	s2 := newTestStore(t)
	_ = s2.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "x"}, now)
	_, _ = s2.AddCommentLocal(ctx, "t2", "メモ", UserRef{GID: "me"}, now)
	_, newest, _ = s2.UnsentState(ctx)
	ops, _ := s2.ListOps(ctx)
	if err := s2.CompleteOp(ctx, ops[0].ID); err != nil {
		t.Fatal(err)
	}
	if res, err := s2.ResetWithBackup(ctx, dir, newest, now); err != nil || res.Unsent != 1 {
		t.Fatalf("fewer changes than confirmed: %+v, %v", res, err)
	}
}

func TestAccountChangedHelp(t *testing.T) {
	got := AccountChangedHelp(3, "asagao reset")
	for _, want := range []string{"アカウント", "3 件", "ログインし直せば", "asagao auth login", "初期化", "asagao reset", "バックアップ"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}
