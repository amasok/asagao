package tui

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/readmodel"
)

// Subtasks in the list view: l / → expands a task's subtasks under it (as a tree),
// h / ← collapses it or jumps to the parent. S adds a subtask, @ changes the assignee.

const maxSubtaskDepth = 5

// withSubtasks inserts the subtasks of expanded tasks after them (recursively).
// Subtasks follow the completion filter, except that "all" also shows completed ones.
func withSubtasks(ctx context.Context, st Store, rows []row, expanded map[string]bool, completed *bool) ([]row, error) {
	if len(expanded) == 0 {
		return rows, nil
	}
	var out []row
	var add func(r row) error
	add = func(r row) error {
		out = append(out, r)
		if r.task == nil || !expanded[r.task.GID] || r.depth >= maxSubtaskDepth {
			return nil
		}
		subs, err := queryOf(st).ListTasks(ctx, readmodel.TaskFilter{ParentGID: r.task.GID, Completed: completed})
		if err != nil {
			return err
		}
		for i := range subs {
			if err := add(row{task: &subs[i], depth: r.depth + 1}); err != nil {
				return err
			}
		}
		return nil
	}
	for _, r := range rows {
		if err := add(r); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// expandOrOpen expands the selected task's subtasks, or opens the detail if it has none
// (or is already expanded).
func (m *Model) expandOrOpen() tea.Cmd {
	t := m.selectedTask()
	if t == nil {
		if m.list.cursor >= 0 && m.list.cursor < len(m.list.rows) && m.list.rows[m.list.cursor].collapsed {
			return m.toggleGroup()
		}
		return nil
	}
	if t.NumSubtasks == 0 || m.list.expanded[t.GID] {
		m.focus = paneDetail
		return nil
	}
	if m.list.expanded == nil {
		m.list.expanded = map[string]bool{}
	}
	m.list.expanded[t.GID] = true
	cmds := []tea.Cmd{m.loadTasks()}
	// Subtasks may not be in the store yet (e.g. a task only seen in My Tasks).
	if m.syncer != nil {
		cmds = append(cmds, m.fetchDetails(t.GID))
	}
	return tea.Batch(cmds...)
}

// collapseOrUp collapses the selected task, moves to its parent, or goes to the sidebar.
func (m *Model) collapseOrUp() tea.Cmd {
	t := m.selectedTask()
	switch {
	case t != nil && m.list.expanded[t.GID]:
		delete(m.list.expanded, t.GID)
		return m.loadTasks()
	case t != nil && m.list.cursor < len(m.list.rows) && m.list.rows[m.list.cursor].depth > 0:
		depth := m.list.rows[m.list.cursor].depth
		for i := m.list.cursor - 1; i >= 0; i-- {
			if m.list.rows[i].task != nil && m.list.rows[i].depth < depth {
				m.list.cursor = i
				m.ensureListVisible()
				return tea.Batch(m.loadDetail(m.list.rows[i].task.GID), m.debounceDetail(m.list.rows[i].task.GID))
			}
		}
		return nil
	}
	if m.hideSidebar {
		m.hideSidebar = false
		m.focus = paneSidebar
		return m.saveLayout()
	}
	m.focus = paneSidebar
	return nil
}

// --- creating subtasks ---

func (m *Model) startSubtask() tea.Cmd {
	t := m.actionTask() // in the detail view: the task being shown
	if t == nil || m.focus == paneSidebar {
		return nil
	}
	m.openPrompt(promptSubtask)
	m.prompt.input.Prompt = "サブタスク（" + truncateRunes(t.Name, 20) + "）: "
	m.prompt.subtaskParent = t.GID
	return m.prompt.input.Focus()
}

func (m *Model) createSubtask(parent, input string) tea.Cmd {
	if m.list.expanded == nil {
		m.list.expanded = map[string]bool{}
	}
	m.list.expanded[parent] = true // show the new subtask under its parent
	keep := m.inDetail()           // in the detail view, stay on the parent
	return m.quickCreate(input, m.deps.Now(), app.CreateHereInput{Parent: parent}, "", "サブタスクを追加しました: ", keep)
}

func truncateRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// --- assignee ---

type usersLoadedMsg struct {
	users []readmodel.UserRef
	err   error
}

func (m *Model) openAssigneePicker() tea.Cmd {
	if m.actionTask() == nil || m.focus == paneSidebar {
		return nil
	}
	st := m.deps.Store
	return func() tea.Msg {
		users, err := queryOf(st).ListUsers(m.ctx)
		return usersLoadedMsg{users: users, err: err}
	}
}

func (m *Model) showAssigneePicker(users []readmodel.UserRef) {
	t := m.actionTask()
	if t == nil {
		return
	}
	me, _ := queryOf(m.deps.Store).MeGID(m.ctx)
	current := ""
	if t.Assignee != nil {
		current = t.Assignee.GID
	}
	p := &picker{kind: pickAssignee, title: "担当者（" + truncateRunes(t.Name, 30) + "）", task: *t}
	for _, u := range users {
		label := u.Name
		if u.GID == me {
			label += "（自分）"
		}
		item := pickerItem{label: label, value: u.GID}
		if u.GID == current {
			item.hint = "現在"
			p.cursor = len(p.items)
		}
		p.items = append(p.items, item)
	}
	unassigned := pickerItem{label: "（未割り当て）"}
	if current == "" {
		unassigned.hint = "現在"
		p.cursor = len(p.items)
	}
	p.items = append(p.items, unassigned)
	m.picker = p
}

func (m *Model) assign(t readmodel.TaskSummary, userGID, label string) tea.Cmd {
	label = strings.TrimSuffix(label, "（自分）")
	return m.updateTask(t, app.TaskUpdate{Assignee: &userGID}, "担当者を変更しました: "+t.Name+" → "+label)
}
