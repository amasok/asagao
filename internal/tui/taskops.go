package tui

import (
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/readmodel"
)

// Deleting a task and changing its parent (#32). Deleting asks first; the delete waits in
// the outbox (where it can be discarded) until it is sent.

// taskOps is the use cases that reorganize tasks, with a clock that reads now (see tasks).
func (m *Model) taskOps(now time.Time) *app.TaskOps {
	return &app.TaskOps{Store: m.deps.Store, Clock: func() time.Time { return now }}
}

type (
	deletePlanMsg struct {
		task readmodel.TaskSummary
		plan readmodel.DeletePlan
		err  error
	}
	taskDeletedMsg struct {
		status string
		err    error
	}
	parentCandidatesMsg struct {
		task  readmodel.TaskSummary
		tasks []readmodel.TaskSummary
		err   error
	}
)

// confirmDeleteTask looks up what deleting the task takes with it, then asks.
func (m *Model) confirmDeleteTask() tea.Cmd {
	t := m.actionTask()
	if t == nil {
		return nil
	}
	task, ops := *t, m.taskOps(m.deps.Now())
	return func() tea.Msg {
		plan, err := ops.PlanDelete(m.ctx, task.GID)
		return deletePlanMsg{task: task, plan: plan, err: err}
	}
}

// deleteQuestion is the confirmation's title: what goes, and how to take it back.
func deleteQuestion(p readmodel.DeletePlan) string {
	q := "「" + truncateRunes(p.Name, 30) + "」"
	if p.Subtasks > 0 {
		q += fmt.Sprintf("とサブタスク %d 件", p.Subtasks)
	}
	q += "を削除しますか？"
	switch {
	case p.Local:
		q += "（まだ送っていないタスクなので、すぐ消えます）"
	case p.Queued > 0:
		q += fmt.Sprintf("（未送信の変更 %d 件も消えます。送るまでは「未送信の変更」から取り消せます）", p.Queued)
	default:
		q += "（送るまでは「未送信の変更」から取り消せます）"
	}
	return q
}

func (m *Model) deleteTask(gid string) tea.Cmd {
	ops := m.taskOps(m.deps.Now())
	return func() tea.Msg {
		plan, err := ops.Delete(m.ctx, gid)
		if err != nil {
			return taskDeletedMsg{err: err}
		}
		return taskDeletedMsg{status: "削除しました: " + plan.Name}
	}
}

// openParentPicker lists the tasks the task can go under: "no parent", then the incomplete
// tasks of its projects (or my incomplete tasks), without itself and its subtasks.
func (m *Model) openParentPicker() tea.Cmd {
	t := m.actionTask()
	if t == nil {
		return nil
	}
	task, st := *t, m.deps.Store
	return func() tea.Msg {
		open := false
		var tasks []readmodel.TaskSummary
		filters := []readmodel.TaskFilter{}
		for _, ms := range task.Memberships {
			filters = append(filters, readmodel.TaskFilter{ProjectGID: ms.ProjectGID, Completed: &open, Limit: 200})
		}
		if len(filters) == 0 {
			me, err := queryOf(st).MeGID(m.ctx)
			if err != nil {
				return parentCandidatesMsg{err: err}
			}
			filters = append(filters, readmodel.TaskFilter{AssigneeGID: me, Completed: &open, Limit: 200})
		}
		for _, f := range filters {
			found, err := queryOf(st).ListTasks(m.ctx, f)
			if err != nil {
				return parentCandidatesMsg{err: err}
			}
			tasks = append(tasks, found...)
		}
		// not itself, its parent, or its subtasks (the store refuses deeper ones too)
		skip := map[string]bool{task.GID: true, task.ParentGID: true}
		for _, c := range tasks {
			if c.ParentGID == task.GID {
				skip[c.GID] = true
			}
		}
		var out []readmodel.TaskSummary
		for _, c := range tasks {
			if !skip[c.GID] {
				skip[c.GID] = true
				out = append(out, c)
			}
		}
		return parentCandidatesMsg{task: task, tasks: out}
	}
}

func (m *Model) showParentCandidates(msg parentCandidatesMsg) {
	p := &picker{kind: pickParent, title: "親タスクを選ぶ（" + truncateRunes(msg.task.Name, 24) + " をサブタスクにする）", task: msg.task}
	if msg.task.ParentGID != "" {
		p.items = append(p.items, pickerItem{label: "（親タスクから外す）", value: "\x00none"})
	}
	for _, c := range msg.tasks {
		item := pickerItem{label: c.Name, value: c.GID}
		if c.DueOn != "" {
			item.hint = shortDue(c.DueOn, m.deps.Now())
		}
		p.items = append(p.items, item)
	}
	if len(p.items) == 0 {
		m.status = "親にできるタスクがありません"
		return
	}
	m.picker = p
}

func (m *Model) setParent(t readmodel.TaskSummary, parent, parentName string) tea.Cmd {
	ops := m.taskOps(m.deps.Now())
	return func() tea.Msg {
		if err := ops.SetParent(m.ctx, t.GID, parent, model.Position{}); err != nil {
			return writeDoneMsg{err: err}
		}
		if parent == "" {
			return writeDoneMsg{status: "親タスクから外しました: " + t.Name, gid: t.GID}
		}
		return writeDoneMsg{status: fmt.Sprintf("「%s」のサブタスクにしました: %s", parentName, t.Name)}
	}
}

// handleTaskOpPicker handles a choice in this file's pickers.
func (m *Model) handleTaskOpPicker(p *picker, item pickerItem) (tea.Cmd, bool) {
	switch p.kind {
	case pickDeleteTask:
		if item.value == "" {
			return nil, true // やめる
		}
		return m.deleteTask(item.value), true
	case pickParent:
		if item.value == "\x00none" {
			return m.setParent(p.task, "", ""), true
		}
		return m.setParent(p.task, item.value, item.label), true
	}
	return nil, false
}

// updateTaskOps handles the messages of this file.
func (m *Model) updateTaskOps(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case deletePlanMsg:
		if msg.err != nil {
			m.status = "エラー: " + msg.err.Error()
			return nil, true
		}
		m.picker = &picker{kind: pickDeleteTask, title: deleteQuestion(msg.plan), task: msg.task,
			items: []pickerItem{{label: "やめる"}, {label: "削除する", value: msg.task.GID}}}
		return nil, true
	case taskDeletedMsg:
		if msg.err != nil {
			m.status = "エラー: " + msg.err.Error()
			return nil, true
		}
		m.status = msg.status
		// the task is hidden now: back to the list
		m.detailStack, m.zoom = nil, false
		if m.focus == paneDetail {
			m.focus = paneList
		}
		return m.afterWrite(), true
	case parentCandidatesMsg:
		if msg.err != nil {
			m.status = "エラー: " + msg.err.Error()
			return nil, true
		}
		m.showParentCandidates(msg)
		return nil, true
	}
	return nil, false
}
