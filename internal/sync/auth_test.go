package sync

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/auth"
	"github.com/amasok/asagao/internal/store"
)

// revoked answers every request with 401, as Asana does for a revoked or expired PAT.
func revoked(rw http.ResponseWriter, _ *http.Request) bool {
	rw.WriteHeader(http.StatusUnauthorized)
	fmt.Fprint(rw, `{"errors":[{"message":"Not Authorized"}]}`)
	return true
}

func assertUnauthorized(t *testing.T, err error) {
	t.Helper()
	if !asana.IsUnauthorized(err) {
		t.Fatalf("err = %v, want a 401", err)
	}
	if errors.Is(err, ErrAccountChanged) {
		t.Fatalf("a rejected token is not another account: %v", err)
	}
}

// With a revoked token nothing is sent, nothing is marked failed or dropped, and every
// change goes out exactly once after the user logs in again.
func TestPushUnauthorizedKeepsEverything(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	_ = st.UpsertTasks(ctx, []asana.Task{{GID: "t2", Name: "Asana にないかも"}})
	_ = st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "B"}, now)
	_, _ = st.CreateTaskLocal(ctx, store.NewTask{ProjectGID: "p1", Name: "新しいタスク"}, now)
	_, _ = st.AddCommentLocal(ctx, "t1", "コメント", store.UserRef{GID: "me"}, now)
	_ = st.UpdateTaskLocal(ctx, "t2", map[string]any{"completed": true}, now)
	ws.setHook(revoked)

	for range 2 { // pushing again while still logged out changes nothing either
		res, err := s.Push(ctx)
		assertUnauthorized(t, err)
		if res.Sent != 0 || res.Failed != 0 || len(res.Discarded) != 0 || res.Remaining != 4 {
			t.Fatalf("push with a revoked token = %+v %v", res, err)
		}
		ops, _ := st.ListOps(ctx)
		for _, op := range ops {
			if op.State != store.OpPending || op.Verify || op.Attempts != 0 || op.LastError != "" {
				t.Fatalf("entry should wait unchanged for a new login (no attempt counted): %+v", op)
			}
		}
	}
	if _, err := st.GetTask(ctx, "t2"); err != nil {
		t.Fatal("a 401 must not remove the task")
	}
	if d, _ := st.PendingDiscards(ctx); len(d) != 0 {
		t.Fatalf("nothing should be recorded as dropped: %+v", d)
	}

	ws.setHook(nil) // logged in again
	ws.tasks["t2"] = &serverTask{Name: "Asana にないかも"}
	res, err := s.Push(ctx)
	if err != nil || res.Sent != 4 || res.Failed != 0 || res.Remaining != 0 {
		t.Fatalf("push after a new login = %+v %v\n%v", res, err, ws.calls)
	}
	// The 401 requests are in ws.calls too: count the successful creates on the server.
	created := 0
	for _, task := range ws.tasks {
		if task.Name == "新しいタスク" {
			created++
		}
	}
	if created != 1 || ws.tasks["t1"].Name != "B" || !ws.tasks["t2"].Completed || len(ws.stories["t1"]) != 1 {
		t.Fatalf("after a new login each change should be applied once: created=%d tasks=%+v stories=%v", created, ws.tasks, ws.stories)
	}
	assertOutboxEmpty(t, st)
}

// A 401 on a create or a comment means Asana did not process it: it is not left "to be
// verified" (which would look it up before sending it again) but simply waits.
func TestPushUnauthorizedCreateIsNotVerified(t *testing.T) {
	for _, tc := range []struct {
		name  string
		kind  string
		queue func(*store.Store)
	}{
		{"create task", store.OpCreateTask, func(st *store.Store) {
			_, _ = st.CreateTaskLocal(context.Background(), store.NewTask{ProjectGID: "p1", Name: "新しいタスク"}, now)
		}},
		{"comment", store.OpAddComment, func(st *store.Store) {
			_, _ = st.AddCommentLocal(context.Background(), "t1", "コメント", store.UserRef{GID: "me"}, now)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ws, _, s, st := setupPush(t)
			ctx := context.Background()
			tc.queue(st)
			ws.setHook(revoked)
			res, err := s.Push(ctx)
			assertUnauthorized(t, err)
			if res.Failed != 0 || res.Remaining != 1 {
				t.Fatalf("push = %+v %v", res, err)
			}
			if op := opByKind(t, st, tc.kind); op == nil || op.State != store.OpPending || op.Verify {
				t.Fatalf("entry should wait, not be verified: %+v", op)
			}
			ws.setHook(nil)
			if res, err := s.Push(ctx); err != nil || res.Sent != 1 {
				t.Fatalf("push after a new login = %+v %v", res, err)
			}
			assertOutboxEmpty(t, st)
		})
	}
}

