package app

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/readmodel"
)

// pushStore maps local gids to what Asana created for them. ctxErr records whether a
// read was made with a context already done.
type pushStore struct {
	tasks, sections map[string]string
	entries         map[string]string
	waiting         map[string]bool
	fail            bool
	ctxErr          error
}

func (s *pushStore) note(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		s.ctxErr = err
	}
	if s.fail {
		return errors.New("store failed")
	}
	return nil
}

func (s *pushStore) RealGID(ctx context.Context, gid string) (string, error) {
	if err := s.note(ctx); err != nil {
		return "", err
	}
	if real, ok := s.tasks[gid]; ok {
		return real, nil
	}
	return gid, nil
}

func (s *pushStore) RealSectionGID(ctx context.Context, gid string) (string, error) {
	if err := s.note(ctx); err != nil {
		return "", err
	}
	if real, ok := s.sections[gid]; ok {
		return real, nil
	}
	return gid, nil
}

func (s *pushStore) LocalResult(ctx context.Context, kind readmodel.ResultKind, gid string) (string, bool, error) {
	if err := s.note(ctx); err != nil {
		return "", false, err
	}
	if kind != readmodel.ResultTimeEntry {
		return "", false, fmt.Errorf("kind %q", kind)
	}
	return s.entries[gid], s.waiting[gid], nil
}

// fakePusher returns res and err, and records the context it was given.
type fakePusher struct {
	res    readmodel.PushResult
	err    error
	called bool
	ctx    context.Context
	wait   bool // wait until the context is done, as a push to a slow Asana would
}

func (p *fakePusher) Push(ctx context.Context) (readmodel.PushResult, error) {
	p.called, p.ctx = true, ctx
	if p.wait {
		<-ctx.Done()
		return p.res, ctx.Err()
	}
	return p.res, p.err
}

func connectTo(p Pusher) func(context.Context) (Pusher, error) {
	return func(context.Context) (Pusher, error) { return p, nil }
}

var errLogin = errors.New("not logged in")

// loginHelp stands for auth.LoginHelp: errLogin (wrapped or not) means logging in.
func loginHelp(err error) string {
	if errors.Is(err, errLogin) {
		return "log in with asagao auth login"
	}
	return ""
}

func newPushNow(st PushStore) PushNow { return PushNow{Store: st, LoginHelp: loginHelp} }

// Each way a push can go is sorted out once, the same for the CLI and the MCP server.
func TestPushNowOutcome(t *testing.T) {
	offline := errors.New("dial tcp: no route to host")
	for _, c := range []struct {
		name    string
		res     readmodel.PushResult
		err     error
		want    PushOutcome
		sent    bool
		help    bool
		wantErr error
	}{
		{name: "everything sent", res: readmodel.PushResult{Sent: 1}, want: PushSent, sent: true},
		{name: "nothing queued", want: PushSent, sent: true},
		// The CLI looked at Remaining, the MCP server did not (#75): a change still queued
		// (offline, waiting for its create, to be checked in Asana) was not sent.
		{name: "entries left", res: readmodel.PushResult{Sent: 1, Remaining: 1}, want: PushIncomplete},
		{name: "conflict", res: readmodel.PushResult{Conflicts: 1}, want: PushIncomplete},
		{name: "failure", res: readmodel.PushResult{Failed: 1}, want: PushIncomplete},
		{name: "inbox read only", res: readmodel.PushResult{InboxReadOnly: true}, want: PushInboxReadOnly},
		{name: "inbox read only, then offline", res: readmodel.PushResult{InboxReadOnly: true}, err: offline, want: PushInboxReadOnly, wantErr: offline},
		{name: "another account", err: fmt.Errorf("%w (a / b)", readmodel.ErrAccountChanged), res: readmodel.PushResult{Remaining: 2}, want: PushAccountChanged, wantErr: readmodel.ErrAccountChanged},
		{name: "token rejected", err: fmt.Errorf("401: %w", errLogin), res: readmodel.PushResult{Remaining: 1}, want: PushLoginRejected, help: true, wantErr: errLogin},
		{name: "busy", err: readmodel.ErrPushBusy, res: readmodel.PushResult{Busy: true, Remaining: 1}, want: PushBusy, wantErr: readmodel.ErrPushBusy},
		{name: "offline", err: offline, res: readmodel.PushResult{Remaining: 1}, want: PushOffline, wantErr: offline},
		{name: "offline after sending some", err: offline, res: readmodel.PushResult{Sent: 2, Remaining: 1}, want: PushOffline, wantErr: offline},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := &fakePusher{res: c.res, err: c.err}
			rep := newPushNow(&pushStore{}).Run(context.Background(), connectTo(p), PushTarget{TaskGIDs: []string{"t1"}})
			if rep.Outcome != c.want || rep.Sent != c.sent {
				t.Fatalf("outcome %v sent %v; want %v sent %v (%+v)", rep.Outcome, rep.Sent, c.want, c.sent, rep)
			}
			if (rep.LoginHelp != "") != c.help {
				t.Errorf("login help %q; want one: %v", rep.LoginHelp, c.help)
			}
			if !errors.Is(rep.Err, c.wantErr) || (c.wantErr == nil) != (rep.Err == nil) {
				t.Errorf("err %v; want %v", rep.Err, c.wantErr)
			}
			if rep.Result == nil || rep.Result.Remaining != c.res.Remaining || rep.Result.Sent != c.res.Sent {
				t.Errorf("result %+v; want %+v", rep.Result, c.res)
			}
		})
	}
}

