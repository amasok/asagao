package tui

import (
	"context"
	"encoding/json"
	"github.com/amasok/asagao/internal/app"
	"slices"

	tea "charm.land/bubbletea/v2"
)

// Collapsible groups in the list view. The collapsed groups of each scope (project or
// My Tasks) are remembered in the local store, so they stay collapsed after a restart.

func (m *Model) scopeKey() string {
	if sc := m.currentScope(); sc.kind == scopeProject {
		return sc.project
	} else if sc.kind == scopeInbox {
		return "inbox"
	}
	return "my"
}

// loadCollapsed reads the collapsed group keys of a scope (in a command goroutine).
func loadCollapsed(ctx context.Context, st *app.UseCases, scope string) map[string]bool {
	out := map[string]bool{}
	v, err := st.ViewState().CollapsedGroups(ctx, scope)
	if err != nil || v == "" {
		return out
	}
	var keys []string
	if json.Unmarshal([]byte(v), &keys) == nil {
		for _, k := range keys {
			out[k] = true
		}
	}
	return out
}

func (m *Model) saveCollapsed() tea.Cmd {
	keys := make([]string, 0, len(m.list.collapsed))
	for k := range m.list.collapsed {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	b, _ := json.Marshal(keys)
	st, scope := m.deps.App, m.scopeKey()
	return func() tea.Msg {
		_ = st.ViewState().SetCollapsedGroups(m.ctx, scope, string(b))
		return nil
	}
}

// visibleRows drops the tasks of collapsed groups (search results are never collapsed).
func (m *Model) visibleRows() []row {
	if m.search.query != "" || len(m.list.collapsed) == 0 {
		out := make([]row, len(m.list.all))
		copy(out, m.list.all)
		for i := range out {
			out[i].collapsed = false
		}
		return out
	}
	out := make([]row, 0, len(m.list.all))
	hiding := false
	for _, r := range m.list.all {
		if r.isHeader() {
			r.collapsed = m.list.collapsed[r.key] && !r.note
			hiding = r.collapsed
			out = append(out, r)
			continue
		}
		if !hiding {
			out = append(out, r)
		}
	}
	return out
}

// groupAt returns the index of the group header for the row at i.
func (m *Model) groupAt(i int) int {
	for ; i >= 0; i-- {
		if i < len(m.list.rows) && m.list.rows[i].isHeader() && !m.list.rows[i].note {
			return i
		}
	}
	return -1
}

// toggleGroup collapses the group under the cursor, or expands it if it is collapsed.
func (m *Model) toggleGroup() tea.Cmd {
	h := m.groupAt(m.list.cursor)
	if h < 0 || m.search.query != "" {
		return nil
	}
	key := m.list.rows[h].key
	if m.list.collapsed == nil {
		m.list.collapsed = map[string]bool{}
	}
	if m.list.collapsed[key] {
		delete(m.list.collapsed, key)
		m.showRows()
		m.list.cursor = m.nextTaskRow(h+1, 1)
		if m.list.cursor < 0 || m.groupAt(m.list.cursor) != h {
			m.list.cursor = h
		}
	} else {
		m.list.collapsed[key] = true
		m.showRows()
		m.list.cursor = h
	}
	m.ensureListVisible()
	cmds := []tea.Cmd{m.saveCollapsed()}
	if t := m.selectedTask(); t != nil {
		cmds = append(cmds, m.loadDetail(t.GID))
	}
	return tea.Batch(cmds...)
}

// setAllGroups collapses (true) or expands (false) every group.
func (m *Model) setAllGroups(collapse bool) tea.Cmd {
	if m.search.query != "" {
		return nil
	}
	var prev string
	if m.list.cursor >= 0 && m.list.cursor < len(m.list.rows) {
		prev = m.list.rows[m.list.cursor].id()
	}
	m.list.collapsed = map[string]bool{}
	if collapse {
		for _, r := range m.list.all {
			if r.isHeader() && !r.note {
				m.list.collapsed[r.key] = true
			}
		}
	}
	m.showRows()
	m.selectInList(prev)
	return m.saveCollapsed()
}

// --- section rename ---

// sectionUnderCursor returns the section to rename: the board column, the list group
// (in a project), or the section of the selected task when it is in a single project.
func (m *Model) sectionUnderCursor() (gid, name string) {
	sc := m.currentScope()
	if m.boardActive() {
		cols := m.boardColumns()
		if m.board.col < len(cols) {
			return cols[m.board.col].gid, cols[m.board.col].name
		}
		return "", ""
	}
	if sc.kind == scopeProject && m.search.query == "" {
		if h := m.groupAt(m.list.cursor); h >= 0 && m.list.rows[h].key != "other" {
			return m.list.rows[h].key, m.list.rows[h].header
		}
		return "", ""
	}
	if t := m.selectedTask(); t != nil && len(t.Memberships) == 1 {
		return t.Memberships[0].SectionGID, t.Memberships[0].SectionName
	}
	return "", ""
}

func (m *Model) startRenameSection() tea.Cmd {
	gid, name := m.sectionUnderCursor()
	if gid == "" {
		m.status = "名前を変えるセクションが決まりません（プロジェクトのリストかボードで使ってください）"
		return nil
	}
	m.openPrompt(promptRenameSection)
	m.prompt.renameGID = gid
	m.prompt.input.SetValue(name)
	m.prompt.input.CursorEnd()
	return m.prompt.input.Focus()
}

func (m *Model) renameSection(gid, name string) tea.Cmd {
	sections := m.sections(m.deps.Now())
	return func() tea.Msg {
		if err := sections.RenameHere(m.ctx, gid, name); err != nil {
			return writeDoneMsg{err: err}
		}
		return writeDoneMsg{status: "セクション名を変更しました: " + name}
	}
}
