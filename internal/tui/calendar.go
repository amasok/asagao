package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/amasok/asana-tui/internal/store"
)

// Calendar view: a month grid of tasks by due date, for My Tasks or a project.
// h/l move a day, j/k a week, n/p a month, "." goes to today, enter lists the day's
// tasks (choose one to open it) and a adds a task due on the selected day.

func (m *Model) calendarActive() bool {
	k := m.currentScope().kind
	return m.calendar && k != scopeRecent && k != scopeInbox && m.query == ""
}

type calendarLoadedMsg struct {
	month time.Time
	tasks map[string][]store.TaskSummary // by due date (YYYY-MM-DD)
	err   error
}

func monthStart(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
}

// calendarRange returns the first and last day shown (whole weeks, Sunday first).
func calendarRange(month time.Time) (time.Time, time.Time) {
	first := monthStart(month)
	start := first.AddDate(0, 0, -int(first.Weekday()))
	last := first.AddDate(0, 1, -1)
	end := last.AddDate(0, 0, 6-int(last.Weekday()))
	return start, end
}

func (m *Model) loadCalendar() tea.Cmd {
	if !m.calendarActive() {
		return nil
	}
	if m.calDay.IsZero() {
		m.calDay = m.deps.Now()
	}
	month := monthStart(m.calDay)
	start, end := calendarRange(month)
	f := store.TaskFilter{DueAfter: start.Format(time.DateOnly), DueBefore: end.Format(time.DateOnly), Tag: m.tagFilter}
	switch m.filter {
	case filterCompleted:
		f.Completed = boolPtr(true)
	case filterAll:
	default:
		f.Completed = boolPtr(false)
	}
	sc, st := m.currentScope(), m.deps.Store
	return func() tea.Msg {
		if sc.kind == scopeProject {
			f.ProjectGID = sc.project
		} else {
			me, err := st.MeGID(m.ctx)
			if err != nil {
				return calendarLoadedMsg{err: err}
			}
			f.AssigneeGID = me
		}
		tasks, err := st.ListTasks(m.ctx, f)
		byDay := map[string][]store.TaskSummary{}
		for _, t := range tasks {
			byDay[t.DueOn] = append(byDay[t.DueOn], t)
		}
		return calendarLoadedMsg{month: month, tasks: byDay, err: err}
	}
}

func (m *Model) onCalendarLoaded(msg calendarLoadedMsg) {
	if msg.err != nil {
		m.status = msg.err.Error()
		return
	}
	m.calMonth, m.calTasks = msg.month, msg.tasks
}

// addMonths moves t by n months, keeping the day but not past the end of the target month
// (Jan 31 + 1 month is Feb 28, not Mar 3 as with AddDate).
func addMonths(t time.Time, n int) time.Time {
	first := time.Date(t.Year(), t.Month()+time.Month(n), 1, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location())
	last := first.AddDate(0, 1, -1).Day()
	return first.AddDate(0, 0, min(t.Day(), last)-1)
}

// moveCalendar moves the selected day and reloads when the month changes.
func (m *Model) moveCalendar(days, months int) tea.Cmd {
	before := monthStart(m.calDay)
	if months != 0 {
		m.calDay = addMonths(m.calDay, months)
	}
	m.calDay = m.calDay.AddDate(0, 0, days)
	if !monthStart(m.calDay).Equal(before) {
		return m.loadCalendar()
	}
	return nil
}

func (m *Model) handleCalendarKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "h", "left":
		return m, m.moveCalendar(-1, 0)
	case "l", "right":
		return m, m.moveCalendar(1, 0)
	case "j", "down":
		return m, m.moveCalendar(7, 0)
	case "k", "up":
		return m, m.moveCalendar(-7, 0)
	case "n", "]":
		return m, m.moveCalendar(0, 1)
	case "p", "[":
		return m, m.moveCalendar(0, -1)
	case ".":
		before := monthStart(m.calDay)
		m.calDay = m.deps.Now()
		if !monthStart(m.calDay).Equal(before) {
			return m, m.loadCalendar()
		}
	case "enter":
		m.openDayPicker()
	}
	return m, nil
}

