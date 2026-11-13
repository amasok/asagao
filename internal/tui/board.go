package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/amasok/asana-tui/internal/store"
)

// This file holds the v0.2 views: the board (sections as columns), the zoomed detail view,
// and the picker used to choose a tag filter or a destination section.

// --- board ---

type boardColumn struct {
	gid, name string
	tasks     []*store.TaskSummary
}

const minBoardColumn = 26

// boardActive reports whether the task area shows the board instead of the list.
func (m *Model) boardActive() bool {
	return m.board && m.currentScope().kind == scopeProject && m.query == ""
}

// boardColumns builds one column per section of the project (empty ones included, so cards
// can be moved there), plus "その他" for tasks whose section is unknown.
func (m *Model) boardColumns() []boardColumn {
	sc := m.currentScope()
	cols := make([]boardColumn, 0, len(sc.sections)+1)
	index := map[string]int{}
	for _, s := range sc.sections {
		index[s.GID] = len(cols)
		cols = append(cols, boardColumn{gid: s.GID, name: s.Name})
	}
	var other []*store.TaskSummary
	for _, r := range m.allRows {
		if r.task == nil || r.depth > 0 { // expanded subtasks are not cards
			continue
		}
		placed := false
		for _, ms := range r.task.Memberships {
			if ms.ProjectGID == sc.project {
				if i, ok := index[ms.SectionGID]; ok {
					cols[i].tasks = append(cols[i].tasks, r.task)
					placed = true
				}
			}
		}
		if !placed {
			other = append(other, r.task)
		}
	}
	if len(other) > 0 {
		cols = append(cols, boardColumn{name: "その他", tasks: other})
	}
	return cols
}

func (m *Model) boardSelected() *store.TaskSummary {
	cols := m.boardColumns()
	if m.boardCol < 0 || m.boardCol >= len(cols) {
		return nil
	}
	col := cols[m.boardCol]
	if m.boardRow < 0 || m.boardRow >= len(col.tasks) {
		return nil
	}
	return col.tasks[m.boardRow]
}

// selectInBoard puts the cursor on gid if it is on the board; otherwise clamps the cursor.
func (m *Model) selectInBoard(gid string) {
	cols := m.boardColumns()
	for c, col := range cols {
		for r, t := range col.tasks {
			if t.GID == gid {
				m.boardCol, m.boardRow = c, r
				return
			}
		}
	}
	m.boardCol = clamp(m.boardCol, 0, len(cols)-1)
	if m.boardCol < len(cols) {
		m.boardRow = clamp(m.boardRow, 0, len(cols[m.boardCol].tasks)-1)
	}
}

func (m *Model) handleBoardKey(key string) (tea.Model, tea.Cmd) {
	cols := m.boardColumns()
	if len(cols) == 0 {
		if key == "h" || key == "left" {
			m.focus = paneSidebar
		}
		return m, nil
	}
	prev := m.boardSelected()
	switch key {
	case "h", "left":
		if m.boardCol == 0 {
			m.focus = paneSidebar
			return m, nil
		}
		m.boardCol--
	case "l", "right":
		m.boardCol = min(m.boardCol+1, len(cols)-1)
	case "j", "down":
		m.boardRow++
	case "k", "up":
		m.boardRow--
	case "g", "home":
		m.boardRow = 0
	case "G", "end":
		m.boardRow = len(cols[m.boardCol].tasks) - 1
	case "H", "L":
		t := m.boardSelected()
		if t == nil {
			return m, nil
		}
		target := m.boardCol - 1
		if key == "L" {
			target = m.boardCol + 1
		}
		if target < 0 || target >= len(cols) || cols[target].gid == "" {
			return m, nil
		}
		m.boardCol = target
		return m, m.moveTask(*t, m.currentScope().project, cols[target].gid, cols[target].name)
	case "enter":
		return m, m.openDetail()
	default:
		return m, nil
	}
	m.boardRow = clamp(m.boardRow, 0, len(cols[m.boardCol].tasks)-1)
	if t := m.boardSelected(); t != nil && (prev == nil || prev.GID != t.GID) {
		m.resetDetailNav()
		return m, tea.Batch(m.loadDetail(t.GID), m.debounceDetail(t.GID))
	}
	return m, nil
}

