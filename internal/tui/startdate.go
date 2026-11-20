package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/readmodel"
)

// The start date of a task (#57), chosen like the due date: common dates, "日付を入力…" or
// none. It needs a due date on or after it; the store refuses otherwise and the status
// line says why. Where a recent 402 says the plan lacks start dates (m.startOffered is
// false), the detail shows the date without offering to change it.

func (m *Model) openStartPicker(t readmodel.TaskDetail) tea.Cmd {
	items := dueItems(m.deps.Now())
	items[len(items)-1].label = "開始日なし"
	title := "開始日（" + truncateRunes(t.Name, 30) + "）"
	if t.DueOn == "" && t.DueAt == "" {
		title += "  ※先に期日が必要です"
	}
	p := &picker{kind: pickStart, title: title, task: t.TaskSummary, items: items}
	for i, item := range p.items {
		if item.value == t.StartOn && item.value != "?" {
			p.items[i].hint = "現在"
			p.cursor = i
		}
	}
	m.picker = p
	return nil
}

func (m *Model) setStart(t readmodel.TaskSummary, start string) tea.Cmd {
	status := "開始日を " + longDue(start, "") + " にしました"
	if start == "" {
		status = "開始日をなしにしました: " + t.Name
	}
	return m.updateTask(t, app.TaskUpdate{Start: &start}, status)
}

func (m *Model) startStartPrompt(t readmodel.TaskSummary) tea.Cmd {
	m.openPrompt(promptStart)
	m.dueTask, m.bulkDueTasks = t, nil
	m.prompt.SetValue(m.detailStartOn(t.GID))
	m.prompt.CursorEnd()
	return m.prompt.Focus()
}

// detailStartOn is the start date of task gid when it is the one in the detail ("" else).
func (m *Model) detailStartOn(gid string) string {
	if m.detail.task != nil && m.detail.task.GID == gid {
		return m.detail.task.StartOn
	}
	return ""
}

func (m *Model) submitStart(input string) tea.Cmd {
	start, err := app.ParseDue(input, m.deps.Now())
	if err != nil {
		return func() tea.Msg { return writeDoneMsg{err: err} }
	}
	return m.setStart(m.dueTask, start)
}
