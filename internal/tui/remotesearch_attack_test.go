package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/sync"
)

// A late answer for an older search does not replace what Asana found for the current one (#36).
func TestRemoteSearchLateAnswerKeepsTheNewer(t *testing.T) {
	h := newHarness(t, 160, 30)
	h.m.query = "B"
	h.run(h.m.remoteSearchDone(remoteSearchMsg{query: "B", res: readmodel.RemoteSearchResult{GIDs: []string{"b1"}}}))
	h.run(h.m.remoteSearchDone(remoteSearchMsg{query: "A", res: readmodel.RemoteSearchResult{GIDs: []string{"a1"}}}))
	if got := h.m.remote.foundFor("B"); len(got) != 1 || got[0] != "b1" {
		t.Fatalf("the current search's results were replaced by an old answer: %+v", h.m.remote)
	}
}

// Results of a search never show for another query, also after clearing and typing the same again.
func TestRemoteFoundOnlyForItsQuery(t *testing.T) {
	r := remoteFound{query: "見積", gids: []string{"t9"}}
	for _, q := range []string{"", "見", "見積 ", "見積もり", "ソファ"} {
		if r.foundFor(q) != nil {
			t.Errorf("found shown for %q", q)
		}
	}
	if (remoteFound{}).foundFor("") != nil {
		t.Error("empty remote matches the empty query")
	}
	if len(r.foundFor("見積")) != 1 {
		t.Error("own query lost")
	}
}

// No syncer: a name lookup is not a typed nil, searching says why; an error from making the syncer is shown clean.
func TestRemoteSearchWithoutSyncer(t *testing.T) {
	h := newHarness(t, 160, 30)
	if h.m.nameFinder() != nil {
		t.Fatal("nil syncer became a non-nil NameFinder")
	}
	h.m.deps.NewSyncer = func(context.Context) (*sync.Syncer, error) { return nil, errors.New("no token\x1b[2J") }
	h.m.query = "x"
	h.run(h.m.searchAsana())
	if s := h.m.status; strings.ContainsRune(s, 0x1b) || !strings.Contains(s, "Asana では探せませんでした") {
		t.Fatalf("status %q", s)
	}
	if h.m.syncer != nil {
		t.Fatal("a failed syncer was kept")
	}
}

// A search term with control characters is shown clean in the status line.
func TestRemoteSearchStatusIsTermsafe(t *testing.T) {
	h := newHarness(t, 160, 30)
	h.m.deps.NewSyncer = func(context.Context) (*sync.Syncer, error) { return nil, errors.New("x") }
	h.m.query = "a\x1b]0;x\x07b"
	_ = h.m.searchAsana()
	if strings.ContainsRune(h.m.status, 0x1b) || strings.ContainsRune(h.m.status, 7) {
		t.Fatalf("status %q", h.m.status)
	}
}
