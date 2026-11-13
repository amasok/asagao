package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asana-tui/internal/store"
)

// Navigation inside the detail view: J/K select a subtask, enter opens it (pushing it on
// a stack, so backspace/esc returns to the parent), x completes the selected subtask and
// S adds a subtask to the task being shown.

// detailGID is the task shown in the detail pane: the top of the stack, or the list selection.
func (m *Model) detailGID() string {
	if n := len(m.detailStack); n > 0 {
		return m.detailStack[n-1]
	}
	if t := m.selectedTask(); t != nil {
		return t.GID
	}
	return ""
}

// inDetail reports whether keys act on the detail view rather than the list.
func (m *Model) inDetail() bool { return (m.focus == paneDetail || m.zoom) && m.detail != nil }

// actionTask is the task that edit keys (c, @, m, e, o) act on: the task shown in the detail
// view while it has focus, otherwise the list selection.
func (m *Model) actionTask() *store.TaskSummary {
	if m.inDetail() {
		return &m.detail.TaskSummary
	}
	return m.selectedTask()
}

// selectedSubtask returns the subtask selected in the detail view, if any.
func (m *Model) selectedSubtask() *store.TaskSummary {
	if !m.inDetail() || m.detailSub < 0 || m.detailSub >= len(m.detail.Subtasks) {
		return nil
	}
	return &m.detail.Subtasks[m.detailSub]
}

// resetDetailNav forgets subtask navigation (when the list selection changes).
func (m *Model) resetDetailNav() {
	m.detailStack = nil
	m.detailSub = -1
}

// openDetail shows the selected list task full screen (enter in the list / board).
func (m *Model) openDetail() tea.Cmd {
	if m.selectedTask() == nil {
		return nil
	}
	m.zoom = true
	m.focus = paneDetail
	m.detailSub = -1
	return nil
}

// handleDetailNavKey handles subtask selection keys; ok is false for other keys.
func (m *Model) handleDetailNavKey(key string) (tea.Cmd, bool) {
	d := m.detail
	if d == nil {
		return nil, false
	}
	switch key {
	case "J", "K":
		if len(d.Subtasks) == 0 {
			m.status = "サブタスクはありません（S で追加）"
			return nil, true
		}
		if key == "J" {
			m.detailSub = min(m.detailSub+1, len(d.Subtasks)-1)
		} else {
			m.detailSub = max(m.detailSub-1, 0)
		}
		m.refreshDetailView()
		m.scrollToSubtask()
		return nil, true
	case "enter":
		sub := m.selectedSubtask()
		if sub == nil {
			return nil, true
		}
		if len(m.detailStack) == 0 {
			m.detailStack = append(m.detailStack, d.GID)
		}
		m.detailStack = append(m.detailStack, sub.GID)
		m.detailSub = -1
		return tea.Batch(m.loadDetail(sub.GID), m.debounceDetail(sub.GID)), true
	case "backspace", "esc":
		if len(m.detailStack) > 1 {
			child := m.detailStack[len(m.detailStack)-1]
			m.detailStack = m.detailStack[:len(m.detailStack)-1]
			parent := m.detailStack[len(m.detailStack)-1]
			if len(m.detailStack) == 1 && m.selectedTask() != nil && m.selectedTask().GID == parent {
				m.detailStack = nil // back at the list selection
			}
			m.detailSub = -1
			m.returnTo = child
			return m.loadDetail(parent), true
		}
		if key == "esc" && m.detailSub >= 0 {
			m.detailSub = -1
			m.refreshDetailView()
			return nil, true
		}
	}
	return nil, false
}

// scrollToSubtask keeps the selected subtask line visible in the detail viewport.
func (m *Model) scrollToSubtask() {
	if m.subtaskLine < 0 || m.detailSub < 0 {
		return
	}
	line := m.subtaskLine + m.detailSub
	top, h := m.detailView.YOffset(), m.detailView.Height()
	switch {
	case line < top:
		m.detailView.SetYOffset(line)
	case line >= top+h:
		m.detailView.SetYOffset(line - h + 1)
	}
}
