package tui

import (
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asana-tui/internal/app"
	"github.com/amasok/asana-tui/internal/store"
)

// Dependencies, task types and approvals in the detail view: the tasks a task waits for
// and the tasks waiting for it (enter: open / remove; "＋" rows add one from the tasks of
// the same projects, or a search), its type (task / milestone / approval) and, for an
// approval task, its status.

// --- type and approval ---

func (m *Model) openSubtypePicker(t store.TaskSummary) tea.Cmd {
	p := &picker{kind: pickSubtype, title: "種類（" + truncateRunes(t.Name, 30) + "）", task: t}
	for i, v := range store.TaskSubtypes {
		item := pickerItem{label: store.SubtypeLabel(v), value: v}
		if v == t.ResourceSubtype || (t.ResourceSubtype == "" && v == "default_task") {
			item.hint, p.cursor = "現在", i
		}
		p.items = append(p.items, item)
	}
	m.picker = p
	return nil
}

func (m *Model) openApprovalPicker(t store.TaskSummary) tea.Cmd {
	current := t.ApprovalStatus
	if current == "" {
		current = "pending"
	}
	p := &picker{kind: pickApproval, title: "承認（" + truncateRunes(t.Name, 30) + "）", task: t}
	for i, v := range []string{"approved", "changes_requested", "rejected", "pending"} {
		item := pickerItem{label: store.ApprovalLabel(v), value: v}
		if v == current {
			item.hint, p.cursor = "現在", i
		}
		p.items = append(p.items, item)
	}
	m.picker = p
	return nil
}

// updateTask applies resolved changes to the task t and reports status.
func (m *Model) updateTask(t store.TaskSummary, u app.TaskUpdate, status string) tea.Cmd {
	tasks := m.tasks(m.deps.Now())
	return func() tea.Msg {
		if err := tasks.UpdateHere(m.ctx, t.GID, u); err != nil {
			return writeDoneMsg{err: err}
		}
		return writeDoneMsg{status: status}
	}
}

// --- dependencies ---

// openDependencyActions offers what can be done with a linked task.
func (m *Model) openDependencyActions(t store.TaskSummary, dep store.DependencyView, dependents bool) tea.Cmd {
	p := &picker{kind: pickDepAction, title: truncateRunes(dep.Name, 40), task: t, dependents: dependents}
	p.items = []pickerItem{{label: "開く", value: "open\x00" + dep.GID}}
	remove := "このタスクを待つのをやめる（依存を外す）"
	if dependents {
		remove = "このタスクを待たないようにする（依存を外す）"
	}
	p.items = append(p.items, pickerItem{label: remove, value: "remove\x00" + dep.GID})
	m.picker = p
	return nil
}

func (m *Model) runDependencyAction(p *picker, value string) tea.Cmd {
	action, gid, _ := strings.Cut(value, "\x00")
	if action == "open" {
		return m.pushDetail(gid)
	}
	return m.setDependency(p.task, gid, p.dependents, false)
}

// setDependency links (or unlinks) task t and other: t waits for other, or with
// dependents other waits for t.
func (m *Model) setDependency(t store.TaskSummary, other string, dependents, add bool) tea.Cmd {
	st, now := m.deps.Store, m.deps.Now()
	return func() tea.Msg {
		waiting, blocker := t.GID, other
		if dependents {
			waiting, blocker = other, t.GID
		}
		if err := st.SetDependencyLocal(m.ctx, waiting, blocker, add, now); err != nil {
			return writeDoneMsg{err: err}
		}
		if add {
			return writeDoneMsg{status: "依存関係を追加しました"}
		}
		return writeDoneMsg{status: "依存関係を外しました"}
	}
}

type depCandidatesMsg struct {
	task       store.TaskSummary
	dependents bool
	query      string
	tasks      []store.TaskSummary
	err        error
}

