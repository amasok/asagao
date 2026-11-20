package sync

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func meCount(log []string) int {
	n := 0
	for _, l := range log {
		if l == "GET /users/me" {
			n++
		}
	}
	return n
}

// #71 (tester): once Run has confirmed the account, Push, OpenProject and FetchCompleted of
// the same Syncer do not ask /users/me again (accountOK is shared by the parts it makes).
func TestPartsShareConfirmedAccount(t *testing.T) {
	e := setupLazy(t, lazyCfg)
	ctx := context.Background()
	e.run(t) // first sync: records the account
	e.run(t) // now there is an account to compare with: Run confirms it
	e.fake.reset()
	if _, err := e.s.Push(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.OpenProject(ctx, "p3", true); err != nil {
		t.Fatal(err)
	}
	e.s.DeferCompleted = true
	e.now = e.now.Add(time.Minute)
	if _, err := e.s.Run(ctx, false); err != nil { // one /users/me for Run itself
		t.Fatal(err)
	}
	n := meCount(e.fake.requests())
	if n != 1 {
		t.Fatalf("Run asked /users/me %d times, want 1", n)
	}
	e.fake.reset()
	if _, err := e.s.FetchCompleted(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Push(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.OpenProject(ctx, "p1", true); err != nil {
		t.Fatal(err)
	}
	if n := meCount(e.fake.requests()); n != 0 {
		t.Fatalf("Push/OpenProject/FetchCompleted asked /users/me %d times after Run confirmed", n)
	}
}

// A fresh Syncer has confirmed nothing; parts made by it ask once, then never again; a
// second Syncer is independent.
func TestPartsConfirmOncePerSyncer(t *testing.T) {
	e := setupLazy(t, lazyCfg)
	ctx := context.Background()
	e.run(t)
	fresh := &Syncer{API: e.api, Store: e.st, Cfg: e.s.Cfg}
	e.fake.reset()
	for range 3 {
		if _, err := fresh.Push(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := meCount(e.fake.requests()); n != 1 {
		t.Fatalf("3 pushes asked /users/me %d times, want 1", n)
	}
	e.fake.reset()
	other := &Syncer{API: e.api, Store: e.st, Cfg: e.s.Cfg}
	if _, err := other.Push(ctx); err != nil {
		t.Fatal(err)
	}
	if n := meCount(e.fake.requests()); n != 1 {
		t.Fatalf("another Syncer asked /users/me %d times, want 1 (not shared)", n)
	}
}

// Parts built by hand (accountOK nil) do not panic: they ask each time.
func TestPartsBuiltDirectlyDoNotPanic(t *testing.T) {
	e := setupLazy(t, lazyCfg)
	ctx := context.Background()
	e.run(t)
	e.run(t)
	p := &Pusher{API: e.api, Store: e.st, Cfg: e.s.Cfg}
	pl := &Puller{API: e.api, Store: e.st, Cfg: e.s.Cfg, Pusher: p}
	e.fake.reset()
	for range 2 {
		if _, err := p.Push(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := meCount(e.fake.requests()); n != 2 {
		t.Fatalf("direct Pusher asked %d times, want 2", n)
	}
	e.now = e.now.Add(time.Minute)
	if _, err := pl.Run(ctx, false); err != nil {
		t.Fatal(err)
	}
	if _, err := pl.OpenProject(ctx, "p1", true); err != nil {
		t.Fatal(err)
	}
	if _, err := pl.FetchCompleted(ctx); err != nil {
		t.Fatal(err)
	}
	if !p.accountConfirmed() == false { // nil accountOK never reads as confirmed
		t.Fatal("a Pusher without accountOK must not claim to be confirmed")
	}
}

// Fields changed after the Syncer was made apply to the next call.
func TestSyncerFieldsChangedLaterApply(t *testing.T) {
	e := setupLazy(t, lazyCfg)
	ctx := context.Background()
	e.run(t)
	if _, err := e.s.Push(ctx); err != nil {
		t.Fatal(err)
	}
	e.s.PullOnly = true
	if _, err := e.s.Push(ctx); !errors.Is(err, ErrPullOnly) {
		t.Fatalf("Push after PullOnly = %v, want ErrPullOnly", err)
	}
	e.s.PullOnly = false
	if _, err := e.s.Push(ctx); err != nil {
		t.Fatalf("Push after PullOnly off: %v", err)
	}

	var msgs []string
	e.s.Progress = func(f string, a ...any) { msgs = append(msgs, f) }
	if _, err := e.s.OpenProject(ctx, "p3", false); err != nil {
		t.Fatal(err)
	}
	if len(msgs) == 0 {
		t.Fatal("Progress set after construction did not reach the Puller")
	}

	e.s.DeferCompleted = true
	e.s.Cfg.CompletedDays = 14
	if _, err := e.s.FetchCompleted(ctx); err != nil {
		t.Fatal(err)
	}
}

// PullOnly reaches the Puller too: Run leaves the outbox alone.
func TestSyncerPullOnlyReachesRun(t *testing.T) {
	e := setupLazy(t, lazyCfg)
	ctx := context.Background()
	e.s.PullOnly = true
	e.run(t)
	for _, l := range e.fake.requests() {
		if strings.HasPrefix(l, "POST ") || strings.HasPrefix(l, "PUT ") || strings.HasPrefix(l, "DELETE ") {
			t.Fatalf("PullOnly Run wrote: %s", l)
		}
	}
	if _, err := e.s.Push(ctx); !errors.Is(err, ErrPullOnly) {
		t.Fatalf("Push = %v", err)
	}
}

// DeferCompleted set on the Syncer reaches the Puller (also when set after a first call).
func TestSyncerDeferCompletedReachesPuller(t *testing.T) {
	e := setupLazy(t, lazyCfg)
	e.s.DeferCompleted = true
	if res := e.run(t); !res.CompletedPending {
		t.Fatal("CompletedPending not set")
	}
}
