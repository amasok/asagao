package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/termsafe"
)

// Saved searches (Issue #10): space / picks one of searches.toml, and the list shows what
// it finds (across every project, like a search). The filter (f), tag (T), field (F) and
// search (/) narrow it further. Moving in the sidebar, or choosing 解除, leaves it.

// SearchFile is where the saved searches are read (app.SearchFile; config.SearchFile).
type SearchFile = app.SearchFile

// clearSavedSearch is the picker item that leaves the saved search.
const clearSavedSearch = "\x00clear"

type savedSearchesLoadedMsg struct {
	searches []readmodel.SavedSearch
	err      error
}

func (m *Model) searches() app.Searches { return app.Searches{File: m.deps.Searches} }

func (m *Model) openSavedSearchPicker() tea.Cmd {
	s := m.searches()
	return func() tea.Msg {
		ss, err := s.List()
		return savedSearchesLoadedMsg{searches: ss, err: err}
	}
}

func (m *Model) updateSearches(msg tea.Msg) (tea.Cmd, bool) {
	loaded, ok := msg.(savedSearchesLoadedMsg)
	if !ok {
		return nil, false
	}
	if loaded.err != nil {
		m.status = "保存した検索を読み込めませんでした: " + termsafe.String(loaded.err.Error())
		return nil, true
	}
	m.showSavedSearchPicker(loaded.searches)
	return nil, true
}

func (m *Model) showSavedSearchPicker(ss []readmodel.SavedSearch) {
	if len(ss) == 0 && m.list.saved == nil {
		m.status = "保存した検索はありません（asagao searches save <名前> で保存）"
		return
	}
	p := &picker{kind: pickSavedSearch, title: "保存した検索"}
	if m.list.saved != nil {
		p.items = append(p.items, pickerItem{label: "（解除）", value: clearSavedSearch, hint: termsafe.String(m.list.saved.Name)})
	}
	for _, s := range ss {
		item := pickerItem{label: termsafe.String(s.Name), value: s.Name, hint: termsafe.String(strings.Join(s.Conditions(), " "))}
		if m.list.saved != nil && m.list.saved.Name == s.Name {
			item.label += "（現在）"
			p.cursor = len(p.items)
		}
		p.items = append(p.items, item)
	}
	m.picker = p
}

// applySavedSearch shows the saved search named name (as it is in the file now), or leaves
// the saved search for clearSavedSearch. The other narrowings start over.
func (m *Model) applySavedSearch(name string) tea.Cmd {
	m.list.cursor, m.list.offset = 0, 0
	m.board.col, m.board.row, m.board.colOffset = 0, 0, 0
	if name == clearSavedSearch {
		m.list.saved = nil
		return m.loadTasks()
	}
	x, err := m.searches().Get(name)
	if err != nil {
		m.status = termsafe.String(err.Error())
		return nil
	}
	m.list.saved = &x
	m.list.filter, m.list.tagFilter, m.list.fieldFilter = filterOpen, "", fieldFilter{}
	m.search.query = ""
	m.search.input.SetValue("")
	return m.loadTasks()
}

// savedRows loads what the saved search finds, with the list's own narrowings on top: the
// filter (other than 未完了, which leaves the search's completion as it is), the tag, the
// field and the search words (which replace the search's).
func savedRows(ctx context.Context, st *app.UseCases, x readmodel.SavedSearch, query string, filter filterMode, tag string, field fieldFilter, now time.Time) ([]row, error) {
	f, err := st.Query().SavedSearchFilter(ctx, x.Name, x.Spec(), now)
	if err != nil {
		return nil, err
	}
	if query != "" {
		f.Query = query
	}
	if tag != "" {
		f.Tag = tag
	}
	if field.field != "" {
		f.Field, f.FieldValue = field.field, field.value
	}
	switch filter {
	case filterDueWeek:
		f.DueBefore = now.AddDate(0, 0, 7).Format(time.DateOnly)
		f.Completed = boolPtr(false)
	case filterOverdue:
		f.DueBefore = now.AddDate(0, 0, -1).Format(time.DateOnly)
		f.Completed = boolPtr(false)
	case filterCompleted:
		f.Completed = boolPtr(true)
	case filterAll:
		f.Completed = nil
	}
	tasks, err := st.Query().ListTasks(ctx, f)
	if err != nil {
		return nil, err
	}
	label := fmt.Sprintf("保存した検索「%s」(%d)", termsafe.String(x.Name), len(tasks))
	return group(tasks, []string{label}, func(readmodel.TaskSummary) int { return 0 }), nil
}

// savedSearchLabel is the header's label of the saved search shown ("" for none).
func (m *Model) savedSearchLabel() string {
	if m.list.saved == nil {
		return ""
	}
	return "保存: " + termsafe.String(m.list.saved.Name)
}
