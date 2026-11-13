package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/amasok/asana-tui/internal/asana"
)

var testNow = time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func onlyOp(t *testing.T, s *Store) Op {
	t.Helper()
	ops, err := s.ListOps(context.Background())
	must(t, err)
	if len(ops) != 1 {
		t.Fatalf("want 1 outbox entry, got %+v", ops)
	}
	return ops[0]
}

func numSubtasks(t *testing.T, s *Store, gid string) int {
	t.Helper()
	d, err := s.GetTask(context.Background(), gid)
	must(t, err)
	return d.NumSubtasks
}

func taskExists(s *Store, gid string) bool {
	_, err := s.GetTask(context.Background(), gid)
	return err == nil
}

func TestDiscardSubtaskCreateDecrementsParent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	before := numSubtasks(t, s, "t1")
	child, err := s.CreateTaskLocal(ctx, NewTask{ParentGID: "t1", Name: "子"}, testNow)
	must(t, err)
	if numSubtasks(t, s, "t1") != before+1 {
		t.Fatal("creating a subtask should count it")
	}
	must(t, s.DiscardOp(ctx, onlyOp(t, s).ID))
	if taskExists(s, child) {
		t.Fatal("discarded subtask should be removed")
	}
	if got := numSubtasks(t, s, "t1"); got != before {
		t.Fatalf("num_subtasks = %d, want %d", got, before)
	}
}

func TestDiscardParentCreateRemovesLocalDescendants(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	parent, err := s.CreateTaskLocal(ctx, NewTask{ProjectGID: "p1", Name: "親"}, testNow)
	must(t, err)
	child, err := s.CreateTaskLocal(ctx, NewTask{ParentGID: parent, Name: "子"}, testNow)
	must(t, err)
	grandchild, err := s.CreateTaskLocal(ctx, NewTask{ParentGID: child, Name: "孫"}, testNow)
	must(t, err)
	_, err = s.AddCommentLocal(ctx, child, "メモ", UserRef{GID: "me"}, testNow)
	must(t, err)
	must(t, s.UpdateTaskLocal(ctx, "t2", map[string]any{"name": "別のタスク"}, testNow)) // unrelated

	ops, _ := s.ListOps(ctx)
	must(t, s.DiscardOp(ctx, ops[0].ID)) // the parent's create
	for _, gid := range []string{parent, child, grandchild} {
		if taskExists(s, gid) {
			t.Fatalf("task %s should be removed with its parent", gid)
		}
	}
	if op := onlyOp(t, s); op.TaskGID != "t2" {
		t.Fatalf("only the unrelated entry should remain: %+v", op)
	}
}

func TestReplaceAttachmentsKeepsUnsentUploads(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "memo.txt")
	must(t, os.WriteFile(file, []byte("x"), 0o600))
	local, err := s.UploadAttachmentLocal(ctx, "t1", file, t.TempDir(), testNow)
	must(t, err)
	must(t, s.ReplaceAttachments(ctx, "t1", []asana.Attachment{{GID: "a1", Name: "server.png"}}))
	d, _ := s.GetTask(ctx, "t1")
	gids := map[string]bool{}
	for _, a := range d.Attachments {
		gids[a.GID] = true
	}
	if !gids[local] || !gids["a1"] || len(gids) != 2 {
		t.Fatalf("attachments = %+v", d.Attachments)
	}
}

func TestEditWhileConflictedMergesIntoConflict(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	must(t, s.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "ローカル"}, testNow))
	op := onlyOp(t, s)
	must(t, s.MarkOpConflict(ctx, op.ID, map[string]any{"name": "サーバー"}))

	must(t, s.UpdateTaskLocal(ctx, "t1", map[string]any{"completed": true}, testNow))
	op = onlyOp(t, s)
	if op.State != OpConflict || op.Base["name"] != "ソファを買う" || op.Payload["name"] != "ローカル" || op.Payload["completed"] != true {
		t.Fatalf("edit should join the conflicted entry: %+v", op)
	}
	// Keeping the local values sends both changes against the server's base.
	must(t, s.ResolveKeepLocal(ctx, op.ID))
	op = onlyOp(t, s)
	if op.State != OpPending || op.Base["name"] != "サーバー" || len(op.Payload) != 2 {
		t.Fatalf("after keep local: %+v", op)
	}
}

