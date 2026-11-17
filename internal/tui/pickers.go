package tui

import (
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/readmodel"
)

// Choosers for fields that have a fixed set of values, so nothing has to be typed:
// due date (common dates + "日付を入力…"), the list filter and the task's tags.

// --- due date ---

func (m *Model) openDuePicker() tea.Cmd {
	t := m.actionTask()
	if t == nil {
		return nil
	}
	p := &picker{kind: pickDue, title: "期日（" + truncateRunes(t.Name, 30) + "）", task: *t, items: dueItems(m.deps.Now())}
	for i, item := range p.items {
		if item.value == t.DueOn && item.value != "?" {
			p.items[i].hint = "現在"
			p.cursor = i
		}
	}
	m.picker = p
	return nil
}

// dueItems are the choices of the due date picker: common dates, "日付を入力…" (value "?")
// and "期日なし" (value "").
func dueItems(now time.Time) []pickerItem {
	day := func(d time.Time) string { return d.Format(time.DateOnly) }
	label := func(name string, d time.Time) string {
		return padRight(name, 12) + fmt.Sprintf("%s(%s)", d.Format("1/2"), weekdays[d.Weekday()])
	}
	saturday := now.AddDate(0, 0, (int(time.Saturday)-int(now.Weekday())+7)%7)
	monday := now.AddDate(0, 0, (int(time.Monday)-int(now.Weekday())+7)%7)
	if monday.Format(time.DateOnly) == now.Format(time.DateOnly) {
		monday = monday.AddDate(0, 0, 7)
	}
	options := []struct {
		name string
		d    time.Time
	}{
		{"今日", now}, {"明日", now.AddDate(0, 0, 1)}, {"明後日", now.AddDate(0, 0, 2)},
		{"今週末", saturday}, {"来週の月曜", monday}, {"1週間後", now.AddDate(0, 0, 7)},
		{"2週間後", now.AddDate(0, 0, 14)}, {"1か月後", now.AddDate(0, 1, 0)},
	}
	var items []pickerItem
	for _, o := range options {
		items = append(items, pickerItem{label: label(o.name, o.d), value: day(o.d)})
	}
	return append(items, pickerItem{label: "日付を入力…", value: "?"}, pickerItem{label: "期日なし", value: ""})
}

func (m *Model) setDue(t readmodel.TaskSummary, due string) tea.Cmd {
	status := "期日を " + longDue(due, "") + " にしました"
	if due == "" {
		status = "期日をなしにしました: " + t.Name
	}
	return m.updateTask(t, app.TaskUpdate{Due: &due}, status)
}

func (m *Model) startDuePrompt(t readmodel.TaskSummary) tea.Cmd {
	m.openPrompt(promptDue)
	m.dueTask, m.bulkDueTasks = t, nil
	m.prompt.SetValue(t.DueOn)
	m.prompt.CursorEnd()
	return m.prompt.Focus()
}

func (m *Model) submitDue(input string) tea.Cmd {
	due, err := app.ParseDue(input, m.deps.Now())
	if err != nil {
		return func() tea.Msg { return writeDoneMsg{err: err} }
	}
	if tasks := m.bulkDueTasks; tasks != nil {
		m.bulkDueTasks = nil
		return m.askDue(tasks, due)
	}
	return m.setDue(m.dueTask, due)
}

// --- list filter ---

func (m *Model) openFilterPicker() tea.Cmd {
	p := &picker{kind: pickFilter, title: "絞り込み", cursor: int(m.filter)}
	for f := range filterCount {
		item := pickerItem{label: f.String(), value: fmt.Sprint(int(f))}
		if f == m.filter {
			item.hint = "現在"
		}
		p.items = append(p.items, item)
	}
	m.picker = p
	return nil
}

// --- tags of a task (multi-select) ---

type taskTagsMsg struct {
	task readmodel.TaskSummary
	all  []readmodel.TagCount
	has  map[string]bool
	err  error
}

func (m *Model) openTagEditor() tea.Cmd {
	t := m.actionTask()
	if t == nil {
		return nil
	}
	return m.openTagEditorFor(*t)
}

func (m *Model) openTagEditorFor(task readmodel.TaskSummary) tea.Cmd {
	st := m.deps.Store
	return func() tea.Msg {
		all, err := queryOf(st).ListTags(m.ctx)
		if err != nil {
			return taskTagsMsg{err: err}
		}
		mine, err := queryOf(st).TaskTags(m.ctx, task.GID)
		has := map[string]bool{}
		for _, r := range mine {
			has[r.GID] = true
		}
		return taskTagsMsg{task: task, all: all, has: has, err: err}
	}
}

func (m *Model) showTagEditor(msg taskTagsMsg) {
	cursor := 0
	if m.picker != nil && m.picker.kind == pickTags {
		cursor = m.picker.cursor // refresh in place after a toggle
	}
	p := &picker{kind: pickTags, title: "タグ（" + truncateRunes(msg.task.Name, 30) + "）  enter / space で付け外し・esc で閉じる", task: msg.task, cursor: cursor}
	for _, tag := range msg.all {
		mark := "[ ]"
		if msg.has[tag.GID] {
			mark = "[✓]"
		}
		p.items = append(p.items, pickerItem{label: mark + " #" + tag.Name, value: tag.GID, checked: msg.has[tag.GID]})
	}
	p.items = append(p.items, pickerItem{label: "＋ 新しいタグを作る…", value: "+"})
	p.cursor = min(p.cursor, len(p.items)-1)
	m.picker = p
}

func (m *Model) toggleTag(t readmodel.TaskSummary, item pickerItem) tea.Cmd {
	relations := m.relations(m.deps.Now())
	return func() tea.Msg {
		if err := relations.SetTag(m.ctx, t.GID, item.value, !item.checked); err != nil {
			return writeDoneMsg{err: err}
		}
		return tagToggledMsg{task: t}
	}
}

type tagToggledMsg struct{ task readmodel.TaskSummary }

func (m *Model) startNewTag(t readmodel.TaskSummary) tea.Cmd {
	m.openPrompt(promptNewTag)
	m.tagTask = t
	return m.prompt.Focus()
}

func (m *Model) submitNewTag(name string) tea.Cmd {
	relations, t := m.relations(m.deps.Now()), m.tagTask
	return func() tea.Msg {
		if err := relations.AddNewTag(m.ctx, t.GID, name); err != nil {
			return writeDoneMsg{err: err}
		}
		return writeDoneMsg{status: "タグを付けました: #" + name}
	}
}