func (m *Model) renderBoard(w, h int) string {
	cols := m.boardColumns()
	if len(cols) == 0 {
		return m.pane("ボード", []string{m.st.dim.Render("セクションがありません")}, w, h, m.focus == paneList)
	}
	visible := clamp(w/minBoardColumn, 1, len(cols))
	if m.boardCol < m.boardColOffset {
		m.boardColOffset = m.boardCol
	}
	if m.boardCol >= m.boardColOffset+visible {
		m.boardColOffset = m.boardCol - visible + 1
	}
	m.boardColOffset = clamp(m.boardColOffset, 0, len(cols)-visible)

	today := m.deps.Now().Format(time.DateOnly)
	var panes []string
	used := 0
	for i := m.boardColOffset; i < m.boardColOffset+visible; i++ {
		colW := w / visible
		if i == m.boardColOffset+visible-1 {
			colW = w - used // give the remainder to the last column
		}
		used += colW
		col := cols[i]
		inner := colW - 4
		rows := h - 3
		offset := 0
		if i == m.boardCol && m.boardRow >= rows {
			offset = m.boardRow - rows + 1
		}
		var lines []string
		for r := offset; r < len(col.tasks) && len(lines) < rows; r++ {
			lines = append(lines, m.cardLine(col.tasks[r], inner, today, i == m.boardCol && r == m.boardRow))
		}
		if len(col.tasks) == 0 {
			lines = []string{m.st.dim.Render("（なし）")}
		}
		title := fmt.Sprintf("%s (%d)", col.name, len(col.tasks))
		if i == m.boardColOffset && i > 0 {
			title = "‹ " + title
		}
		if i == m.boardColOffset+visible-1 && i < len(cols)-1 {
			title += " ›"
		}
		panes = append(panes, m.pane(title, lines, colW, h, m.focus == paneList && i == m.boardCol))
	}
	return strings.Join(joinColumns(panes), "\n")
}

func (m *Model) cardLine(t *store.TaskSummary, w int, today string, selected bool) string {
	check := "☐"
	if t.Completed {
		check = "☑"
	}
	due := shortDue(t.DueOn, m.deps.Now())
	name := t.Name
	if t.Pending {
		name += " ↑"
	}
	nameW := max(4, w-2-ansi.StringWidth(due)-1)
	line := check + " " + padRight(ansi.Truncate(name, nameW, "…"), nameW)
	if due != "" {
		line += " " + due
	}
	if selected {
		return m.highlight(line, w, true, m.focus == paneList)
	}
	switch {
	case t.Completed:
		return m.st.done.Render(line)
	case t.DueOn != "" && t.DueOn < today:
		return check + " " + padRight(ansi.Truncate(name, nameW, "…"), nameW) + " " + m.st.overdue.Render(due)
	}
	return line
}

// joinColumns places rendered panes side by side line by line.
func joinColumns(panes []string) []string {
	split := make([][]string, len(panes))
	height := 0
	for i, p := range panes {
		split[i] = strings.Split(p, "\n")
		height = max(height, len(split[i]))
	}
	out := make([]string, height)
	for i := range out {
		var b strings.Builder
		for _, lines := range split {
			if i < len(lines) {
				b.WriteString(lines[i])
			}
		}
		out[i] = b.String()
	}
	return out
}

// --- picker (tag filter / move destination) ---

type pickerKind int

const (
	pickTag pickerKind = iota
	pickSection
	pickAssignee
	pickDue
	pickFilter
	pickTags
	pickDayTask
)

type pickerItem struct {
	label, value, hint string
	checked            bool // pickTags: the task has this tag
}

type picker struct {
	kind   pickerKind
	title  string
	items  []pickerItem
	cursor int
	// for pickSection
	task    store.TaskSummary
	project string
}

type tagsLoadedMsg struct {
	tags []store.TagCount
	err  error
}

func (m *Model) openTagPicker() tea.Cmd {
	st := m.deps.Store
	return func() tea.Msg {
		tags, err := st.ListTags(m.ctx)
		return tagsLoadedMsg{tags: tags, err: err}
	}
}

func (m *Model) showTagPicker(tags []store.TagCount) {
	p := &picker{kind: pickTag, title: "タグで絞り込む", items: []pickerItem{{label: "（すべてのタスク）"}}}
	for i, t := range tags {
		p.items = append(p.items, pickerItem{label: "#" + t.Name, value: t.Name, hint: fmt.Sprintf("未完了 %d", t.Open)})
		if t.Name == m.tagFilter {
			p.cursor = i + 1
		}
	}
	m.picker = p
}

