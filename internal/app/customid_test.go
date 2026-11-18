package app

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"testing"

	"github.com/amasok/asagao/internal/readmodel"
)

// Commits that name a task by its custom ID, "asana#ENG-123" (Issue #36).

func TestGitLinkByCustomID(t *testing.T) {
	st := newGitStore(gitTaskA)
	link := gitLink(st)
	link.CustomIDs = map[string]string{"ENG-1": gitTaskA}
	acts, err := link.Apply(context.Background(), GitCommit{SHA: gitSHA, Message: "fixes asana#ENG-1, see asana#ENG-9"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(acts) != 2 {
		t.Fatalf("acts %+v", acts)
	}
	if a := acts[0]; a.TaskGID != gitTaskA || a.CustomID != "ENG-1" || a.Result != GitLinkCommented || !a.Completed {
		t.Fatalf("ENG-1: %+v", a)
	}
	if a := acts[1]; a.TaskGID != "" || a.CustomID != "ENG-9" || a.Result != GitLinkNotFound {
		t.Fatalf("ENG-9: %+v", a)
	}
	if !st.tasks[gitTaskA].Completed {
		t.Fatal("not completed")
	}
}

type customIDState map[string]string

func (s customIDState) SyncState(_ context.Context, key string) (string, error) { return s[key], nil }

type fakeCustomIDs struct {
	gids  map[string]string
	err   error // for the IDs not in gids
	calls []string
}

func (f *fakeCustomIDs) TaskByCustomID(_ context.Context, id string) (string, error) {
	f.calls = append(f.calls, id)
	if gid, ok := f.gids[id]; ok {
		return gid, nil
	}
	if f.err != nil {
		return "", f.err
	}
	return "", fmt.Errorf("タスクの ID %q: %w", id, readmodel.ErrNotFound)
}

func TestResolveCustomIDs(t *testing.T) {
	ctx := context.Background()
	remembered := customIDState{readmodel.KeyCustomID + "ENG-2": "t2", readmodel.KeyCustomID + "ENG-3": "t3-old"}
	ids := []string{"ENG-1", "ENG-2", "ENG-3"}

	// online: Asana's answer; an ID it does not know is left out even if remembered
	f := &fakeCustomIDs{gids: map[string]string{"ENG-1": "t1", "ENG-2": "t2"}}
	got, err := ResolveCustomIDs(ctx, remembered, f, ids)
	if err != nil || !maps.Equal(got, map[string]string{"ENG-1": "t1", "ENG-2": "t2"}) || !slices.Equal(f.calls, ids) {
		t.Fatalf("online: %v %v calls %v", got, err, f.calls)
	}
	// Asana cannot be reached: what was found before, and the error for a warning
	offline := errors.New("dial tcp: offline")
	got, err = ResolveCustomIDs(ctx, remembered, &fakeCustomIDs{err: offline}, ids)
	if !errors.Is(err, offline) || !maps.Equal(got, map[string]string{"ENG-2": "t2", "ENG-3": "t3-old"}) {
		t.Fatalf("offline: %v %v", got, err)
	}
	// no finder (the hook without time, --offline): the same, without an error
	got, err = ResolveCustomIDs(ctx, remembered, nil, ids)
	if err != nil || len(got) != 2 {
		t.Fatalf("no finder: %v %v", got, err)
	}
}

func TestCustomIDsOfCommits(t *testing.T) {
	commits := []GitCommit{
		{Message: "asana#ENG-1 #" + gitTaskA},
		{Message: "fixes asana#ENG-1 asana#ENG-2"},
	}
	if got := CustomIDs(commits); !slices.Equal(got, []string{"ENG-1", "ENG-2"}) {
		t.Fatalf("got %v", got)
	}
}

// After Asana fails once (offline, out of time), the other IDs are not asked for: they are
// resolved as found before.
func TestResolveCustomIDsStopsAskingAfterAFailure(t *testing.T) {
	ctx := context.Background()
	remembered := customIDState{readmodel.KeyCustomID + "ENG-1": "t1", readmodel.KeyCustomID + "ENG-2": "t2"}
	f := &fakeCustomIDs{err: context.DeadlineExceeded}
	got, err := ResolveCustomIDs(ctx, remembered, f, []string{"ENG-1", "ENG-2"})
	if !errors.Is(err, context.DeadlineExceeded) || !maps.Equal(got, map[string]string{"ENG-1": "t1", "ENG-2": "t2"}) {
		t.Fatalf("got %v %v", got, err)
	}
	if !slices.Equal(f.calls, []string{"ENG-1"}) {
		t.Fatalf("asked %v", f.calls)
	}
}