// A create whose answer was lost is verified before sending it again. If the token is
// rejected while verifying, the entry keeps waiting to be verified (not failed, not resent).
func TestPushVerifyUnauthorizedKeepsVerifying(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	_, _ = st.CreateTaskLocal(ctx, store.NewTask{ProjectGID: "p1", Name: "届いたかも"}, now)
	ws.setFailAfter("POST /tasks", 1) // created, but the answer is lost
	ws.setHook(func(rw http.ResponseWriter, r *http.Request) bool {
		// Push confirms the account first; the token is revoked right after.
		if r.Method == http.MethodGet && r.URL.Path != "/users/me" {
			return revoked(rw, r)
		}
		return false
	})
	res, err := s.Push(ctx)
	assertUnauthorized(t, err)
	if res.Failed != 0 || res.Remaining != 1 {
		t.Fatalf("push = %+v %v", res, err)
	}
	op := opByKind(t, st, store.OpCreateTask)
	if op == nil || op.State != store.OpPending || !op.Verify {
		t.Fatalf("entry should stay queued to be verified: %+v", op)
	}

	ws.setHook(revoked) // still logged out on the next sync
	s.now = func() time.Time { return time.Now().Add(time.Hour) }
	res, err = s.Push(ctx)
	assertUnauthorized(t, err)
	if op := opByKind(t, st, store.OpCreateTask); res.Failed != 0 || op == nil || op.State != store.OpPending || !op.Verify {
		t.Fatalf("second push = %+v %v, entry %+v", res, err, op)
	}

	ws.setHook(nil)
	s.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if res, err := s.Push(ctx); err != nil || res.Failed != 0 || res.Remaining != 0 {
		t.Fatalf("push after a new login = %+v %v", res, err)
	}
	created := 0
	for _, task := range ws.tasks {
		if task.Name == "届いたかも" {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("the create must not be duplicated (%d on the server)\n%v", created, ws.calls)
	}
	assertOutboxEmpty(t, st)
}

// A 404 makes Push check the account before dropping anything. A 401 on that check is a
// login problem: the change is kept (not dropped, not failed, not ErrAccountChanged).
func TestPushAccountCheckUnauthorizedKeepsChange(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	_ = st.UpsertTasks(ctx, []asana.Task{{GID: "t2", Name: "消えたかも"}})
	_ = st.UpdateTaskLocal(ctx, "t2", map[string]any{"completed": true}, now)
	_, _ = st.AddCommentLocal(ctx, "t2", "メモ", store.UserRef{GID: "me"}, now)
	ws.setHook(func(rw http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/users/me" {
			return revoked(rw, r)
		}
		return false // t2 is not on the server: 404
	})
	res, err := s.Push(ctx)
	assertUnauthorized(t, err)
	if len(res.Discarded) != 0 || res.Failed != 0 || res.Remaining != 2 {
		t.Fatalf("push = %+v %v", res, err)
	}
	ops, _ := st.ListOps(ctx)
	for _, op := range ops {
		if op.State != store.OpPending {
			t.Fatalf("entries should be kept for after a new login: %+v", ops)
		}
	}
	if _, err := st.GetTask(ctx, "t2"); err != nil {
		t.Fatal("the task must not be removed")
	}

	// After logging in again (same account) the deletion is confirmed and both are dropped.
	ws.setHook(nil)
	res, err = s.Push(ctx)
	if err != nil || len(res.Discarded) != 2 || res.Remaining != 0 {
		t.Fatalf("push after a new login = %+v %v", res, err)
	}
}

// Run checks the account first; a 401 there stops the sync with a login problem, not
// ErrAccountChanged, and sends nothing.
func TestRunUnauthorized(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	_ = st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "B"}, now)
	ws.setHook(revoked)
	_, err := s.Run(ctx, false)
	assertUnauthorized(t, err)
	if ws.tasks["t1"].Name != "A" {
		t.Fatal("nothing should be sent")
	}
	if op := opByKind(t, st, store.OpUpdateTask); op == nil || op.State != store.OpPending {
		t.Fatalf("entry = %+v", op)
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v", err)
	}
}

// A token revoked during the sync (after the account check): the pull's 401 still comes
// back as a login problem.
func TestRunUnauthorizedDuringPull(t *testing.T) {
	ws, _, s, _ := setupPush(t)
	ws.setHook(func(rw http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/users/me" {
			return false
		}
		return revoked(rw, r)
	})
	_, err := s.Run(context.Background(), true)
	assertUnauthorized(t, err)
}

type failingTokens struct{ err error }

func (f failingTokens) Token() (*oauth2.Token, error) { return nil, f.err }