func TestEditRevertingConflictedFieldClearsConflict(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	must(t, s.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "ローカル", "completed": true}, testNow))
	op := onlyOp(t, s)
	must(t, s.MarkOpConflict(ctx, op.ID, map[string]any{"name": "サーバー"}))
	must(t, s.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "ソファを買う"}, testNow)) // back to base
	op = onlyOp(t, s)
	if op.State != OpPending || len(op.Conflict) != 0 || op.Payload["name"] != nil || op.Payload["completed"] != true {
		t.Fatalf("no conflicting field is left; the entry should be pending: %+v", op)
	}
}

func TestEditWhileSendingQueuesNewEntry(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	must(t, s.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "送信中"}, testNow))
	sending := onlyOp(t, s)
	ok, err := s.BeginSend(ctx, sending.ID)
	if !ok || err != nil {
		t.Fatalf("begin send: %v %v", ok, err)
	}
	must(t, s.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "その後"}, testNow))
	ops, _ := s.ListOps(ctx)
	if len(ops) != 2 || ops[1].State != OpPending || ops[1].Base["name"] != "送信中" || ops[1].Payload["name"] != "その後" {
		t.Fatalf("an edit during send must not change the entry being sent: %+v", ops)
	}
	if ops[0].Payload["name"] != "送信中" {
		t.Fatalf("entry being sent changed: %+v", ops[0])
	}
}

func TestFailedUpdateIsNotReappliedAfterPull(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	must(t, s.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "失敗した変更"}, testNow))
	must(t, s.UpdateTaskLocal(ctx, "t2", map[string]any{"name": "未送信の変更"}, testNow))
	ops, _ := s.ListOps(ctx)
	must(t, s.MarkOpFailed(ctx, ops[0].ID, errors.New("asana: 403")))
	must(t, s.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "サーバー1"}, {GID: "t2", Name: "サーバー2"}}))
	if d, _ := s.GetTask(ctx, "t1"); d.Name != "サーバー1" {
		t.Fatalf("a failed change must not be reapplied over the server value: %q", d.Name)
	}
	if d, _ := s.GetTask(ctx, "t2"); d.Name != "未送信の変更" {
		t.Fatalf("a pending change must be reapplied: %q", d.Name)
	}
}

func TestRetryOp(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	must(t, s.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "x"}, testNow))
	op := onlyOp(t, s)
	must(t, s.MarkOpFailed(ctx, op.ID, errors.New("asana: 500")))
	must(t, s.RetryOp(ctx, op.ID))
	if op = onlyOp(t, s); op.State != OpPending || op.LastError != "" {
		t.Fatalf("after retry: %+v", op)
	}
	ok, _ := s.BeginSend(ctx, op.ID)
	if !ok {
		t.Fatal("begin send")
	}
	for name, fn := range map[string]func(context.Context, int64) error{
		"retry": s.RetryOp, "discard": s.DiscardOp, "keep_local": s.ResolveKeepLocal,
	} {
		if err := fn(ctx, op.ID); !errors.Is(err, ErrOpSending) {
			t.Fatalf("%s of an entry being sent = %v, want ErrOpSending", name, err)
		}
	}
	if err := s.RetryOp(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("retry of a missing entry = %v", err)
	}
}