// Without a connection nothing is pushed: not logged in is told apart from the rest.
func TestPushNowNotConnected(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		want PushOutcome
		help string
	}{
		{"not logged in", fmt.Errorf("token: %w", errLogin), PushNotLoggedIn, "log in with asagao auth login"},
		{"offline mode", errors.New("offline mode"), PushNotConnected, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := &fakePusher{}
			connect := func(context.Context) (Pusher, error) { return p, c.err }
			rep := newPushNow(&pushStore{tasks: map[string]string{"local:1": "real1"}}).Run(context.Background(), connect, PushTarget{TaskGIDs: []string{"local:1"}})
			if rep.Outcome != c.want || rep.Sent || rep.LoginHelp != c.help || !errors.Is(rep.Err, c.err) || rep.Result != nil || p.called {
				t.Fatalf("report %+v (pushed: %v); want %v, help %q, err %v, no result", rep, p.called, c.want, c.help, c.err)
			}
			if rep.TaskGID() != "real1" {
				t.Errorf("task gid %q: the gid is read from the store even without a connection", rep.TaskGID())
			}
		})
	}
}

// No LoginHelp: no error is taken as a login problem (and nothing panics).
func TestPushNowWithoutLoginHelp(t *testing.T) {
	p := &fakePusher{err: errLogin}
	rep := PushNow{Store: &pushStore{}}.Run(context.Background(), connectTo(p), PushTarget{})
	if rep.Outcome != PushOffline || rep.LoginHelp != "" {
		t.Fatalf("report %+v; want offline without help", rep)
	}
}

// The gids of what the write made offline are given as Asana's once it created them.
func TestPushNowResolvesGIDs(t *testing.T) {
	st := &pushStore{
		tasks:    map[string]string{"local:t1": "real-t1"},
		sections: map[string]string{"local:s1": "real-s1"},
		entries:  map[string]string{"local:e1": "real-e1", "local:e2": "real-e2"},
		waiting:  map[string]bool{"local:e2": true},
	}
	run := func(t PushTarget) PushReport {
		return newPushNow(st).Run(context.Background(), connectTo(&fakePusher{}), t)
	}
	rep := run(PushTarget{TaskGIDs: []string{"local:t1", "t2", "local:t3"}, SectionGID: "local:s1", TimeEntryGID: "local:e1"})
	if got := fmt.Sprint(rep.TaskGIDs); got != "[real-t1 t2 local:t3]" {
		t.Errorf("tasks %s; want the created one real, the others as given", got)
	}
	if rep.TaskGID() != "real-t1" || rep.SectionGID != "real-s1" || rep.TimeEntryGID != "real-e1" {
		t.Errorf("report %+v", rep)
	}
	// A time entry whose create is still queued keeps its local gid (a change to it waits
	// for the create), and one not created yet too.
	if rep := run(PushTarget{TimeEntryGID: "local:e2"}); rep.TimeEntryGID != "local:e2" {
		t.Errorf("waiting time entry %q; want the local gid", rep.TimeEntryGID)
	}
	if rep := run(PushTarget{TimeEntryGID: "local:e9", SectionGID: "local:s9"}); rep.TimeEntryGID != "local:e9" || rep.SectionGID != "local:s9" {
		t.Errorf("not created: %+v", rep)
	}
	// Real gids are given as they are, and empty task gids are left out.
	if rep := run(PushTarget{TaskGIDs: []string{"", "t5", ""}, SectionGID: "s5", TimeEntryGID: "e5"}); fmt.Sprint(rep.TaskGIDs) != "[t5]" || rep.SectionGID != "s5" || rep.TimeEntryGID != "e5" {
		t.Errorf("real gids: %+v", rep)
	}
	if rep := run(PushTarget{}); rep.TaskGID() != "" || len(rep.TaskGIDs) != 0 {
		t.Errorf("no task: %+v", rep)
	}
	// The store failing keeps every gid as given.
	st.fail = true
	if rep := run(PushTarget{TaskGIDs: []string{"local:t1"}, SectionGID: "local:s1", TimeEntryGID: "local:e1"}); rep.TaskGID() != "local:t1" || rep.SectionGID != "local:s1" || rep.TimeEntryGID != "local:e1" {
		t.Errorf("store failing: %+v", rep)
	}
}

