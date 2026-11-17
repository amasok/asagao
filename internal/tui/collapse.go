package tui

import (
	"context"
	"encoding/json"
	"slices"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asagao/internal/readmodel"
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

func collapsedStateKey(scope string) string { return readmodel.KeyCollapsed + scope }

// loadCollapsed reads the collapsed group keys of a scope (in a command goroutine).
func loadCollapsed(ctx context.Context, st Store, scope string) map[string]bool {
	out := map[string]bool{}
	v, err := st.SyncState(ctx, collapsedStateKey(scope))
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
	keys := make([]string, 0, len(m.collapsed))
	for k := range m.collapsed {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	b, _ := json.Marshal(keys)
	st, key := m.deps.Store, collapsedStateKey(m.scopeKey())
	return func() tea.Msg {
		_ = st.SetSyncState(m.ctx, key, string(b))
		return nil
	}
}

// visibleRows drops the tasks of collapsed groups (search results are never collapsed).
func (m *Model) visibleRows() []row {
	if m.query != "" || len(m.collapsed) == 0 {
		out := make([]row, len(m.allRows))
		copy(out, m.allRows)
		for i := range out {
			out[i].collapsed = false
		}
		return out
	}
	out := make([]row, 0, len(m.allRows))
	hiding := false
	for _, r := range m.allRows {
		if r.isHeader() {
			r.collapsed = m.collapsed[r.key] && !r.note
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
		if i < len(m.rows) && m.rows[i].isHeader() && !m.rows[i].note {
			return i
		}
	}
	return -1
}

// toggleGroup collapses the group under the cursor, or expands it if it is collapsed.
func (m *Model) toggleGroup() tea.Cmd {
	h := m.groupAt(m.listCursor)
	if h < 0 || m.query != "" {
		return nil
	}
	key := m.rows[h].key
	if m.collapsed == nil {
		m.collapsed = map[string]bool{}
	}
	if m.collapsed[key] {
		delete(m.collapsed, key)
		m.rows = m.visibleRows()
		m.listCursor = m.nextTaskRow(h+1, 1)
		if m.listCursor < 0 || m.groupAt(m.listCursor) != h {
			m.listCursor = h
		}
	} else {
		m.collapsed[key] = true
		m.rows = m.visibleRows()
		m.listCursor = h
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
	if m.query != "" {
		return nil
	}
	var prev string
	if m.listCursor >= 0 && m.listCursor < len(m.rows) {
		prev = m.rows[m.listCursor].id()
	}
	m.collapsed = map[string]bool{}
	if collapse {
		for _, r := range m.allRows {
			if r.isHeader() && !r.note {
				m.collapsed[r.key] = true
			}
		}
	}
	m.rows = m.visibleRows()
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
		if m.boardCol < len(cols) {
			return cols[m.boardCol].gid, cols[m.boardCol].name
		}
		return "", ""
	}
	if sc.kind == scopeProject && m.query == "" {
		if h := m.groupAt(m.listCursor); h >= 0 && m.rows[h].key != "other" {
			return m.rows[h].key, m.rows[h].header
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
	m.renameGID = gid
	m.prompt.SetValue(name)
	m.prompt.CursorEnd()
	return m.prompt.Focus()
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