// openSectionPicker lets the user choose a section to move the selected task to.
func (m *Model) openSectionPicker() {
	t := m.actionTask()
	if t == nil {
		return
	}
	project := ""
	if sc := m.currentScope(); sc.kind == scopeProject {
		project = sc.project
	} else if len(t.Memberships) == 1 {
		project = t.Memberships[0].ProjectGID
	}
	if project == "" {
		m.status = "どのプロジェクトのセクションに移動するか決められません（プロジェクトを開いてから m を押してください）"
		return
	}
	var sections []store.SectionView
	var projectName string
	for _, sc := range m.scopes {
		if sc.project == project {
			sections, projectName = sc.sections, sc.name
		}
	}
	current := ""
	for _, ms := range t.Memberships {
		if ms.ProjectGID == project {
			current = ms.SectionGID
		}
	}
	p := &picker{kind: pickSection, title: "移動先のセクション（" + projectName + "）", task: *t, project: project}
	for i, s := range sections {
		item := pickerItem{label: s.Name, value: s.GID}
		if s.GID == current {
			item.hint = "現在"
			p.cursor = i
		}
		p.items = append(p.items, item)
	}
	if len(p.items) == 0 {
		m.status = "このプロジェクトにはセクションがありません"
		return
	}
	m.picker = p
}

func (m *Model) handlePickerKey(key string) (tea.Model, tea.Cmd) {
	p := m.picker
	switch key {
	case "esc", "q":
		m.picker = nil
	case "j", "down", "ctrl+n":
		p.cursor = min(p.cursor+1, len(p.items)-1)
	case "k", "up", "ctrl+p":
		p.cursor = max(p.cursor-1, 0)
	case "g", "home":
		p.cursor = 0
	case "G", "end":
		p.cursor = len(p.items) - 1
	case "enter", "space":
		item := p.items[p.cursor]
		if p.kind == pickTags {
			if item.value == "+" {
				m.picker = nil
				return m, m.startNewTag(p.task)
			}
			return m, m.toggleTag(p.task, item) // stays open
		}
		if key == "space" {
			return m, nil
		}
		m.picker = nil
		switch p.kind {
		case pickDayTask:
			return m, m.openTaskDetail(item.value)
		case pickDue:
			if item.value == "?" {
				return m, m.startDuePrompt(p.task)
			}
			return m, m.setDue(p.task, item.value)
		case pickFilter:
			var f int
			fmt.Sscan(item.value, &f)
			m.filter = filterMode(f)
			return m, m.loadTasks()
		case pickTag:
			m.tagFilter = item.value
			m.listCursor, m.listOffset = 0, 0
			return m, m.loadTasks()
		case pickSection:
			return m, m.moveTask(p.task, p.project, item.value, item.label)
		case pickAssignee:
			return m, m.assign(p.task, item.value, item.label)
		}
	}
	return m, nil
}

func (m *Model) renderPicker(w, h int) string {
	p := m.picker
	lines := []string{m.st.dim.Render("j/k 移動   enter 決定   esc 取り消し"), ""}
	for i, it := range p.items {
		line := it.label
		if it.hint != "" {
			line += "  " + m.st.dim.Render(it.hint)
		}
		lines = append(lines, m.highlight(line, w-4, i == p.cursor, true))
	}
	// keep the cursor visible
	if extra := p.cursor + 2 - (h - 4); extra > 0 {
		lines = append(lines[:2], lines[2+extra:]...)
	}
	return m.pane(p.title, lines, w, h, true)
}

func (m *Model) moveTask(t store.TaskSummary, project, section, sectionName string) tea.Cmd {
	st, now := m.deps.Store, m.deps.Now()
	return func() tea.Msg {
		if err := st.MoveTaskLocal(m.ctx, t.GID, project, section, now); err != nil {
			return writeDoneMsg{err: err}
		}
		return writeDoneMsg{status: fmt.Sprintf("移動しました: %s → %s", t.Name, sectionName), gid: t.GID}
	}
}

// --- keys that switch views ---

func (m *Model) viewKey(key string) tea.Cmd {
	switch key {
	case "z":
		if m.zoom {
			m.zoom = false
			m.focus = paneList
		} else if m.selectedTask() != nil {
			m.zoom = true
			m.focus = paneDetail
		}
	case "v":
		// list -> board (projects only) -> calendar -> list
		if m.query != "" {
			m.status = "検索中は表示を切り替えられません（esc で検索を解除）"
			return nil
		}
		if m.currentScope().kind == scopeRecent {
			m.status = "最近の更新はリスト表示だけです"
			return nil
		}
		selected := m.selectedTask()
		switch {
		case m.calendar:
			m.calendar, m.board = false, false
		case m.board || m.currentScope().kind != scopeProject:
			m.calendar, m.board = true, false
		default:
			m.board = true
		}
		m.focus = paneList
		if selected != nil {
			if m.boardActive() {
				m.selectInBoard(selected.GID)
			} else {
				m.selectInList(selected.GID)
			}
		}
		if m.calendarActive() {
			return m.loadCalendar()
		}
	case "t":
		return m.openTagPicker()
	case "m":
		if m.focus != paneSidebar {
			m.openSectionPicker()
		}
	}
	return nil
}