// openDayPicker lists the selected day's tasks; choosing one opens its detail.
func (m *Model) openDayPicker() {
	day := m.calDay.Format(time.DateOnly)
	tasks := m.calTasks[day]
	if len(tasks) == 0 {
		m.status = longDue(day, "") + " のタスクはありません（a で追加）"
		return
	}
	p := &picker{kind: pickDayTask, title: longDue(day, "") + " のタスク"}
	for _, t := range tasks {
		label := "☐ " + t.Name
		if t.Completed {
			label = "☑ " + t.Name
		}
		hint := ""
		if t.Assignee != nil {
			hint = t.Assignee.Name
		}
		p.items = append(p.items, pickerItem{label: label, value: t.GID, hint: hint})
	}
	m.picker = p
}

// openTaskDetail shows any task full screen (from the calendar or recent updates).
func (m *Model) openTaskDetail(gid string) tea.Cmd {
	m.detailStack = []string{gid}
	m.zoom = true
	m.focus = paneDetail
	m.detailCursor = 0
	return tea.Batch(m.loadDetail(gid), m.debounceDetail(gid))
}

func (m *Model) renderCalendar(w, h int) string {
	if m.calDay.IsZero() {
		m.calDay = m.deps.Now()
	}
	month := monthStart(m.calDay)
	start, end := calendarRange(month)
	weeks := int(end.Sub(start).Hours()/24+1) / 7
	inner := w - 4
	cellW := max(6, (inner-6)/7) // 6 separators
	gridW := cellW*7 + 6
	cellH := max(2, (h-3-2-weeks)/max(1, weeks)) // title, weekday row, rules
	today := m.deps.Now().Format(time.DateOnly)
	selected := m.calDay.Format(time.DateOnly)

	var lines []string
	noDue := ""
	lines = append(lines, m.st.dim.Render("h/l 日  j/k 週  n/p 月  . 今日  enter その日のタスク  a 追加"+noDue))
	var head []string
	for i, wd := range weekdays {
		s := padRight(" "+wd, cellW)
		switch i {
		case 0:
			s = m.st.overdue.Render(s)
		case 6:
			s = m.st.tag.Render(s)
		}
		head = append(head, s)
	}
	lines = append(lines, strings.Join(head, m.st.dim.Render("│")))
	rule := m.st.dim.Render(strings.Repeat("─", gridW))
	for wk := range weeks {
		lines = append(lines, rule)
		cells := make([][]string, 7)
		for wd := range 7 {
			d := start.AddDate(0, 0, wk*7+wd)
			key := d.Format(time.DateOnly)
			label := fmt.Sprintf("%d", d.Day())
			if key == today {
				label += " 今日"
			}
			tasks := m.calTasks[key]
			var cell []string
			style := m.st.label
			if d.Month() != month.Month() {
				style = m.st.dim
			}
			cell = append(cell, style.Render(label))
			for i, t := range tasks {
				if len(cell) == cellH-1 && len(tasks)-i > 1 {
					cell = append(cell, m.st.dim.Render(fmt.Sprintf("他 %d 件", len(tasks)-i)))
					break
				}
				name := t.Name
				if t.Completed {
					name = "☑" + name
				}
				cell = append(cell, ansi.Truncate(name, cellW, "…"))
				if len(cell) >= cellH {
					break
				}
			}
			for len(cell) < cellH {
				cell = append(cell, "")
			}
			for i := range cell {
				cell[i] = padRight(ansi.Truncate(cell[i], cellW, "…"), cellW)
				if key == selected {
					cell[i] = m.highlight(ansi.Strip(cell[i]), cellW, true, m.focus == paneList)
				}
			}
			cells[wd] = cell
		}
		for r := range cellH {
			var row []string
			for wd := range 7 {
				row = append(row, cells[wd][r])
			}
			lines = append(lines, strings.Join(row, m.st.dim.Render("│")))
		}
	}
	title := fmt.Sprintf("カレンダー %d年%d月", month.Year(), int(month.Month()))
	return m.pane(title, lines, w, h, m.focus == paneList)
}