// No token to send with (logged out in another terminal, or the OAuth refresh failed):
// the request never left, so a create is not marked "to be verified" (which would look
// it up in Asana before sending), nothing fails, and it is sent once after a new login.
func TestPushWithoutTokenIsNotSent(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		login bool // auth.LoginHelp has advice for it
	}{
		{"logged out", auth.ErrNotLoggedIn, true},
		{"logged in as someone else elsewhere", auth.ErrLoginReplaced, true},
		{"refresh failed", &oauth2.RetrieveError{Response: &http.Response{StatusCode: 500}, ErrorCode: "server_error"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ws, srv, s, st := setupPush(t)
			ctx := context.Background()
			_, _ = st.CreateTaskLocal(ctx, store.NewTask{ProjectGID: "p1", Name: "新しいタスク"}, now)
			_, _ = st.AddCommentLocal(ctx, "t1", "コメント", store.UserRef{GID: "me"}, now)
			good := s.API
			bad := asana.NewClient(oauth2.NewClient(ctx, &auth.Source{TokenSource: failingTokens{tc.err}}))
			bad.BaseURL, bad.MaxRetries = srv.URL, 0
			s.API = bad
			// The token goes away after this Syncer confirmed the account (the usual case
			// in a long-running TUI / MCP server), so the send itself has no token.
			s.accountOK.Store(true)
			for range 2 {
				res, err := s.Push(ctx)
				if err == nil || res.Failed != 0 || res.Remaining != 2 || res.Sent != 0 {
					t.Fatalf("push without a token = %+v %v", res, err)
				}
				if (auth.LoginHelp(err) != "") != tc.login {
					t.Fatalf("LoginHelp(%v) = %q", err, auth.LoginHelp(err))
				}
				ops, _ := st.ListOps(ctx)
				for _, op := range ops {
					if op.State != store.OpPending || op.Verify {
						t.Fatalf("entry should wait unchanged: %+v", op)
					}
				}
			}
			if len(ws.calls) != 0 {
				t.Fatalf("nothing can reach Asana without a token: %v", ws.calls)
			}
			s.API = good
			if res, err := s.Push(ctx); err != nil || res.Sent != 2 {
				t.Fatalf("push after a new login = %+v %v", res, err)
			}
			if n := ws.countCalls("POST /tasks "); n != 1 {
				t.Fatalf("the create should be sent once, without verifying first: %d\n%v", n, ws.calls)
			}
			assertOutboxEmpty(t, st)
		})
	}
}

// Push checks the account before sending. If that check cannot be made (Asana down,
// offline) nothing is sent and nothing fails; a failed check is never remembered as
// passed, and a passed one is not asked again by the same Syncer.
func TestPushAccountCheckFailureSendsNothing(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	_, _ = st.CreateTaskLocal(ctx, store.NewTask{ProjectGID: "p1", Name: "新しいタスク"}, now)
	_ = st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "B"}, now)
	meAnswer := func(status int, gid string) {
		ws.setHook(func(rw http.ResponseWriter, r *http.Request) bool {
			if r.URL.Path != "/users/me" {
				return false
			}
			if status != 0 {
				rw.WriteHeader(status)
				return true
			}
			fmt.Fprintf(rw, `{"data":{"gid":%q}}`, gid)
			return true
		})
	}
	sentNothing := func(step string) {
		t.Helper()
		if len(ws.calls) != 0 {
			t.Fatalf("%s: nothing may be sent before the account is confirmed: %v", step, ws.calls)
		}
		ops, _ := st.ListOps(ctx)
		for _, op := range ops {
			if op.State != store.OpPending || op.Verify || op.Attempts != 0 {
				t.Fatalf("%s: entry should wait unchanged: %+v", step, op)
			}
		}
	}

	meAnswer(http.StatusServiceUnavailable, "")
	res, err := s.Push(ctx)
	if err == nil || errors.Is(err, ErrAccountChanged) || res.Remaining != 2 || res.Failed != 0 {
		t.Fatalf("push while /users/me fails = %+v %v", res, err)
	}
	sentNothing("5xx")

	meAnswer(0, "someone-else")
	for range 2 { // not remembered: asked again and refused again
		if res, err := s.Push(ctx); !errors.Is(err, ErrAccountChanged) || res.Remaining != 2 {
			t.Fatalf("push as another account = %+v %v", res, err)
		}
		sentNothing("other account")
	}

	meAnswer(0, "me")
	if res, err := s.Push(ctx); err != nil || res.Sent != 2 {
		t.Fatalf("push as the right account = %+v %v", res, err)
	}
	asked := 0
	ws.setHook(func(rw http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/users/me" {
			asked++
		}
		return false
	})
	_ = st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "C"}, now)
	if res, err := s.Push(ctx); err != nil || res.Sent != 1 || asked != 0 {
		t.Fatalf("second push = %+v %v (asked /users/me %d times, want 0)", res, err, asked)
	}
}

// Before the account is confirmed, no token means nothing is sent at all.
func TestPushWithoutTokenStopsAtAccountCheck(t *testing.T) {
	ws, srv, s, st := setupPush(t)
	ctx := context.Background()
	_, _ = st.CreateTaskLocal(ctx, store.NewTask{ProjectGID: "p1", Name: "新しいタスク"}, now)
	bad := asana.NewClient(oauth2.NewClient(ctx, failingTokens{auth.ErrLoginReplaced}))
	bad.BaseURL, bad.MaxRetries = srv.URL, 0
	s.API = bad
	res, err := s.Push(ctx)
	if auth.LoginHelp(err) == "" || res.Remaining != 1 || res.Failed != 0 || len(ws.calls) != 0 {
		t.Fatalf("push = %+v %v (calls %v)", res, err, ws.calls)
	}
	if op := opByKind(t, st, store.OpCreateTask); op == nil || op.State != store.OpPending || op.Verify || op.Attempts != 0 {
		t.Fatalf("entry = %+v", op)
	}
}
