package tui

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/sync"
	"github.com/amasok/asagao/internal/termsafe"
)

// Searching Asana as well as the cache (Issue #36): space g, with a search typed. What
// Asana finds is cached and listed after the cache's matches, as long as the search stays
// the same.

// remoteFound is what Asana found for one search.
type remoteFound struct {
	query  string
	gids   []string
	byName bool // typeahead: the workspace's plan lacks the search API
}

// remoteSearchMsg is the answer of a search in Asana.
type remoteSearchMsg struct {
	query  string
	res    readmodel.RemoteSearchResult
	err    error
	syncer *sync.Syncer // made for this search (nil: the model's)
}

// remoteTimeout bounds a search in Asana, and nameTimeout a name lookup (a quick add waits
// for it).
const (
	remoteTimeout = 30 * time.Second
	nameTimeout   = 10 * time.Second
)

// searchCompleted is which tasks a search lists with the filter: like the cache's search
// (queryRows), completed ones too unless the filter says otherwise.
func searchCompleted(filter filterMode) *bool {
	switch filter {
	case filterOpen, filterAll:
		return nil
	case filterCompleted:
		return boolPtr(true)
	}
	return boolPtr(false)
}

// searchAsana searches Asana for the current search.
func (m *Model) searchAsana() tea.Cmd {
	if m.query == "" {
		m.status = "先に / で検索する語を入れてください（Asana でも探します）"
		return nil
	}
	if m.syncer == nil && m.deps.NewSyncer == nil {
		m.status = "オフラインのため Asana では探せません"
		return nil
	}
	query, completed := m.query, searchCompleted(m.filter)
	syncer, newSyncer := m.syncer, m.deps.NewSyncer
	m.status = "Asana で「" + termsafe.String(query) + "」を探しています…"
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, remoteTimeout)
		defer cancel()
		msg := remoteSearchMsg{query: query}
		if syncer == nil {
			s, err := newSyncer(ctx)
			if err != nil {
				msg.err = err
				return msg
			}
			syncer, msg.syncer = s, s
		}
		msg.res, msg.err = syncer.SearchTasks(ctx, query, completed, app.RemoteSearchLimit)
		return msg
	}
}

func (m *Model) remoteSearchDone(msg remoteSearchMsg) tea.Cmd {
	if msg.syncer != nil && m.syncer == nil {
		m.syncer = msg.syncer
	}
	if msg.err != nil {
		m.status = "Asana では探せませんでした: " + termsafe.String(msg.err.Error())
		return nil
	}
	m.remote = remoteFound{query: msg.query, gids: msg.res.GIDs, byName: msg.res.ByName}
	m.status = fmt.Sprintf("Asana で %d 件見つかりました", len(msg.res.GIDs))
	if msg.res.ByName {
		m.status += "（プランに検索 API がないため、タスク名の先頭で探しました）"
	}
	if msg.query != m.query {
		return nil // the search changed meanwhile: shown when it is searched again
	}
	return m.loadTasks()
}

// foundFor returns what Asana found for query (nil when it was not searched for it).
func (r remoteFound) foundFor(query string) []string {
	if query == "" || r.query != query {
		return nil
	}
	return r.gids
}

// nameFinder looks names the cache lacks up in Asana for quick add (nil: offline).
func (m *Model) nameFinder() app.NameFinder {
	if m.syncer == nil {
		return nil // a nil *sync.Syncer must not become a non-nil NameFinder
	}
	return timedFinder{m.syncer}
}

// timedFinder bounds each lookup with nameTimeout.
type timedFinder struct{ s *sync.Syncer }

func (f timedFinder) FindUsers(ctx context.Context, q string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, nameTimeout)
	defer cancel()
	return f.s.FindUsers(ctx, q)
}

func (f timedFinder) FindTags(ctx context.Context, q string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, nameTimeout)
	defer cancel()
	return f.s.FindTags(ctx, q)
}