// openDependencyTargets lists tasks to link: with query, the search hits; otherwise the
// incomplete tasks of the task's projects (or my incomplete tasks).
func (m *Model) openDependencyTargets(t store.TaskSummary, dependents bool, query string) tea.Cmd {
	st := m.deps.Store
	return func() tea.Msg {
		open := false
		var tasks []store.TaskSummary
		switch {
		case query != "":
			found, err := st.ListTasks(m.ctx, store.TaskFilter{Query: query, Limit: 100})
			if err != nil {
				return depCandidatesMsg{err: err}
			}
			tasks = found
		case len(t.Memberships) > 0:
			for _, ms := range t.Memberships {
				found, err := st.ListTasks(m.ctx, store.TaskFilter{ProjectGID: ms.ProjectGID, Completed: &open, Limit: 200})
				if err != nil {
					return depCandidatesMsg{err: err}
				}
				tasks = append(tasks, found...)
			}
		default:
			me, err := st.MeGID(m.ctx)
			if err != nil {
				return depCandidatesMsg{err: err}
			}
			if tasks, err = st.ListTasks(m.ctx, store.TaskFilter{AssigneeGID: me, Completed: &open, Limit: 200}); err != nil {
				return depCandidatesMsg{err: err}
			}
		}
		d, err := st.GetTask(m.ctx, t.GID)
		if err != nil {
			return depCandidatesMsg{err: err}
		}
		linked := map[string]bool{t.GID: true}
		for _, l := range slices.Concat(d.Dependencies, d.Dependents) {
			linked[l.GID] = true
		}
		var out []store.TaskSummary
		for _, c := range tasks {
			if !linked[c.GID] && !store.IsLocalGID(c.GID) {
				linked[c.GID] = true
				out = append(out, c)
			}
		}
		return depCandidatesMsg{task: t, dependents: dependents, query: query, tasks: out}
	}
}

func (m *Model) showDependencyTargets(msg depCandidatesMsg) {
	title := "待つタスクを選ぶ（" + truncateRunes(msg.task.Name, 24) + " が待つ）"
	if msg.dependents {
		title = "待たせるタスクを選ぶ（" + truncateRunes(msg.task.Name, 24) + " を待つ）"
	}
	if msg.query != "" {
		title += "  検索: " + msg.query
	}
	p := &picker{kind: pickDepTarget, title: title, task: msg.task, dependents: msg.dependents}
	p.items = append(p.items, pickerItem{label: "＋ タスク名で検索…", value: "?"})
	for _, c := range msg.tasks {
		label := c.Name
		if b := store.TaskBadges(c); b != "" {
			label += " " + b
		}
		item := pickerItem{label: label, value: c.GID}
		if c.Completed {
			item.hint = "完了"
		} else if c.DueOn != "" {
			item.hint = shortDue(c.DueOn, m.deps.Now())
		}
		p.items = append(p.items, item)
	}
	if len(msg.tasks) > 0 {
		p.cursor = 1
	}
	m.picker = p
}

func (m *Model) startDependencySearch(t store.TaskSummary, dependents bool) tea.Cmd {
	m.openPrompt(promptDepSearch)
	m.depTask, m.depDependents = t, dependents
	return m.prompt.Focus()
}

// updateDeps handles the messages of this file.
func (m *Model) updateDeps(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case depCandidatesMsg:
		if msg.err != nil {
			m.status = "タスクを読み込めませんでした: " + msg.err.Error()
		} else {
			m.showDependencyTargets(msg)
		}
		return nil, true
	}
	return nil, false
}

// handleDepPicker runs the choice of a chooser of this file; ok is false for other choosers.
func (m *Model) handleDepPicker(p *picker, item pickerItem) (tea.Cmd, bool) {
	switch p.kind {
	case pickSubtype:
		return m.updateTask(p.task, app.TaskUpdate{Subtype: &item.value}, "種類を「"+item.label+"」にしました"), true
	case pickApproval:
		return m.updateTask(p.task, app.TaskUpdate{Approval: &item.value}, "承認の状態を「"+item.label+"」にしました"), true
	case pickDepAction:
		return m.runDependencyAction(p, item.value), true
	case pickDepTarget:
		if item.value == "?" {
			return m.startDependencySearch(p.task, p.dependents), true
		}
		return m.setDependency(p.task, item.value, p.dependents, true), true
	}
	return nil, false
}

// dependencyLine is a linked task in the detail view.
func (m *Model) dependencyLine(d store.DependencyView) string {
	check := "☐"
	if d.Completed {
		check = "☑"
	}
	name := d.Name
	if name == "" {
		name = m.st.dim.Render(fmt.Sprintf("（未取得のタスク %s）", d.GID))
	}
	text := check + " " + name
	if d.ResourceSubtype == "milestone" {
		text = check + " ◆ " + name
	}
	if d.Pending {
		text += " ↑"
	}
	return text
}