// A change discarded because its task was deleted in Asana is not reported as sent.
func TestPushNowDropped(t *testing.T) {
	st := &pushStore{tasks: map[string]string{"local:new": "real-new"}}
	for _, c := range []struct {
		name    string
		target  []string
		discard string
		dropped bool
	}{
		{"the task changed", []string{"t1"}, "t1", true},
		{"one of the tasks changed", []string{"t1", "t2"}, "t2", true},
		{"another task", []string{"t1"}, "t9", false},
		// The create of a task made offline was dropped (e.g. its project was deleted).
		{"the create", []string{"local:new"}, "local:new", true},
		// A follow-up of a create that reached Asana (its move into a deleted section):
		// the task exists, the discard is reported on its own.
		{"a follow-up of the create", []string{"local:new"}, "real-new", false},
		// A write naming no task can not be told from another such change.
		{"no task, a change without a task", nil, "", true},
		{"no task, a change to a task", nil, "t1", false},
		{"a task, a change without a task", []string{"t1"}, "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := &fakePusher{res: readmodel.PushResult{Sent: 1, Discarded: []readmodel.Discard{{ID: 1, TaskGID: c.discard}}}}
			rep := newPushNow(st).Run(context.Background(), connectTo(p), PushTarget{TaskGIDs: c.target})
			if rep.Dropped != c.dropped || rep.Sent == c.dropped || rep.Outcome != PushSent {
				t.Fatalf("dropped %v sent %v outcome %v; want dropped %v", rep.Dropped, rep.Sent, rep.Outcome, c.dropped)
			}
		})
	}
}

// Drop also takes the discards an entry point shows that this push did not make (an
// earlier push's, or another process's while this one was busy).
func TestPushNowDropLater(t *testing.T) {
	p := &fakePusher{err: readmodel.ErrPushBusy, res: readmodel.PushResult{Busy: true}}
	rep := newPushNow(&pushStore{}).Run(context.Background(), connectTo(p), PushTarget{TaskGIDs: []string{"t1"}})
	if rep.Dropped || rep.Sent {
		t.Fatalf("before: %+v", rep)
	}
	rep.Drop([]readmodel.Discard{{TaskGID: "t9"}})
	if rep.Dropped {
		t.Fatal("another task's discard drops nothing")
	}
	rep.Drop([]readmodel.Discard{{TaskGID: "t9"}, {TaskGID: "t1"}})
	if !rep.Dropped || rep.Sent || rep.Outcome != PushBusy {
		t.Fatalf("after: %+v", rep)
	}
}

// Connecting and pushing are bounded by the timeout; the gids are still read from the
// store afterwards, with a context that is not done.
func TestPushNowTimeout(t *testing.T) {
	st := &pushStore{tasks: map[string]string{"local:1": "real1"}}
	p := &fakePusher{wait: true}
	var connectCtx context.Context
	connect := func(ctx context.Context) (Pusher, error) { connectCtx = ctx; return p, nil }
	start := time.Now()
	rep := PushNow{Store: st, LoginHelp: loginHelp, Timeout: 20 * time.Millisecond}.Run(context.Background(), connect, PushTarget{TaskGIDs: []string{"local:1"}})
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("Run took %v", d)
	}
	if rep.Outcome != PushOffline || !errors.Is(rep.Err, context.DeadlineExceeded) || rep.Sent {
		t.Fatalf("report %+v; want offline with the deadline", rep)
	}
	if _, ok := connectCtx.Deadline(); !ok {
		t.Error("connect was not given the timeout")
	}
	if st.ctxErr != nil || rep.TaskGID() != "real1" {
		t.Errorf("store read with a done context (%v), gid %q", st.ctxErr, rep.TaskGID())
	}
}

// Without a timeout set, DefaultPushTimeout bounds the push.
func TestPushNowDefaultTimeout(t *testing.T) {
	p := &fakePusher{}
	newPushNow(&pushStore{}).Run(context.Background(), connectTo(p), PushTarget{})
	dl, ok := p.ctx.Deadline()
	if left := time.Until(dl); !ok || left > DefaultPushTimeout || left < DefaultPushTimeout-5*time.Second {
		t.Fatalf("deadline in %v (set: %v); want about %v", left, ok, DefaultPushTimeout)
	}
}
