package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/amasok/asagao/internal/readmodel"
)

// Timeline view (Issue #58): the project's list rows (sections, tasks and the subtasks
// expanded with l, indented as in a WBS) with a Gantt chart on the right. A task is a bar
// from its start date to its due date (one day when it has no start date), a milestone a
// ◆. The tasks the selected task waits for are marked 前 and the tasks waiting for it 後;
// other tasks still waiting for something are marked 待. It is read only for now: the
// keys of the list work as in the list, and [ ] scroll a week, { } change the width of a
// day, . goes back to today.

const (
	tlDefaultDayW = 2
	tlMaxDayW     = 4
	tlGutterW     = 2 // 前 / 後 / 待 (wide characters)
	tlLeadDays    = 7 // days shown before today by default
)

func (m *Model) timelineActive() bool {
	return m.timeline && m.currentScope().kind == scopeProject && m.query == ""
}

// tlDayWidth is the number of columns of one day.
func (m *Model) tlDayWidth() int {
	if m.tlDayW <= 0 {
		return tlDefaultDayW
	}
	return min(m.tlDayW, tlMaxDayW)
}

// dayOf is t's calendar day (in t's location) as midnight UTC, so that days can be
// counted without daylight saving time getting in the way.
func dayOf(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func daysBetween(a, b time.Time) int { return int(b.Sub(a).Hours() / 24) }

// tlOrigin is the first day shown.
func (m *Model) tlOrigin() time.Time {
	if m.tlStart.IsZero() {
		return dayOf(m.deps.Now()).AddDate(0, 0, -tlLeadDays)
	}
	return m.tlStart
}

// taskSpan returns the days a task's bar covers: start date to due date, only the due
// date without a start date (or only the start date without a due date). ok is false
// when the task has neither.
func taskSpan(t *readmodel.TaskSummary, loc *time.Location) (start, end time.Time, ok bool) {
	parse := func(s string) (time.Time, bool) {
		d, err := time.Parse(time.DateOnly, s)
		return d, err == nil
	}
	due, hasDue := parse(t.DueOn)
	if !hasDue && t.DueAt != "" {
		if at, err := time.Parse(time.RFC3339, t.DueAt); err == nil {
			due, hasDue = dayOf(at.In(loc)), true
		}
	}
	st, hasStart := parse(t.StartOn)
	switch {
	case hasDue && hasStart:
		if st.After(due) {
			st = due
		}
		return st, due, true
	case hasDue:
		return due, due, true
	case hasStart:
		return st, st, true
	}
	return time.Time{}, time.Time{}, false
}

func isMilestone(t *readmodel.TaskSummary) bool { return t.ResourceSubtype == "milestone" }

// handleTimelineKey handles the keys the timeline adds to (or changes in) the list.
func (m *Model) handleTimelineKey(key string) (tea.Cmd, bool) {
	switch key {
	case "[":
		m.tlStart = m.tlOrigin().AddDate(0, 0, -7)
	case "]":
		m.tlStart = m.tlOrigin().AddDate(0, 0, 7)
	case "{":
		m.tlDayW = max(1, m.tlDayWidth()-1)
	case "}":
		m.tlDayW = min(tlMaxDayW, m.tlDayWidth()+1)
	case ".":
		m.tlStart = time.Time{}
	case "l", "right":
		// The detail pane is hidden: open it full screen when there is nothing to expand.
		if t := m.selectedTask(); t != nil && (t.NumSubtasks == 0 || m.expanded[t.GID]) {
			return m.openDetail(), true
		}
		return nil, false
	default:
		return nil, false
	}
	return nil, true
}

// tlLayout is how the timeline's width is shared.
type tlLayout struct {
	nameW, ganttW int // ganttW 0: names only (too narrow for the chart)
	dayW, days    int // columns per day, days shown
}

func timelineLayout(inner, dayW int) tlLayout {
	l := tlLayout{nameW: inner}
	nameW := clamp(inner*2/5, 10, 36)
	ganttW := inner - nameW - tlGutterW - 1
	if ganttW < 8 {
		nameW = max(4, inner-tlGutterW-1-8)
		ganttW = inner - nameW - tlGutterW - 1
	}
	if ganttW < 1 {
		return l
	}
	l.nameW, l.ganttW = nameW, ganttW
	l.dayW = clamp(dayW, 1, max(1, ganttW/7)) // at least a week
	l.days = ganttW / l.dayW
	return l
}

// tlFollowSelection scrolls so that the selected task's bar can be seen, once each time
// another task is selected (so [ and ] can still scroll away from it).
func (m *Model) tlFollowSelection(l tlLayout) {
	t := m.selectedTask()
	if t == nil || l.days == 0 {
		return
	}
	if t.GID == m.tlFollow {
		return
	}
	m.tlFollow = t.GID
	start, end, ok := taskSpan(t, m.deps.Now().Location())
	if !ok {
		return
	}
	origin := m.tlOrigin()
	last := origin.AddDate(0, 0, l.days-1)
	if !end.Before(origin) && !start.After(last) {
		return // (partly) visible
	}
	lead := min(tlLeadDays, l.days/4)
	m.tlStart = start.AddDate(0, 0, -lead)
}

// tlRelations are the tasks the selected task waits for (前) and that wait for it (後),
// from its detail once loaded.
func (m *Model) tlRelations() (before, after map[string]bool) {
	t := m.selectedTask()
	if t == nil || m.detail == nil || m.detail.GID != t.GID {
		return nil, nil
	}
	before, after = map[string]bool{}, map[string]bool{}
	for _, d := range m.detail.Dependencies {
		before[d.GID] = true
	}
	for _, d := range m.detail.Dependents {
		after[d.GID] = true
	}
	return before, after
}

func (m *Model) renderTimeline(w, h int) string {
	inner := max(1, w-4)
	l := timelineLayout(inner, m.tlDayWidth())
	m.tlFollowSelection(l)
	origin := m.tlOrigin()
	today := dayOf(m.deps.Now())
	focused := m.focus == paneList

	title := "タイムライン"
	if l.days > 0 {
		last := origin.AddDate(0, 0, l.days-1)
		title += fmt.Sprintf(" %d/%d/%d〜", origin.Year(), int(origin.Month()), origin.Day())
		if last.Year() != origin.Year() {
			title += fmt.Sprintf("%d/", last.Year())
		}
		title += fmt.Sprintf("%d/%d", int(last.Month()), last.Day())
	}

	var lines []string
	pad := strings.Repeat(" ", l.nameW+tlGutterW)
	if l.ganttW > 0 {
		lines = append(lines,
			pad+m.st.dim.Render("│")+m.tlMonthLine(l, origin),
			pad+m.st.dim.Render("│")+m.tlDayLine(l, origin, today))
	}
	if len(m.rows) == 0 {
		msg := "該当するタスクはありません"
		if m.syncing && m.lastSync.IsZero() {
			msg = "同期しています…"
		}
		return m.pane(title, append(lines, m.st.dim.Render(msg)), w, h, focused)
	}
	before, after := m.tlRelations()
	rowsH := max(1, h-3-len(lines))
	offset := m.listOffset
	if m.listCursor >= offset+rowsH { // e.g. the window got shorter
		offset = m.listCursor - rowsH + 1
	}
	offset = clamp(offset, 0, max(0, len(m.rows)-1))
	end := min(len(m.rows), offset+rowsH)
	for i := offset; i < end; i++ {
		r := m.rows[i]
		selected := i == m.listCursor
		var name, gutter string
		var t *readmodel.TaskSummary
		switch {
		case r.task != nil:
			t = r.task
			if mark, ok := m.markGutter(t); ok && l.nameW > 2 { // V: marked for a bulk action
				name = mark + m.tlNameCell(t, r.depth, l.nameW-2, selected, focused)
			} else {
				name = m.tlNameCell(t, r.depth, l.nameW, selected, focused)
			}
			switch {
			case before[t.GID]:
				gutter = m.st.tag.Render("前")
			case after[t.GID]:
				gutter = m.st.tag.Render("後")
			case t.OpenDependencies > 0 && !t.Completed:
				gutter = m.st.dim.Render("待")
			}
		case r.isHeader():
			header := "▾ " + r.header
			if r.collapsed {
				header = fmt.Sprintf("▸ %s (%d)", r.header, r.count)
			}
			if selected {
				name = m.highlight(header, l.nameW, true, focused)
			} else {
				name = m.st.sectionHeader.Render(padRight(ansi.Truncate(header, l.nameW, "…"), l.nameW))
			}
		}
		line := padRight(name, l.nameW) + padRight(gutter, tlGutterW)
		if l.ganttW > 0 {
			hl := before[gidOf(t)] || after[gidOf(t)]
			line += m.st.dim.Render("│") + m.tlBar(t, l, origin, today, hl)
		}
		lines = append(lines, line)
	}
	return m.pane(title, lines, w, h, focused)
}

func gidOf(t *readmodel.TaskSummary) string {
	if t == nil {
		return ""
	}
	return t.GID
}

// tlNameCell is the left column of a task row: indented under its parent like the list.
func (m *Model) tlNameCell(t *readmodel.TaskSummary, depth, w int, selected, focused bool) string {
	mark := "☐"
	switch {
	case isMilestone(t):
		mark = "◆"
	case t.Completed:
		mark = "☑"
	}
	if depth > 0 {
		mark = strings.Repeat("  ", depth-1) + "└ " + mark
	}
	s := mark + " " + t.Name
	// The list's marks (an approval's status, a custom type and its status, what it waits
	// for), but the milestone's ◆, which is the mark here.
	if badges := strings.TrimSpace(strings.TrimPrefix(readmodel.TaskBadges(*t), "◆")); badges != "" {
		s += " " + badges
	}
	if t.NumSubtasks > 0 {
		if m.expanded[t.GID] {
			s += fmt.Sprintf(" ▾%d", t.NumSubtasks)
		} else {
			s += fmt.Sprintf(" ↳%d", t.NumSubtasks)
		}
	}
	if t.Pending {
		s += " ↑"
	}
	s = padRight(ansi.Truncate(s, w, "…"), w)
	if selected {
		return m.highlight(s, w, true, focused)
	}
	if t.Completed {
		return m.st.dim.Render(s)
	}
	return s
}

// tlMonthLine labels the first day shown and the first day of each month.
func (m *Model) tlMonthLine(l tlLayout, origin time.Time) string {
	var b strings.Builder
	col := 0
	for i := range l.days {
		d := origin.AddDate(0, 0, i)
		if i != 0 && d.Day() != 1 {
			continue
		}
		label := fmt.Sprintf("%d月", int(d.Month()))
		if i == 0 || d.Month() == time.January {
			label = fmt.Sprintf("%d年%d月", d.Year(), int(d.Month()))
		}
		at := i * l.dayW
		if at < col {
			continue
		}
		b.WriteString(strings.Repeat(" ", at-col))
		b.WriteString(label)
		col = at + ansi.StringWidth(label) + 1
		b.WriteString(" ")
	}
	return m.st.label.Render(padRight(ansi.Truncate(b.String(), l.ganttW, ""), l.ganttW))
}

// tlDayLine numbers the days: every day when a day is 3 columns or wider, otherwise the
// Mondays. Today is always numbered (in the today color).
func (m *Model) tlDayLine(l tlLayout, origin, today time.Time) string {
	buf := []rune(strings.Repeat(" ", l.ganttW))
	put := func(i int) {
		d := origin.AddDate(0, 0, i)
		s := strconv.Itoa(d.Day())
		for j, r := range s {
			if at := i*l.dayW + j; at < len(buf) {
				buf[at] = r
			}
		}
	}
	for i := range l.days {
		if l.dayW >= 3 || origin.AddDate(0, 0, i).Weekday() == time.Monday {
			put(i)
		}
	}
	ti := daysBetween(origin, today)
	if ti < 0 || ti >= l.days {
		return m.st.dim.Render(string(buf))
	}
	s := strconv.Itoa(today.Day())
	at := ti * l.dayW
	// clear the label that today's number may run into
	for j := at; j < min(len(buf), at+len(s)+1); j++ {
		buf[j] = ' '
	}
	stop := min(len(buf), at+len(s))
	return m.st.dim.Render(string(buf[:at])) + m.st.today.Render(s[:stop-at]) + m.st.dim.Render(string(buf[stop:]))
}

// tlCell kinds
const (
	tlEmpty = iota
	tlToday
	tlBar
	tlDone
	tlLate
	tlRel
	tlArrow
)

// tlBar draws the chart part of a row: the task's bar or ◆ (nil t: a section header,
// only the today line). hl marks a task related to the selected one.
func (m *Model) tlBar(t *readmodel.TaskSummary, l tlLayout, origin, today time.Time, hl bool) string {
	chars := []rune(strings.Repeat(" ", l.ganttW))
	kinds := make([]int, l.ganttW)
	if ti := daysBetween(origin, today); ti >= 0 && ti < l.days {
		chars[ti*l.dayW], kinds[ti*l.dayW] = '│', tlToday
	}
	if t != nil {
		if start, end, ok := taskSpan(t, m.deps.Now().Location()); ok {
			kind := tlBar
			switch {
			case hl:
				kind = tlRel
			case t.Completed:
				kind = tlDone
			case end.Before(today):
				kind = tlLate
			}
			fill := '█'
			if t.Completed {
				fill = '░'
			}
			s, e := daysBetween(origin, start), daysBetween(origin, end)
			switch {
			case e < 0:
				chars[0], kinds[0] = '◀', tlArrow
			case s >= l.days:
				chars[l.ganttW-1], kinds[l.ganttW-1] = '▶', tlArrow
			case isMilestone(t) && e >= l.days:
				chars[l.ganttW-1], kinds[l.ganttW-1] = '▶', tlArrow
			case isMilestone(t):
				chars[e*l.dayW], kinds[e*l.dayW] = '◆', kind
			default:
				for c := max(0, s) * l.dayW; c < min(l.days, e+1)*l.dayW; c++ {
					chars[c], kinds[c] = fill, kind
				}
			}
		}
	}
	var b strings.Builder
	for i := 0; i < len(chars); {
		j := i
		for j < len(chars) && kinds[j] == kinds[i] {
			j++
		}
		seg := string(chars[i:j])
		switch kinds[i] {
		case tlToday:
			seg = m.st.today.Render(seg)
		case tlBar:
			seg = m.st.paneTitle.UnsetBold().Render(seg)
		case tlDone, tlArrow:
			seg = m.st.dim.Render(seg)
		case tlLate:
			seg = m.st.overdue.Render(seg)
		case tlRel:
			seg = m.st.tag.Render(seg)
		}
		b.WriteString(seg)
		i = j
	}
	return b.String()
}
