package app

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/readmodel"
)

// Several conditions at once: the order of classify decides.
func TestPushNowAttackOverlappingConditions(t *testing.T) {
	acct := fmt.Errorf("%w (a / b)", readmodel.ErrAccountChanged)
	both := fmt.Errorf("%w: %w", readmodel.ErrAccountChanged, errLogin)
	busyLogin := fmt.Errorf("%w: %w", readmodel.ErrPushBusy, errLogin)
	for _, c := range []struct {
		name string
		res  readmodel.PushResult
		err  error
		want PushOutcome
	}{
		{"inbox read only beats account changed", readmodel.PushResult{InboxReadOnly: true}, acct, PushInboxReadOnly},
		{"inbox read only beats login", readmodel.PushResult{InboxReadOnly: true}, errLogin, PushInboxReadOnly},
		{"inbox read only beats conflicts", readmodel.PushResult{InboxReadOnly: true, Conflicts: 1, Failed: 1}, nil, PushInboxReadOnly},
		{"account changed beats login", readmodel.PushResult{}, both, PushAccountChanged},
		{"login beats busy", readmodel.PushResult{Busy: true}, busyLogin, PushLoginRejected},
		{"account changed beats conflicts", readmodel.PushResult{Conflicts: 3}, acct, PushAccountChanged},
		{"busy beats remaining", readmodel.PushResult{Remaining: 4}, readmodel.ErrPushBusy, PushBusy},
		{"error beats a clean count", readmodel.PushResult{}, errors.New("boom"), PushOffline},
	} {
		t.Run(c.name, func(t *testing.T) {
			rep := newPushNow(&pushStore{}).Run(context.Background(), connectTo(&fakePusher{res: c.res, err: c.err}), PushTarget{TaskGIDs: []string{"t1"}})
			if rep.Outcome != c.want || rep.Sent {
				t.Fatalf("outcome %v sent %v; want %v", rep.Outcome, rep.Sent, c.want)
			}
		})
	}
}

// A discard of another write's task never counts, whatever the target holds; empty
// gids in the target are dropped and do not turn it into "no task".
func TestPushNowAttackDropMatchesOnlyOwnGIDs(t *testing.T) {
	disc := func(g ...string) []readmodel.Discard {
		var ds []readmodel.Discard
		for i, x := range g {
			ds = append(ds, readmodel.Discard{ID: int64(i + 1), TaskGID: x})
		}
		return ds
	}
	for _, c := range []struct {
		name    string
		target  []string
		ds      []readmodel.Discard
		dropped bool
	}{
		{"only empty gids given: acts as no task", []string{"", ""}, disc(""), true},
		{"only empty gids given: a task's discard is not mine", []string{""}, disc("t1"), false},
		{"empty plus a task: a task-less discard is not mine", []string{"", "t1"}, disc(""), false},
		{"prefix of a gid is not a match", []string{"t1"}, disc("t10", "t"), false},
		{"many discards, mine last", []string{"t1"}, disc("a", "b", "c", "t1"), true},
		{"no discards", []string{"t1"}, nil, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := &fakePusher{res: readmodel.PushResult{Sent: 1, Discarded: c.ds}}
			rep := newPushNow(&pushStore{}).Run(context.Background(), connectTo(p), PushTarget{TaskGIDs: c.target})
			if rep.Dropped != c.dropped || rep.Sent == c.dropped {
				t.Fatalf("dropped %v sent %v; want dropped %v", rep.Dropped, rep.Sent, c.dropped)
			}
		})
	}
}

// Run does not write into the caller's slice, and a failing store leaves local gids.
func TestPushNowAttackDoesNotMutateInput(t *testing.T) {
	in := []string{"local:1", "", "t2"}
	st := &pushStore{tasks: map[string]string{"local:1": "real1"}}
	rep := newPushNow(st).Run(context.Background(), connectTo(&fakePusher{}), PushTarget{TaskGIDs: in})
	if in[0] != "local:1" || in[1] != "" || in[2] != "t2" {
		t.Fatalf("input changed: %v", in)
	}
	if len(rep.TaskGIDs) != 2 || rep.TaskGIDs[0] != "real1" || rep.TaskGIDs[1] != "t2" {
		t.Fatalf("gids %v", rep.TaskGIDs)
	}
	st = &pushStore{fail: true, tasks: map[string]string{"local:1": "real1"}}
	rep = newPushNow(st).Run(context.Background(), connectTo(&fakePusher{}), PushTarget{TaskGIDs: []string{"local:1"}, SectionGID: "local:s", TimeEntryGID: "local:e"})
	if rep.TaskGID() != "local:1" || rep.SectionGID != "local:s" || rep.TimeEntryGID != "local:e" {
		t.Fatalf("store failure must leave gids as given: %+v", rep)
	}
}

// A time entry still waiting for its create is not given a gid; a real gid is untouched.
func TestPushNowAttackTimeEntryWaitingAndRealGIDs(t *testing.T) {
	st := &pushStore{entries: map[string]string{"local:e": "real-e"}, waiting: map[string]bool{"local:e": true}}
	rep := newPushNow(st).Run(context.Background(), connectTo(&fakePusher{}), PushTarget{TimeEntryGID: "local:e", SectionGID: "123", TaskGIDs: []string{"456"}})
	if rep.TimeEntryGID != "local:e" || rep.SectionGID != "123" || rep.TaskGID() != "456" {
		t.Fatalf("%+v", rep)
	}
	if rep.TaskGID() == "" || (PushReport{}).TaskGID() != "" {
		t.Fatal("TaskGID of an empty report must be empty")
	}
}

// A caller whose context is already canceled: connect sees it, nothing panics, and the
// connect error is not mistaken for a login problem.
func TestPushNowAttackCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	connect := func(c context.Context) (Pusher, error) { return nil, c.Err() }
	rep := newPushNow(&pushStore{}).Run(ctx, connect, PushTarget{TaskGIDs: []string{"t1"}})
	if rep.Outcome != PushNotConnected || !errors.Is(rep.Err, context.Canceled) || rep.Result != nil || rep.Sent || rep.LoginHelp != "" {
		t.Fatalf("%+v", rep)
	}
}

// Canceling the caller's context while pushing stops the push.
func TestPushNowAttackCancelWhilePushing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := &fakePusher{wait: true}
	connect := func(context.Context) (Pusher, error) {
		time.AfterFunc(10*time.Millisecond, cancel)
		return p, nil
	}
	rep := newPushNow(&pushStore{}).Run(ctx, connect, PushTarget{})
	if rep.Outcome != PushOffline || !errors.Is(rep.Err, context.Canceled) {
		t.Fatalf("%+v", rep)
	}
}
