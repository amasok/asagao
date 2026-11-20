package tui

import (
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asagao/internal/readmodel"
)

// The detail view is a list of items: the task's fields, its subtasks and "add" rows.
// j/k move between items, enter changes the item (a chooser, the editor, or opening a
// subtask). Opened subtasks stack up; backspace returns to the parent.

// detailState is the state of the detail pane.
type detailState struct {
	task *readmodel.TaskDetail
	me   string // the logged-in user when task was loaded: their comments can be changed
	view viewport.Model
	// startOffered: the task can be given a start date (startdate.go)
	startOffered bool
	fetchingFor  string // gid whose details are being fetched from Asana

	stack          []string     // tasks opened from the detail view (subtasks); top is shown
	cursor         int          // selected item in the detail view
	items          []detailItem // items of the rendered detail (fields, subtasks, add rows)
	renderedActive bool         // the detail was last rendered with selectable items
	returnTo       string       // subtask to reselect after going back to its parent
}

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
	itemCustomField   // sub: index into CustomFields
	itemSubtype       // task type
	itemApproval      // approval status
	itemCustomStatus  // status of a custom task type (#59)
	itemDependency    // sub: index into Dependencies
	itemAddDependency //
	itemDependent     // sub: index into Dependents
	itemAddDependent  //
	itemComment       // sub: index into Stories (my own comments only: they can be changed)
	itemStart         // start date (startdate.go)
)

type detailItem struct {
	kind itemKind
	sub  int // index into Subtasks for itemSubtask
	line int // first line of the item in the rendered detail
}

// detailGID is the task shown in the detail pane: the top of the stack, or the list selection.
func (m *Model) detailGID() string {
	if n := len(m.detail.stack); n > 0 {
		return m.detail.stack[n-1]
	}
	if t := m.selectedTask(); t != nil {
		return t.GID
	}
	return ""
}

// inDetail reports whether keys act on the detail view rather than the list.
func (m *Model) inDetail() bool { return (m.focus == paneDetail || m.zoom) && m.detail.task != nil }

// actionTask is the task that actions apply to: the task shown in the detail view while it
// has focus, otherwise the list selection.
func (m *Model) actionTask() *readmodel.TaskSummary {
	if m.inDetail() {
		return &m.detail.task.TaskSummary
	}
	return m.selectedTask()
}

func (m *Model) currentItem() *detailItem {
	if !m.inDetail() || m.detail.cursor < 0 || m.detail.cursor >= len(m.detail.items) {
		return nil
	}
	return &m.detail.items[m.detail.cursor]
}

// selectedSubtask returns the subtask under the detail cursor, if any.
func (m *Model) selectedSubtask() *readmodel.TaskSummary {
	it := m.currentItem()
	if it == nil || it.kind != itemSubtask || it.sub >= len(m.detail.task.Subtasks) {
		return nil
	}
	return &m.detail.task.Subtasks[it.sub]
}

// resetDetailNav forgets the subtask stack (when the list selection changes).
func (m *Model) resetDetailNav() {
	m.detail.stack = nil
	m.detail.cursor = 0
}

// openDetail shows the selected list task full screen (enter in the list / board).
func (m *Model) openDetail() tea.Cmd {
	if m.selectedTask() == nil {
		return nil
	}
	m.zoom = true
	m.focus = paneDetail
	m.detail.cursor = 0
	m.refreshDetailView()
	return nil
}

// moveDetailCursor moves between items; past the last item it scrolls (to read comments).
func (m *Model) moveDetailCursor(delta int) {
	n := len(m.detail.items)
	if n == 0 {
		m.scrollDetail(delta)
		return
	}
	next := m.detail.cursor + delta
	switch {
	case next >= n:
		m.scrollDetail(1)
		return
	case next < 0:
		m.detail.view.GotoTop()
		return
	}
	m.detail.cursor = next
	m.refreshDetailView()
	m.scrollToCursor()
}

func (m *Model) scrollDetail(delta int) {
	if delta > 0 {
		m.detail.view.ScrollDown(delta)
	} else {
		m.detail.view.ScrollUp(-delta)
	}
}

// scrollToCursor keeps the selected item visible.
func (m *Model) scrollToCursor() {
	it := m.currentItem()
	if it == nil {
		return
	}
	top, h := m.detail.view.YOffset(), m.detail.view.Height()
	switch {
	case it.line < top:
		m.detail.view.SetYOffset(max(0, it.line-1))
	case it.line >= top+h:
		m.detail.view.SetYOffset(it.line - h + 2)
	}
}

