package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asana-tui/internal/store"
)

// The detail view is a list of items: the task's fields, its subtasks and "add" rows.
// j/k move between items, enter changes the item (a chooser, the editor, or opening a
// subtask). Opened subtasks stack up; backspace returns to the parent.

type itemKind int

const (
	itemName itemKind = iota
	itemAssignee
	itemDue
	itemSection
	itemTags
	itemCompleted
	itemParent
	itemNotes
	itemSubtask
	itemAddSubtask
	itemAddComment
	itemLike
	itemFollowers
	itemProjects // "＋ プロジェクトに追加 / 外す"
	itemAttachment
	itemAddAttachment
)

type detailItem struct {
	kind itemKind
	sub  int // index into Subtasks for itemSubtask
	line int // first line of the item in the rendered detail
}

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

// actionTask is the task that actions apply to: the task shown in the detail view while it
// has focus, otherwise the list selection.
func (m *Model) actionTask() *store.TaskSummary {
	if m.inDetail() {
		return &m.detail.TaskSummary
	}
	return m.selectedTask()
}

func (m *Model) currentItem() *detailItem {
	if !m.inDetail() || m.detailCursor < 0 || m.detailCursor >= len(m.detailItems) {
		return nil
	}
	return &m.detailItems[m.detailCursor]
}

// selectedSubtask returns the subtask under the detail cursor, if any.
func (m *Model) selectedSubtask() *store.TaskSummary {
	it := m.currentItem()
	if it == nil || it.kind != itemSubtask || it.sub >= len(m.detail.Subtasks) {
		return nil
	}
	return &m.detail.Subtasks[it.sub]
}

// resetDetailNav forgets the subtask stack (when the list selection changes).
func (m *Model) resetDetailNav() {
	m.detailStack = nil
	m.detailCursor = 0
}

// openDetail shows the selected list task full screen (enter in the list / board).
func (m *Model) openDetail() tea.Cmd {
	if m.selectedTask() == nil {
		return nil
	}
	m.zoom = true
	m.focus = paneDetail
	m.detailCursor = 0
	m.refreshDetailView()
	return nil
}

// moveDetailCursor moves between items; past the last item it scrolls (to read comments).
func (m *Model) moveDetailCursor(delta int) {
	n := len(m.detailItems)
	if n == 0 {
		m.scrollDetail(delta)
		return
	}
	next := m.detailCursor + delta
	switch {
	case next >= n:
		m.scrollDetail(1)
		return
	case next < 0:
		m.detailView.GotoTop()
		return
	}
	m.detailCursor = next
	m.refreshDetailView()
	m.scrollToCursor()
}

func (m *Model) scrollDetail(delta int) {
	if delta > 0 {
		m.detailView.ScrollDown(delta)
	} else {
		m.detailView.ScrollUp(-delta)
	}
}

// scrollToCursor keeps the selected item visible.
func (m *Model) scrollToCursor() {
	it := m.currentItem()
	if it == nil {
		return
	}
	top, h := m.detailView.YOffset(), m.detailView.Height()
	switch {
	case it.line < top:
		m.detailView.SetYOffset(max(0, it.line-1))
	case it.line >= top+h:
		m.detailView.SetYOffset(it.line - h + 2)
	}
}

// activateItem runs enter on the selected item.
func (m *Model) activateItem() tea.Cmd {
	it := m.currentItem()
	if it == nil || m.detail == nil {
		return nil
	}
	d := m.detail
	switch it.kind {
	case itemName, itemNotes:
		return m.editTask(d.GID)
	case itemAssignee:
		return m.openAssigneePicker()
	case itemDue:
		return m.openDuePicker()
	case itemSection:
		if it.sub < len(d.Memberships) {
			m.sectionProject = d.Memberships[it.sub].ProjectGID
		}
		m.openSectionPicker()
		return nil
	case itemLike:
		return m.toggleLike()
	case itemFollowers:
		return m.openFollowers()
	case itemProjects:
		return m.openProjects()
	case itemAttachment:
		if it.sub < len(d.Attachments) {
			return m.openAttachment(d.Attachments[it.sub])
		}
	case itemAddAttachment:
		return m.startAttach()
	case itemTags:
		return m.openTagEditor()
	case itemCompleted:
		return m.toggleComplete(d.TaskSummary)
	case itemParent:
		if d.Parent != nil {
			return m.pushDetail(d.Parent.GID)
		}
	case itemSubtask:
		if sub := m.selectedSubtask(); sub != nil {
			return m.pushDetail(sub.GID)
		}
	case itemAddSubtask:
		return m.startSubtask()
	case itemAddComment:
		return m.editComment(d.GID)
	}
	return nil
}

// pushDetail opens another task (a subtask or the parent) inside the detail view.
func (m *Model) pushDetail(gid string) tea.Cmd {
	if len(m.detailStack) == 0 {
		m.detailStack = append(m.detailStack, m.detail.GID)
	}
	m.detailStack = append(m.detailStack, gid)
	m.detailCursor = 0
	return tea.Batch(m.loadDetail(gid), m.debounceDetail(gid))
}

// popDetail returns to the previous task in the detail view; false if there is none.
func (m *Model) popDetail() (tea.Cmd, bool) {
	if len(m.detailStack) < 2 {
		return nil, false
	}
	child := m.detailStack[len(m.detailStack)-1]
	m.detailStack = m.detailStack[:len(m.detailStack)-1]
	parent := m.detailStack[len(m.detailStack)-1]
	if len(m.detailStack) == 1 && m.selectedTask() != nil && m.selectedTask().GID == parent {
		m.detailStack = nil // back at the list selection
	}
	m.returnTo = child
	return m.loadDetail(parent), true
}

// placeCursorAfterLoad restores the cursor when the shown task (re)loads.
func (m *Model) placeCursorAfterLoad(sameTask bool) {
	if m.returnTo != "" { // back from a subtask: select it
		m.refreshDetailView()
		for i, it := range m.detailItems {
			if it.kind == itemSubtask && it.sub < len(m.detail.Subtasks) && m.detail.Subtasks[it.sub].GID == m.returnTo {
				m.detailCursor = i
			}
		}
		m.returnTo = ""
		return
	}
	if !sameTask {
		m.detailCursor = 0
	}
}

func (m *Model) handleDetailKey(key string) tea.Cmd {
	switch key {
	case "j", "down":
		m.moveDetailCursor(1)
	case "k", "up":
		m.moveDetailCursor(-1)
	case "ctrl+d", "pgdown":
		m.detailView.HalfPageDown()
	case "ctrl+u", "pgup":
		m.detailView.HalfPageUp()
	case "g", "home":
		m.detailCursor = 0
		m.refreshDetailView()
		m.detailView.GotoTop()
	case "G", "end":
		m.detailCursor = max(0, len(m.detailItems)-1)
		m.refreshDetailView()
		m.detailView.GotoBottom()
	case "enter", "l", "right":
		return m.activateItem()
	case "h", "left":
		if !m.zoom {
			m.focus = paneList
		}
	}
	return nil
}
