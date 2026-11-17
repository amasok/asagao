package sync

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/store"
)

func resolveStore(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ref := func(gid string) *asana.Ref { return &asana.Ref{GID: gid} }
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.SetSyncState(ctx, store.KeyMeGID, "me"))
	must(st.UpsertUser(ctx, asana.User{GID: "me", Name: "Me"}))
	must(st.UpsertWorkspaces(ctx, []asana.Workspace{{GID: "w1", Name: "WS"}}))
	must(st.UpsertProjects(ctx, "w1", []asana.Project{{GID: "p1", Name: "新居", Members: []asana.Ref{{GID: "me"}}}}, "me"))
	must(st.ReplaceSections(ctx, "p1", []asana.Section{{GID: "s1", Name: "家具"}, {GID: "s2", Name: "手続き"}}))
	must(st.UpsertTasks(ctx, []asana.Task{
		{GID: "t1", Name: "ソファを買う", Workspace: ref("w1"), Memberships: []asana.Membership{{Project: ref("p1"), Section: ref("s1")}}},
	}))
	return st
}

// queue makes one outbox entry of the given kind and returns its id.
func queue(t *testing.T, st *store.Store, kind string) int64 {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	var err error
	switch kind {
	case store.OpUpdateTask:
		err = st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "ローカル"}, now)
	case store.OpCreateTask:
		_, err = st.CreateTaskLocal(ctx, store.NewTask{Name: "新規", WorkspaceGID: "w1"}, now)
	case store.OpRenameSection:
		err = st.RenameSectionLocal(ctx, "s1", "家具2", now)
	case store.OpCreateStatus:
		_, err = st.CreateStatusLocal(ctx, "p1", "on_track", "", "順調", "", store.UserRef{GID: "me"}, now)
	case store.OpAddComment:
		_, err = st.AddCommentLocal(ctx, "t1", "メモ", store.UserRef{GID: "me"}, now)
	case store.OpMoveTask:
		err = st.MoveTaskLocal(ctx, "t1", "p1", "s2", now)
	case store.OpDuplicateTask:
		_, err = st.DuplicateTaskLocal(ctx, "t1", "", now)
	default:
		t.Fatalf("no fixture for %s", kind)
	}
	if err != nil {
		t.Fatal(err)
	}
	ops, err := st.ListOps(ctx)
	if err != nil || len(ops) == 0 {
		t.Fatalf("ListOps = %v, %v", ops, err)
	}
	return ops[len(ops)-1].ID
}

