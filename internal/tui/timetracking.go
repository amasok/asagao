package tui

import (
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/readmodel"
)

// The time tracking entries of a task (#6): a list with the total, read from the cache and
// then from Asana. enter on "記録する" asks for the time ("1h30m 昨日"); enter on an entry
// offers to change it or to delete it (asked first). Asana offers time tracking on paid
// plans only: once it refused it (402) the menu item is hidden (planLoadedMsg).

// timeEntriesMsg is a task's entries as the cache has them; fetched is set once they were
// read from Asana (fetchErr: why not).
type timeEntriesMsg struct {
	task     readmodel.TaskSummary
	entries  readmodel.TimeEntries
	err      error
	fetched  bool
	fetchErr error
}

func (m *Model) timeTracking(now time.Time) *app.TimeTracking {
	return &app.TimeTracking{Store: m.deps.Store, Clock: func() time.Time { return now }}
}

func (m *Model) openTimeEntries() tea.Cmd {
	t := m.actionTask()
	if t == nil {
		return nil
	}
	return m.loadTimeEntries(*t, true)
}

// loadTimeEntries shows task's entries from the cache and, with fetch, again once they were
// read from Asana.
func (m *Model) loadTimeEntries(task readmodel.TaskSummary, fetch bool) tea.Cmd {
	tracking, syncer := m.timeTracking(m.deps.Now()), m.syncer
	load := func(fetched bool, fetchErr error) tea.Msg {
		es, err := tracking.List(m.ctx, task.GID)
		return timeEntriesMsg{task: task, entries: es, err: err, fetched: fetched, fetchErr: fetchErr}
	}
	cached := func() tea.Msg { return load(false, nil) }
	if !fetch || syncer == nil {
		return cached
	}
	return tea.Batch(cached, func() tea.Msg {
		return load(true, syncer.FetchTimeEntries(m.ctx, task.GID))
	})
}

func (m *Model) updateTimeTracking(msg tea.Msg) (tea.Cmd, bool) {
	if w, ok := msg.(timeWrittenMsg); ok {
		_, cmd, _ := m.updateWrite(w.done)
		return tea.Batch(cmd, m.loadTimeEntries(w.task, false)), true
	}
	msg2, ok := msg.(timeEntriesMsg)
	if !ok {
		return nil, false
	}
	open := m.picker != nil && m.picker.kind == pickTimeEntries && m.timeTask.GID == msg2.task.GID
	switch {
	case app.IsPlanUnavailable(msg2.fetchErr):
		if open {
			m.picker = nil
		}
		m.status = msg2.fetchErr.Error()
		return m.loadPlan(), true // the menu item goes
	case msg2.err != nil:
		m.status = "作業時間を読み込めませんでした: " + msg2.err.Error()
		return nil, true
	case msg2.fetched && !open:
		return nil, true // closed meanwhile
	case !msg2.fetched && m.picker != nil && !open:
		return nil, true // something else was opened meanwhile
	}
	m.showTimeEntries(msg2.task, msg2.entries)
	if msg2.fetchErr != nil {
		m.picker.title += "（Asana から読めなかったため、キャッシュの内容）"
	}
	return nil, true
}

func (m *Model) showTimeEntries(task readmodel.TaskSummary, es readmodel.TimeEntries) {
	cursor := 0
	if m.picker != nil && m.picker.kind == pickTimeEntries {
		cursor = m.picker.cursor
	}
	m.timeTask = task
	title := fmt.Sprintf("作業時間（%s）  合計 %s（%d 件）", truncateRunes(task.Name, 30), es.Total, len(es.Entries))
	items := []pickerItem{{label: "＋ 作業時間を記録する…", value: "+"}}
	for _, e := range es.Entries {
		day := e.EnteredOn
		if d, err := time.Parse(time.DateOnly, e.EnteredOn); err == nil {
			day = fmt.Sprintf("%s(%s)", d.Format("2006-01-02"), weekdays[d.Weekday()])
		}
		who := ""
		if e.Assignee != nil {
			who = e.Assignee.Name
		} else if e.CreatedBy != nil {
			who = e.CreatedBy.Name
		}
		label := padRight(day, 15) + padRight(e.Duration, 8) + who
		if e.Project != "" {
			label += "  " + e.Project
		}
		hint := ""
		if e.Pending {
			hint = "未送信"
		}
		items = append(items, pickerItem{label: label, value: e.GID, hint: hint})
	}
	m.picker = &picker{kind: pickTimeEntries, title: title, items: items, task: task, cursor: min(cursor, len(items)-1)}
}

