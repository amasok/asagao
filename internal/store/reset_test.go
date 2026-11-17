package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/amasok/asagao/internal/asana"
)

func rowCount(t *testing.T, s *Store, table string) int {
	t.Helper()
	var n int
	must(t, s.db.QueryRow("SELECT count(*) FROM "+table).Scan(&n))
	return n
}

func TestResetRefusesWithUnsentChanges(t *testing.T) {
	for _, state := range []string{OpPending, OpFailed, OpConflict, OpSending} {
		t.Run(state, func(t *testing.T) {
			s := newTestStore(t)
			ctx := context.Background()
			must(t, s.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "未送信"}, testNow))
			op := onlyOp(t, s)
			switch state {
			case OpFailed:
				must(t, s.MarkOpFailed(ctx, op.ID, errors.New("x")))
			case OpConflict:
				must(t, s.MarkOpConflict(ctx, op.ID, map[string]any{"name": "y"}))
			case OpSending:
				ok, err := s.BeginSend(ctx, op.ID, testNow)
				if !ok || err != nil {
					t.Fatal(ok, err)
				}
			}
			n, err := s.Reset(ctx, false, testNow)
			if !errors.Is(err, ErrUnsentChanges) || n != 1 {
				t.Fatalf("reset = %d %v", n, err)
			}
			if rowCount(t, s, "tasks") == 0 || rowCount(t, s, "outbox") != 1 {
				t.Fatal("nothing may be deleted without force")
			}
			if me, _ := s.SyncState(ctx, KeyMeGID); me != "me" {
				t.Fatalf("account = %q", me)
			}
		})
	}
}

func TestResetDeletesEverything(t *testing.T) {
	for _, force := range []bool{true, false} {
		t.Run(map[bool]string{true: "force with unsent", false: "nothing unsent"}[force], func(t *testing.T) {
			s := newTestStore(t)
			ctx := context.Background()
			dir := t.TempDir()
			src := filepath.Join(t.TempDir(), "a.txt")
			must(t, os.WriteFile(src, []byte("x"), 0o600))
			downloaded := filepath.Join(dir, "77-img.png")
			must(t, os.WriteFile(downloaded, []byte("png"), 0o600))
			must(t, s.UpsertAttachment(ctx, "t1", attachmentFixture("77")))
			must(t, s.SetAttachmentPath(ctx, "77", downloaded))
			var upload string
			if force {
				_, err := s.UploadAttachmentLocal(ctx, "t1", src, dir, testNow)
				must(t, err)
				op := onlyOp(t, s)
				upload, _ = op.Payload["path"].(string)
				_, err = s.AddCommentLocal(ctx, "t1", "メモ", UserRef{GID: "me"}, testNow)
				must(t, err)
			}
			// A dropped change recorded before (old account's data, not shown yet).
			_, err := s.db.Exec(`INSERT INTO discarded_changes(kind, summary, discarded_at) VALUES('update_task', '前の記録', '2026-11-01T00:00:00Z')`)
			must(t, err)
			n, err := s.Reset(ctx, force, testNow)
			if err != nil || n != map[bool]int{true: 2, false: 0}[force] {
				t.Fatalf("reset = %d %v", n, err)
			}
			for _, table := range []string{"tasks", "projects", "sections", "stories", "attachments", "tags", "task_tags",
				"task_memberships", "users", "workspaces", "outbox", "sync_state", "status_updates", "project_members"} {
				if c := rowCount(t, s, table); c != 0 {
					t.Errorf("%s has %d rows after reset", table, c)
				}
			}
			if c := rowCount(t, s, "tasks_fts WHERE tasks_fts MATCH 'ソファ'"); c != 0 {
				t.Errorf("full-text index still finds %d tasks", c)
			}
			for _, f := range []string{downloaded, upload} {
				if f == "" {
					continue
				}
				if _, err := os.Stat(f); !os.IsNotExist(err) {
					t.Errorf("cached file %s should be deleted (%v)", f, err)
				}
			}
			if _, err := os.Stat(src); err != nil {
				t.Error("the user's original file must stay")
			}
			// Unsent changes deleted with force are kept as dropped changes, with their content.
			d, err := s.PendingDiscards(ctx)
			if err != nil || len(d) != map[bool]int{true: 2, false: 0}[force] {
				t.Fatalf("recorded = %+v %v", d, err)
			}
			if force && (d[1].Payload["text"] != "メモ" || d[1].Reason != ResetReason || d[1].TaskName != "ソファを買う") {
				t.Fatalf("recorded = %+v", d[1])
			}
			// The push lock file stays (another process may hold it).
			if _, err := os.Stat(s.Path() + ".push.lock"); err != nil {
				t.Errorf("lock file: %v", err)
			}
		})
	}
}

func TestResetWaitsForNoPush(t *testing.T) {
	s := newTestStore(t)
	unlock, err := s.TryLockPush()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := s.Reset(context.Background(), true, testNow); !errors.Is(err, ErrPushBusy) {
		t.Fatalf("reset while pushing = %v", err)
	}
	if rowCount(t, s, "tasks") == 0 {
		t.Fatal("nothing may be deleted while another process pushes")
	}
}

func attachmentFixture(gid string) asana.Attachment {
	return asana.Attachment{GID: gid, Name: "img.png"}
}

// Another process with the store open sees the empty store after a reset, and its writes
// to tasks that no longer exist fail.
func TestResetSeenByOtherProcess(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	other, err := Open(ctx, s.Path())
	must(t, err)
	defer other.Close()
	if _, err := other.GetTask(ctx, "t1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reset(ctx, false, testNow); err != nil {
		t.Fatal(err)
	}
	if _, err := other.GetTask(ctx, "t1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other process still sees the task: %v", err)
	}
	if err := other.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "x"}, testNow); !errors.Is(err, ErrNotFound) {
		t.Fatalf("write to a reset task = %v", err)
	}
}