func TestDiscardOpByKind(t *testing.T) {
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "memo.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		queue func(t *testing.T, s *Store) string // returns a gid used by check
		check func(t *testing.T, s *Store, gid string)
	}{
		{"update restores base", func(t *testing.T, s *Store) string {
			must(t, s.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "x", "due_on": ""}, testNow))
			return ""
		}, func(t *testing.T, s *Store, _ string) {
			d, _ := s.GetTask(ctx, "t1")
			if d.Name != "ソファを買う" || d.DueOn != "2026-11-20" {
				t.Fatalf("task = %+v", d.TaskSummary)
			}
		}},
		{"move restores section", func(t *testing.T, s *Store) string {
			must(t, s.MoveTaskLocal(ctx, "t1", "p1", "s2", testNow))
			return ""
		}, func(t *testing.T, s *Store, _ string) {
			d, _ := s.GetTask(ctx, "t1")
			if d.Memberships[0].SectionGID != "s1" {
				t.Fatalf("memberships = %+v", d.Memberships)
			}
		}},
		{"comment is removed", func(t *testing.T, s *Store) string {
			gid, err := s.AddCommentLocal(ctx, "t1", "コメント", UserRef{GID: "me"}, testNow)
			must(t, err)
			return gid
		}, func(t *testing.T, s *Store, gid string) {
			d, _ := s.GetTask(ctx, "t1")
			for _, st := range d.Stories {
				if st.GID == gid {
					t.Fatal("comment still there")
				}
			}
		}},
		{"follower add is undone", func(t *testing.T, s *Store) string {
			must(t, s.SetFollowerLocal(ctx, "t1", "u9", true, testNow))
			return ""
		}, func(t *testing.T, s *Store, _ string) {
			if f, _ := s.TaskFollowers(ctx, "t1"); len(f) != 0 {
				t.Fatalf("followers = %+v", f)
			}
		}},
		{"project add is undone", func(t *testing.T, s *Store) string {
			must(t, s.SetProjectLocal(ctx, "t1", "p2", "", true, testNow))
			return ""
		}, func(t *testing.T, s *Store, _ string) {
			if d, _ := s.GetTask(ctx, "t1"); len(d.Memberships) != 1 {
				t.Fatalf("memberships = %+v", d.Memberships)
			}
		}},
		{"tag add is undone", func(t *testing.T, s *Store) string {
			_, err := s.AddTagLocal(ctx, "t1", "", "新タグ", testNow)
			must(t, err)
			return ""
		}, func(t *testing.T, s *Store, _ string) {
			if tags, _ := s.TaskTags(ctx, "t1"); len(tags) != 1 || tags[0].GID != "g1" {
				t.Fatalf("tags = %+v", tags)
			}
		}},
		{"tag removal is undone", func(t *testing.T, s *Store) string {
			must(t, s.RemoveTagLocal(ctx, "t1", "g1", testNow))
			return ""
		}, func(t *testing.T, s *Store, _ string) {
			if tags, _ := s.TaskTags(ctx, "t1"); len(tags) != 1 || tags[0].GID != "g1" {
				t.Fatalf("tags = %+v", tags)
			}
		}},
		{"section rename is undone", func(t *testing.T, s *Store) string {
			must(t, s.RenameSectionLocal(ctx, "s1", "新しい名前", testNow))
			return ""
		}, func(t *testing.T, s *Store, _ string) {
			if n, _ := s.SectionName(ctx, "s1"); n != "家具" {
				t.Fatalf("section name = %q", n)
			}
		}},
		{"upload is removed", func(t *testing.T, s *Store) string {
			gid, err := s.UploadAttachmentLocal(ctx, "t1", file, t.TempDir(), testNow)
			must(t, err)
			return gid
		}, func(t *testing.T, s *Store, gid string) {
			if _, err := s.GetAttachment(ctx, gid); !errors.Is(err, ErrNotFound) {
				t.Fatalf("attachment still there: %v", err)
			}
		}},
		{"status update is removed", func(t *testing.T, s *Store) string {
			_, err := s.CreateStatusLocal(ctx, "p1", "on_track", "t", "x", "", UserRef{Name: "Me"}, testNow)
			must(t, err)
			return ""
		}, func(t *testing.T, s *Store, _ string) {
			if ups, _ := s.StatusUpdates(ctx, "p1"); len(ups) != 0 {
				t.Fatalf("status updates = %+v", ups)
			}
		}},
		{"duplicate is removed", func(t *testing.T, s *Store) string {
			gid, err := s.DuplicateTaskLocal(ctx, "t1", "", testNow)
			must(t, err)
			return gid
		}, func(t *testing.T, s *Store, gid string) {
			if taskExists(s, gid) {
				t.Fatal("local copy still there")
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newTestStore(t)
			gid := c.queue(t, s)
			must(t, s.DiscardOp(ctx, onlyOp(t, s).ID))
			if ops, _ := s.ListOps(ctx); len(ops) != 0 {
				t.Fatalf("outbox not empty: %+v", ops)
			}
			c.check(t, s, gid)
		})
	}
}

