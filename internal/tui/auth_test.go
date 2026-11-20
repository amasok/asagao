package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/auth"
	"github.com/amasok/asagao/internal/sync"
)

// Not logged in, or a token Asana rejects: the TUI says how to log in, and the next sync
// builds the syncer again so a login made meanwhile in another terminal is picked up.
func TestSyncLoginProblemRebuildsSyncer(t *testing.T) {
	calls := 0
	nextErr := error(auth.ErrNotLoggedIn)
	m := New(context.Background(), Deps{Store: fixtureStore(t), Now: func() time.Time { return testNow },
		NewSyncer: func(context.Context) (*sync.Syncer, error) {
			calls++
			return nil, nextErr
		}})
	h := &harness{t: t, m: m}
	h.send(tea.WindowSizeMsg{Width: 220, Height: 30})
	h.run(m.Init())
	h.run(m.startSync())
	if calls == 0 {
		t.Fatal("NewSyncer should have been called")
	}
	if s := h.screen(); !strings.Contains(s, "未ログイン") || !strings.Contains(s, "auth login --pat") {
		t.Fatalf("not logged in should be shown with the PAT login:\n%s", s)
	}

	// A sync whose token is rejected (e.g. a revoked PAT) drops the syncer.
	built := &sync.Syncer{}
	hint := (&auth.Source{Method: "personal_access_token"}).UnauthorizedHint()
	m.syncer = built
	h.send(syncDoneMsg{syncer: built, err: fmt.Errorf("ユーザー情報の取得: %w", &asana.Error{Status: 401, Hint: hint}), at: testNow})
	if m.syncer != nil {
		t.Fatal("the syncer should be dropped after a 401")
	}
	if s := h.screen(); !strings.Contains(s, "ログインし直してください") || !strings.Contains(s, "auth login --pat") {
		t.Fatalf("a 401 should say how to log in again:\n%s", s)
	}
	before := calls
	h.run(m.startSync())
	if calls != before+1 {
		t.Fatalf("the next sync should build the syncer again (NewSyncer calls %d -> %d)", before, calls)
	}

	// Other errors (Asana down) keep the syncer: nothing about the login changed.
	m.syncer = built
	h.send(syncDoneMsg{syncer: built, err: &asana.Error{Status: 503}, at: testNow})
	if m.syncer != built {
		t.Fatal("a 5xx should keep the syncer")
	}
	if s := h.screen(); strings.Contains(s, "ログインし直して") || strings.Contains(s, "未ログイン") {
		t.Fatalf("a 5xx is not a login problem:\n%s", s)
	}
	h.send(syncDoneMsg{syncer: built, err: errors.New("boom"), at: testNow})
	if m.syncer != built {
		t.Fatal("an unrelated error should keep the syncer")
	}
}

// A login as someone else made in another terminal (auth.ErrLoginReplaced): the TUI syncs
// again at once with a new syncer, so the account check applies without waiting.
func TestLoginReplacedSyncsAgainAtOnce(t *testing.T) {
	calls := 0
	m := New(context.Background(), Deps{Store: fixtureStore(t), Now: func() time.Time { return testNow },
		NewSyncer: func(context.Context) (*sync.Syncer, error) {
			calls++
			return nil, errors.New("offline")
		}})
	h := &harness{t: t, m: m}
	h.send(tea.WindowSizeMsg{Width: 220, Height: 30})
	built := &sync.Syncer{}
	m.syncer = built
	m.syncState.running = true
	h.send(syncDoneMsg{syncer: built, err: fmt.Errorf("push: %w", auth.ErrLoginReplaced), at: testNow})
	if m.syncer == built {
		t.Fatal("the syncer should be dropped")
	}
	if calls != 1 {
		t.Fatalf("a new syncer should be built right away (NewSyncer called %d times)", calls)
	}
}