// activateItem runs enter on the selected item.
func (m *Model) activateItem() tea.Cmd {
	it := m.currentItem()
	if it == nil || m.detail.task == nil {
		return nil
	}
	d := m.detail.task
	switch it.kind {
	case itemName, itemNotes:
		return m.editTask(d.GID)
	case itemAssignee:
		return m.openAssigneePicker()
	case itemDue:
		return m.openDuePicker()
	case itemStart:
		return m.openStartPicker(*d)
	case itemSection:
		if it.sub < len(d.Memberships) {
			m.sectionProject = d.Memberships[it.sub].ProjectGID
		}
		m.openSectionPicker()
		return nil
	case itemLike:
		return m.toggleLike()
	case itemSubtype:
		return m.openSubtypePicker(*d)
	case itemApproval:
		return m.openApprovalPicker(d.TaskSummary)
	case itemCustomStatus:
		return m.openCustomStatusPicker(*d)
	case itemDependency:
		if it.sub < len(d.Dependencies) {
			return m.openDependencyActions(d.TaskSummary, d.Dependencies[it.sub], false)
		}
	case itemDependent:
		if it.sub < len(d.Dependents) {
			return m.openDependencyActions(d.TaskSummary, d.Dependents[it.sub], true)
		}
	case itemAddDependency:
		return m.openDependencyTargets(d.TaskSummary, false, "")
	case itemAddDependent:
		return m.openDependencyTargets(d.TaskSummary, true, "")
	case itemCustomField:
		if it.sub < len(d.CustomFields) {
			return m.editCustomField(d.TaskSummary, d.CustomFields[it.sub])
		}
	case itemFollowers:
		return m.openFollowers()
	case itemProjects:
		return m.openProjects()
	case itemAttachment:
		if it.sub < len(d.Attachments) {
			return m.openAttachmentActions(d.Attachments[it.sub])
		}
	case itemComment:
		if it.sub < len(d.Stories) {
			return m.openCommentActions(d.Stories[it.sub])
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
	if len(m.detail.stack) == 0 {
		m.detail.stack = append(m.detail.stack, m.detail.task.GID)
	}
	m.detail.stack = append(m.detail.stack, gid)
	m.detail.cursor = 0
	return tea.Batch(m.loadDetail(gid), m.debounceDetail(gid))
}

// popDetail returns to the previous task in the detail view; false if there is none.
func (m *Model) popDetail() (tea.Cmd, bool) {
	if len(m.detail.stack) < 2 {
		return nil, false
	}
	child := m.detail.stack[len(m.detail.stack)-1]
	m.detail.stack = m.detail.stack[:len(m.detail.stack)-1]
	parent := m.detail.stack[len(m.detail.stack)-1]
	if len(m.detail.stack) == 1 && m.selectedTask() != nil && m.selectedTask().GID == parent {
		m.detail.stack = nil // back at the list selection
	}
	m.detail.returnTo = child
	return m.loadDetail(parent), true
}

// placeCursorAfterLoad restores the cursor when the shown task (re)loads.
func (m *Model) placeCursorAfterLoad(sameTask bool) {
	if m.detail.returnTo != "" { // back from a subtask: select it
		m.refreshDetailView()
		for i, it := range m.detail.items {
			if it.kind == itemSubtask && it.sub < len(m.detail.task.Subtasks) && m.detail.task.Subtasks[it.sub].GID == m.detail.returnTo {
				m.detail.cursor = i
			}
		}
		m.detail.returnTo = ""
		return
	}
	if !sameTask {
		m.detail.cursor = 0
	}
}

func (m *Model) handleDetailKey(key string) tea.Cmd {
	switch key {
	case "j", "down":
		m.moveDetailCursor(1)
	case "k", "up":
		m.moveDetailCursor(-1)
	case "ctrl+d", "pgdown":
		m.detail.view.HalfPageDown()
	case "ctrl+u", "pgup":
		m.detail.view.HalfPageUp()
	case "g", "home":
		m.detail.cursor = 0
		m.refreshDetailView()
		m.detail.view.GotoTop()
	case "G", "end":
		m.detail.cursor = max(0, len(m.detail.items)-1)
		m.refreshDetailView()
		m.detail.view.GotoBottom()
	case "enter", "l", "right":
		return m.activateItem()
	case "h", "left":
		if !m.zoom {
			m.focus = paneList
		}
	}
	return nil
}