// handleTimePicker runs the choice made in the pickers of this file.
func (m *Model) handleTimePicker(p *picker, item pickerItem) (tea.Cmd, bool) {
	switch p.kind {
	case pickTimeEntries:
		if item.value == "+" {
			m.openPrompt(promptTimeAdd)
			return m.prompt.Focus(), true
		}
		m.picker = &picker{kind: pickTimeAction, title: "作業時間 " + strings.TrimSpace(item.label), task: p.task,
			items: []pickerItem{
				{label: "時間・日を直す…", value: "edit\x00" + item.value},
				{label: "削除する…", value: "delete\x00" + item.value},
				{label: "戻る", value: "back"},
			}}
		return nil, true
	case pickTimeAction:
		action, gid, _ := strings.Cut(item.value, "\x00")
		switch action {
		case "edit":
			e, err := m.timeTracking(m.deps.Now()).Entry(m.ctx, gid)
			if err != nil {
				m.status = err.Error()
				return nil, true
			}
			m.openPrompt(promptTimeEdit)
			m.timeEntry = gid
			m.prompt.SetValue(e.Duration + " " + e.EnteredOn)
			m.prompt.CursorEnd()
			return m.prompt.Focus(), true
		case "delete":
			m.picker = &picker{kind: pickTimeDelete, title: "この作業時間の記録を削除しますか？（Asana に送ったあとは取り消せません）", task: p.task,
				items: []pickerItem{{label: "やめる", value: "back"}, {label: "削除する", value: gid}}}
			return nil, true
		}
		return m.loadTimeEntries(p.task, false), true // 戻る
	case pickTimeDelete:
		if item.value == "back" {
			return m.loadTimeEntries(p.task, false), true
		}
		tracking, task, gid := m.timeTracking(m.deps.Now()), p.task, item.value
		return m.timeWrite(task, func() error { return tracking.Delete(m.ctx, gid) }, "作業時間の記録を削除しました"), true
	}
	return nil, false
}

// submitTime records (promptTimeAdd) or changes (promptTimeEdit) an entry from what was
// typed: the time, then optionally the day.
func (m *Model) submitTime(kind promptKind, input string) tea.Cmd {
	duration, day, _ := strings.Cut(strings.TrimSpace(input), " ")
	day = strings.TrimSpace(day)
	tracking, task := m.timeTracking(m.deps.Now()), m.timeTask
	if kind == promptTimeEdit {
		gid := m.timeEntry
		return m.timeWrite(task, func() error { return tracking.Edit(m.ctx, gid, duration, day) }, "作業時間を直しました")
	}
	return m.timeWrite(task, func() error {
		_, err := tracking.Add(m.ctx, task.GID, duration, day)
		return err
	}, "作業時間を記録しました: "+duration)
}

// timeWrittenMsg is a write of an entry done (as writeDoneMsg); the task's entries are
// shown again after it.
type timeWrittenMsg struct {
	done writeDoneMsg
	task readmodel.TaskSummary
}

// timeWrite runs write and shows the task's entries again.
func (m *Model) timeWrite(task readmodel.TaskSummary, write func() error, done string) tea.Cmd {
	return func() tea.Msg {
		if err := write(); err != nil {
			if fe := (*app.FieldError)(nil); errors.As(err, &fe) {
				err = fe.Err // the time or the day, as typed
			}
			return timeWrittenMsg{done: writeDoneMsg{err: err}, task: task}
		}
		return timeWrittenMsg{done: writeDoneMsg{status: done}, task: task}
	}
}