func setState(t *testing.T, st *store.Store, id int64, state string) {
	t.Helper()
	ctx := context.Background()
	var err error
	switch state {
	case store.OpConflict:
		err = st.MarkOpConflict(ctx, id, map[string]any{"name": "サーバー"})
	case store.OpFailed:
		err = st.MarkOpFailed(ctx, id, errors.New("boom"))
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestResolveStateRules(t *testing.T) {
	for _, tc := range []struct {
		action  ResolveAction
		state   string
		wantErr bool // ErrWrongState
		push    bool
		gone    bool // the entry is removed
	}{
		{KeepLocal, store.OpConflict, false, true, false},
		{KeepLocal, store.OpPending, true, false, false},
		{KeepLocal, store.OpFailed, true, false, false},
		{KeepRemote, store.OpConflict, false, false, true},
		{KeepRemote, store.OpPending, true, false, false},
		{KeepRemote, store.OpFailed, true, false, false},
		{Retry, store.OpFailed, false, true, false},
		{Retry, store.OpPending, true, false, false},
		{Retry, store.OpConflict, true, false, false},
		{Discard, store.OpPending, false, false, true},
		{Discard, store.OpConflict, false, false, true},
		{Discard, store.OpFailed, false, false, true},
	} {
		t.Run(string(tc.action)+"/"+tc.state, func(t *testing.T) {
			ctx := context.Background()
			st := resolveStore(t)
			id := queue(t, st, store.OpUpdateTask)
			setState(t, st, id, tc.state)

			r, err := Resolve(ctx, st, id, tc.action)
			if tc.wantErr {
				if !errors.Is(err, ErrWrongState) {
					t.Fatalf("err = %v, want ErrWrongState", err)
				}
				if op, _ := st.GetOp(ctx, id); op == nil || op.State != tc.state {
					t.Fatalf("a refused action must not change the entry: %+v", op)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if r.Push != tc.push || r.Op.ID != id || r.Action != tc.action {
				t.Fatalf("resolution = %+v", r)
			}
			op, err := st.GetOp(ctx, id)
			if tc.gone {
				if !errors.Is(err, store.ErrNotFound) {
					t.Fatalf("entry should be removed: %+v, %v", op, err)
				}
				if r.RefreshGID != "t1" {
					t.Fatalf("dropping a task change should refresh the task: %+v", r)
				}
				return
			}
			if err != nil || op.State != store.OpPending {
				t.Fatalf("entry should be pending again: %+v, %v", op, err)
			}
			if tc.action == KeepLocal && op.Base["name"] != "サーバー" {
				t.Fatalf("keep_local should take the server value as base: %+v", op.Base)
			}
		})
	}
}

func TestResolveRefreshTarget(t *testing.T) {
	for _, tc := range []struct {
		kind string
		want string
	}{
		{store.OpUpdateTask, "t1"},
		{store.OpMoveTask, "t1"},
		{store.OpAddComment, "t1"},
		{store.OpCreateTask, ""},    // the local task is deleted
		{store.OpDuplicateTask, ""}, // the local copy is deleted
		{store.OpRenameSection, ""}, // task_gid is a section
		{store.OpCreateStatus, ""},  // task_gid is a project
	} {
		t.Run(tc.kind, func(t *testing.T) {
			st := resolveStore(t)
			id := queue(t, st, tc.kind)
			r, err := Resolve(context.Background(), st, id, Discard)
			if err != nil {
				t.Fatal(err)
			}
			if r.RefreshGID != tc.want {
				t.Fatalf("RefreshGID = %q, want %q", r.RefreshGID, tc.want)
			}
		})
	}
}

func TestResolveBadInput(t *testing.T) {
	ctx := context.Background()
	st := resolveStore(t)
	if _, err := Resolve(ctx, st, 999, Discard); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown id: %v", err)
	}
	id := queue(t, st, store.OpUpdateTask)
	if _, err := Resolve(ctx, st, id, "explode"); err == nil {
		t.Fatal("unknown action should fail")
	}
	if op, _ := st.GetOp(ctx, id); op == nil {
		t.Fatal("unknown action must not touch the entry")
	}
	for _, s := range []string{"keep_local", "keep_remote", "retry", "discard"} {
		if a, err := ParseResolveAction(s); err != nil || string(a) != s {
			t.Errorf("ParseResolveAction(%q) = %q, %v", s, a, err)
		}
	}
	if _, err := ParseResolveAction("local"); err == nil {
		t.Error(`ParseResolveAction("local") should fail`)
	}
}

// An entry Push is sending cannot be resolved by anyone.
func TestResolveRefusesSending(t *testing.T) {
	for _, action := range []ResolveAction{KeepLocal, KeepRemote, Retry, Discard} {
		t.Run(string(action), func(t *testing.T) {
			ctx := context.Background()
			st := resolveStore(t)
			id := queue(t, st, store.OpUpdateTask)
			if ok, err := st.BeginSend(ctx, id, time.Now()); !ok || err != nil {
				t.Fatalf("BeginSend = %v, %v", ok, err)
			}
			_, err := Resolve(ctx, st, id, action)
			if !errors.Is(err, store.ErrOpSending) {
				t.Fatalf("err = %v, want ErrOpSending", err)
			}
			if op, _ := st.GetOp(ctx, id); op == nil || op.State != store.OpSending {
				t.Fatalf("the entry must be left alone: %+v", op)
			}
		})
	}
}