func TestPruneProjectsRemovesRelatedRows(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	must(t, s.UpsertProjects(ctx, "w1", []asana.Project{{GID: "p3", Name: "仕事", Members: []asana.Ref{{GID: "u2"}}}}, "me"))
	must(t, s.ReplaceStatusUpdates(ctx, "p3", []asana.StatusUpdate{{GID: "su1", StatusType: "on_track", Title: "t"}}))
	must(t, s.UpsertTasks(ctx, []asana.Task{{GID: "t9", Name: "x",
		Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p3"}}, {Project: &asana.Ref{GID: "p1"}}}}}))
	must(t, s.PruneProjects(ctx, "w1", []asana.Project{{GID: "p1"}, {GID: "p2"}}))
	for table, col := range map[string]string{"project_members": "project_gid", "status_updates": "project_gid", "task_memberships": "project_gid", "sections": "project_gid", "projects": "gid"} {
		var n int
		must(t, s.db.QueryRowContext(ctx, "SELECT count(*) FROM "+table+" WHERE "+col+" = 'p3'").Scan(&n))
		if n != 0 {
			t.Errorf("%s still has %d rows of the pruned project", table, n)
		}
	}
	if d, _ := s.GetTask(ctx, "t9"); len(d.Memberships) != 1 {
		t.Fatalf("membership in the kept project should stay: %+v", d.Memberships)
	}
}

func TestEditLocalTaskWhileItsCreateIsSendingOrFailed(t *testing.T) {
	for _, state := range []string{OpSending, OpFailed} {
		t.Run(state, func(t *testing.T) {
			s := newTestStore(t)
			ctx := context.Background()
			gid, err := s.CreateTaskLocal(ctx, NewTask{ProjectGID: "p1", SectionGID: "s1", Name: "新規"}, testNow)
			must(t, err)
			create := onlyOp(t, s)
			if ok, err := s.BeginSend(ctx, create.ID); !ok || err != nil {
				t.Fatalf("begin send: %v %v", ok, err)
			}
			if state == OpFailed {
				must(t, s.MarkOpUnknown(ctx, create.ID))
			}
			must(t, s.UpdateTaskLocal(ctx, gid, map[string]any{"name": "名前変更"}, testNow))
			must(t, s.MoveTaskLocal(ctx, gid, "p1", "s2", testNow))
			ops, _ := s.ListOps(ctx)
			if len(ops) != 3 || ops[0].Payload["name"] != "新規" || ops[0].Payload["section"] != "s1" {
				t.Fatalf("the create being sent must not change: %+v", ops)
			}
			if ops[1].Kind != OpUpdateTask || ops[1].TaskGID != gid || ops[1].Payload["name"] != "名前変更" ||
				ops[2].Kind != OpMoveTask || ops[2].Payload["section"] != "s2" {
				t.Fatalf("edits should be queued for the local task: %+v", ops)
			}
			if d, _ := s.GetTask(ctx, gid); d.Name != "名前変更" || d.Memberships[0].SectionGID != "s2" {
				t.Fatalf("local task = %+v", d.TaskSummary)
			}
			if state == OpSending { // Asana created it: the queued edits follow the real gid
				must(t, s.CompleteCreate(ctx, create.ID, gid, "R1", nil))
				ops, _ = s.ListOps(ctx)
				if len(ops) != 2 || ops[0].TaskGID != "R1" || ops[1].TaskGID != "R1" {
					t.Fatalf("after create: %+v", ops)
				}
			} else { // discarding the failed create drops the edits too
				must(t, s.DiscardOp(ctx, create.ID))
				if ops, _ = s.ListOps(ctx); len(ops) != 0 {
					t.Fatalf("after discard: %+v", ops)
				}
			}
		})
	}
}
