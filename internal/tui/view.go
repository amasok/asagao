package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	localgid "github.com/amasok/asagao/internal/domain/gid"
	"github.com/amasok/asagao/internal/domain/task"
	"github.com/amasok/asagao/internal/readmodel"
)

const (
	sidebarWidth = 28
	minListWidth = 30
)

func (m *Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.WindowTitle = "asagao"
	return v
}

func (m *Model) render() string {
	if m.width == 0 || m.height == 0 {
		return ""
	}
	header := m.renderHeader()
	footer := m.renderFooter()
	bodyH := max(3, m.height-2)
	var body string
	if m.showHelp {
		body = m.renderHelp(m.width, bodyH)
	} else if m.showOutbox {
		body = m.renderOutbox(m.width, bodyH)
	} else if m.statusView {
		body = m.renderStatuses(m.width, bodyH)
	} else if m.overviewView {
		body = m.renderOverview(m.width, bodyH)
	} else if m.goals.open {
		body = m.renderGoals(m.width, bodyH)
	} else if m.picker != nil {
		body = m.renderPicker(m.width, bodyH)
	} else if m.leader {
		body = m.renderMenuPane(bodyH)
	} else {
		body = m.renderBody(bodyH)
	}
	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
}

// renderMenuPane draws the open space menu below the task list (alone on a short screen),
// one page of it when it is too high for the screen.
func (m *Model) renderMenuPane(bodyH int) string {
	menu, p, n, alone := m.currentMenuPage()
	title := "メニュー（キーを押す・esc で閉じる）"
	if n > 1 {
		title = fmt.Sprintf("メニュー %d/%d（tab で次のページ・キーを押す・esc で閉じる）", p+1, n)
	}
	if alone {
		return m.pane(title, menu, m.width, bodyH, true)
	}
	menuH := min(len(menu)+3, bodyH-3)
	return lipgloss.JoinVertical(lipgloss.Left, m.renderBody(bodyH-menuH), m.pane(title, menu, m.width, menuH, true))
}

// --- layout ---

// paneWidths returns the outer widths of sidebar, list and detail (0 = hidden).
func (m *Model) paneWidths() (int, int, int) {
	if m.zoom {
		return 0, 0, m.width
	}
	if m.narrow() {
		w := [3]int{}
		w[m.focus] = m.width
		return w[0], w[1], w[2]
	}
	side := sidebarWidth
	if m.hideSidebar {
		side = 0
	}
	rest := m.width - side
	detail := rest * m.detailRatio / 100
	if rest-detail < minListWidth { // keep the task list usable
		detail = max(minListWidth, rest-minListWidth)
	}
	return side, rest - detail, detail
}

func (m *Model) listHeight() int { return max(1, m.height-2-3) } // header, footer, borders, title

func (m *Model) layoutDetail() {
	_, _, dw := m.paneWidths()
	if m.narrow() {
		dw = m.width
	}
	m.detailView.SetWidth(max(1, dw-4))
	m.detailView.SetHeight(m.listHeight())
	m.refreshDetailView()
}

func (m *Model) ensureListVisible() {
	h := m.listHeight()
	if m.listCursor < m.listOffset {
		m.listOffset = m.listCursor
		// show the group header above the first task when scrolling up
		if m.listOffset > 0 && m.rows[m.listOffset-1].isHeader() {
			m.listOffset--
		}
	}
	if m.listCursor >= m.listOffset+h {
		m.listOffset = m.listCursor - h + 1
	}
	m.listOffset = clamp(m.listOffset, 0, max(0, len(m.rows)-h))
}

func (m *Model) renderBody(h int) string {
	sw, lw, dw := m.paneWidths()
	if m.calendarActive() && !m.zoom {
		if m.narrow() {
			if m.focus == paneSidebar {
				return m.pane("プロジェクト", m.sidebarLines(m.width-4, h-3), m.width, h, true)
			}
			return m.renderCalendar(m.width, h)
		}
		if sw == 0 {
			return m.renderCalendar(m.width, h)
		}
		side := m.pane("プロジェクト", m.sidebarLines(sw-4, h-3), sw, h, m.focus == paneSidebar)
		return lipgloss.JoinHorizontal(lipgloss.Top, side, m.renderCalendar(m.width-sw, h))
	}
	if m.boardActive() && !m.zoom {
		// The board takes the list and detail area; enter opens the zoomed detail.
		if m.narrow() {
			if m.focus == paneSidebar {
				return m.pane("プロジェクト", m.sidebarLines(m.width-4, h-3), m.width, h, true)
			}
			return m.renderBoard(m.width, h)
		}
		if sw == 0 {
			return m.renderBoard(m.width, h)
		}
		side := m.pane("プロジェクト", m.sidebarLines(sw-4, h-3), sw, h, m.focus == paneSidebar)
		return lipgloss.JoinHorizontal(lipgloss.Top, side, m.renderBoard(m.width-sw, h))
	}
	var panes []string
	if sw > 0 {
		panes = append(panes, m.pane("プロジェクト", m.sidebarLines(sw-4, h-3), sw, h, m.focus == paneSidebar))
	}
	if lw > 0 {
		panes = append(panes, m.pane(m.listTitle(), m.listLines(lw-4, h-3), lw, h, m.focus == paneList))
	}
	if dw > 0 {
		if m.detailView.Width() != max(1, dw-4) {
			m.detailView.SetWidth(max(1, dw-4))
			m.refreshDetailView() // re-wrap for the new width (e.g. after z)
		}
		m.detailView.SetHeight(h - 3)
		title := "詳細"
		if m.zoom {
			title = "詳細（z / esc で戻る）"
		}
		if m.fetchingFor != "" && m.detail != nil && m.fetchingFor == m.detail.GID {
			title += " ⟳"
		}
		panes = append(panes, m.pane(title, strings.Split(m.detailView.View(), "\n"), dw, h, m.focus == paneDetail))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, panes...)
}

// pane draws a bordered box of exactly w×h with a title line and content lines.
func (m *Model) pane(title string, lines []string, w, h int, focused bool) string {
	inner := max(1, w-4)
	borderColor := m.st.muted
	if focused {
		borderColor = m.st.accent
	}
	content := make([]string, 0, h-2)
	content = append(content, m.st.paneTitle.Render(ansi.Truncate(title, inner, "…")))
	for i := 0; i < h-3; i++ {
		line := ""
		if i < len(lines) {
			line = ansi.Truncate(lines[i], inner, "…")
		}
		content = append(content, line+strings.Repeat(" ", max(0, inner-ansi.StringWidth(line))))
	}
	return lipgloss.NewStyle().
		Border(m.st.border).
		BorderForeground(borderColor).
		Padding(0, 1).
		Width(w).
		Render(strings.Join(content, "\n"))
}

// --- header / footer ---

func (m *Model) renderHeader() string {
	badge := ""
	if sc := m.currentScope(); sc.status != "" {
		badge = " " + m.statusBadge(sc.status) // latest project status
	}
	left := m.st.header.Render(" asagao ") + " " + m.st.headerScope.Render(m.currentScope().name) + badge +
		"  " + m.st.headerFilter.Render("["+m.filter.String()+"]")
	if l := m.savedSearchLabel(); l != "" {
		left += "  " + m.st.headerFilter.Render(l)
	}
	if m.tagFilter != "" {
		left += "  " + m.st.tag.Render("#"+m.tagFilter)
	}
	if m.fieldFilter.field != "" {
		left += "  " + m.st.tag.Render(m.fieldFilter.label())
	}
	if m.boardActive() {
		left += "  " + m.st.headerFilter.Render("[ボード]")
	}
	if m.calendarActive() {
		left += "  " + m.st.headerFilter.Render("[カレンダー]")
	}
	if m.query != "" {
		left += "  " + m.st.headerFilter.Render("検索: "+m.query)
	}
	status, ok := m.syncStatus()
	style := m.st.dim
	if !ok {
		style = m.st.errorText
	}
	right := style.Render(status + " ")
	var counts []string
	if n := m.outbox.pending; n > 0 {
		counts = append(counts, m.st.today.Render(fmt.Sprintf("↑ 未送信 %d", n)))
	}
	if n := m.outbox.conflict; n > 0 {
		counts = append(counts, m.st.errorText.Render(fmt.Sprintf("⚠ 競合 %d", n)))
	}
	if n := m.outbox.failed; n > 0 {
		counts = append(counts, m.st.errorText.Render(fmt.Sprintf("✗ 失敗 %d", n)))
	}
	if len(counts) > 0 {
		right = strings.Join(counts, "  ") + m.st.dim.Render(" (!)") + "  " + right
	}
	gap := max(1, m.width-ansi.StringWidth(left)-ansi.StringWidth(right))
	return ansi.Truncate(left+strings.Repeat(" ", gap)+right, m.width, "")
}

func (m *Model) renderFooter() string {
	if m.promptMode != promptNone {
		m.prompt.SetWidth(max(10, m.width-4))
		return m.prompt.View()
	}
	if m.searching {
		m.search.SetWidth(max(10, m.width-4))
		return m.search.View()
	}
	if q, ok := m.bulkFooter(); ok {
		return ansi.Truncate(" "+m.st.today.Render(q), m.width, "…")
	}
	if m.status != "" {
		return ansi.Truncate(" "+m.st.footer.Render(m.status), m.width, "…")
	}
	if m.leader {
		hint := "メニュー: キーを押してください（esc で閉じる）"
		if _, _, n, _ := m.currentMenuPage(); n > 1 {
			hint = "メニュー: キーを押してください（tab / j・k でページを切り替え・esc で閉じる）"
		}
		return ansi.Truncate(" "+m.st.footer.Render(hint), m.width, "…")
	}
	keys := m.footerHints()
	var parts []string
	for _, k := range keys {
		parts = append(parts, m.st.footerKey.Render(k[0])+" "+m.st.footer.Render(k[1]))
	}
	return ansi.Truncate(" "+strings.Join(parts, "  "), m.width, "…")
}

func (m *Model) renderHelp(w, h int) string {
	// block renders a titled list of (keys, label) rows with the key column aligned.
	block := func(title string, rows [][2]string) []string {
		kw := 0
		for _, r := range rows {
			kw = max(kw, ansi.StringWidth(r[0]))
		}
		out := []string{m.st.paneTitle.Render(title)}
		for _, r := range rows {
			out = append(out, m.st.footerKey.Render(padRight(r[0], kw))+"  "+r[1])
		}
		return out
	}
	nav := block("移動（この画面）", m.navHelp())
	nav = append(nav, "")
	nav = append(nav, block("どこでも", [][2]string{{"space", "メニュー（ここでできる操作）"}, {"/", "検索"},
		{"esc / q", "戻る / 終了"}, {"?", "このヘルプ"}})...)
	groups := map[string][][2]string{}
	for _, a := range m.available() {
		keys := "space " + a.key
		if len(a.shortcut) > 0 {
			keys = a.shortcut[0] + " / " + keys
		}
		groups[a.group] = append(groups[a.group], [2]string{keys, a.label})
	}
	cols := [][]string{nav}
	for _, g := range groupOrder {
		if len(groups[g]) > 0 {
			cols = append(cols, block(g, groups[g]))
		}
	}
	lines := append([]string{m.st.dim.Render("「x / space x」は x を直接押しても、space のあと x を押しても同じです。何かキーを押すと閉じます。"), ""},
		layoutColumns(cols, w-4)...)
	return m.pane("ヘルプ", lines, w, h, true)
}

// --- pane contents ---

func (m *Model) sidebarLines(w, h int) []string {
	lines := make([]string, 0, len(m.scopes))
	cursorLine, group := 0, ""
	for i, s := range m.scopes {
		// Projects are under headings: my favorites, then each team (only when there is
		// more than one heading, so a list of one team's projects stays as it was).
		if s.kind == scopeProject && s.group != group {
			group = s.group
			if m.sidebarGrouped() {
				lines = append(lines, m.st.dim.Render(ansi.Truncate(group, max(1, w), "…")))
			}
		}
		if i == m.scopeCursor {
			cursorLine = len(lines)
		}
		label := s.name
		switch s.kind {
		case scopeMyTasks:
			label = "★ " + label
		case scopeRecent:
			label = "◷ " + label
		case scopeInbox:
			label = "✉ " + label
		default:
			label = "  " + label
		}
		lines = append(lines, m.highlight(label, w, i == m.scopeCursor, m.focus == paneSidebar))
	}
	offset := clamp(cursorLine-h+1, 0, max(0, len(lines)-h))
	return lines[offset:]
}

// sidebarGrouped reports whether the projects are under more than one heading.
func (m *Model) sidebarGrouped() bool {
	first := ""
	for _, s := range m.scopes {
		if s.kind != scopeProject {
			continue
		}
		if first == "" {
			first = s.group
		} else if s.group != first {
			return true
		}
	}
	return false
}

func (m *Model) listTitle() string {
	n := 0
	for _, r := range m.allRows {
		if r.notif == nil && r.task != nil && r.depth == 0 {
			n++
		}
	}
	marked := ""
	if len(m.marked) > 0 {
		marked = fmt.Sprintf("  印 %d 件", len(m.marked))
	}
	if m.query != "" {
		return fmt.Sprintf("検索 (%d)", n) + marked
	}
	if m.currentScope().kind == scopeInbox {
		return fmt.Sprintf("通知 (未読 %d)", m.inboxUnread)
	}
	return fmt.Sprintf("タスク (%d)", n) + marked
}

func (m *Model) listLines(w, h int) []string {
	if len(m.rows) == 0 {
		msg := "該当するタスクはありません"
		if m.syncing && m.lastSync.IsZero() {
			msg = "同期しています…"
		}
		return []string{m.st.dim.Render(msg)}
	}
	today := m.deps.Now().Format(time.DateOnly)
	end := min(len(m.rows), m.listOffset+h)
	lines := make([]string, 0, end-m.listOffset)
	for i := m.listOffset; i < end; i++ {
		r := m.rows[i]
		if r.note {
			lines = append(lines, m.st.dim.Render(ansi.Truncate(r.header, w, "…")))
			continue
		}
		if r.notif != nil {
			lines = append(lines, m.notificationLine(r.notif, w, i == m.listCursor))
			continue
		}
		if r.task == nil {
			header := "▾ " + r.header
			if r.collapsed {
				header = fmt.Sprintf("▸ %s (%d)", r.header, r.count)
			}
			if i == m.listCursor {
				lines = append(lines, m.highlight(header, w, true, m.focus == paneList))
			} else {
				lines = append(lines, m.st.sectionHeader.Render(header))
			}
			continue
		}
		if mark, ok := m.markGutter(r.task); ok {
			lines = append(lines, mark+m.taskLine(r.task, r.depth, w-2, today, i == m.listCursor))
			continue
		}
		lines = append(lines, m.taskLine(r.task, r.depth, w, today, i == m.listCursor))
	}
	return lines
}

func (m *Model) taskLine(t *readmodel.TaskSummary, depth, w int, today string, selected bool) string {
	check := "☐"
	if t.Completed {
		check = "☑"
	}
	if depth > 0 { // expanded subtask: indent under its parent
		check = strings.Repeat("  ", depth-1) + "└ " + check
	}
	due := shortDue(t.DueOn, m.deps.Now())
	extra := fieldBadges(t)
	if badges := readmodel.TaskBadges(*t); badges != "" {
		extra = " " + badges + extra
	}
	if t.Pending {
		extra += " ↑"
	}
	if t.NumSubtasks > 0 {
		if m.expanded[t.GID] {
			extra += fmt.Sprintf(" ▾%d", t.NumSubtasks)
		} else {
			extra += fmt.Sprintf(" ↳%d", t.NumSubtasks)
		}
	}
	if depth == 0 && (m.currentScope().kind == scopeMyTasks || m.query != "") {
		switch {
		case t.ParentName != "":
			extra += "  ⊂ " + t.ParentName // a subtask listed on its own
		case len(t.Memberships) > 0:
			extra += "  " + t.Memberships[0].ProjectName
		}
	}
	dueW := 7
	checkW := ansi.StringWidth(check)
	nameW := max(4, w-checkW-1-dueW-1)
	name := ansi.Truncate(t.Name+extra, nameW, "…")

	if selected {
		plain := check + " " + padRight(name, nameW) + " " + padLeft(due, dueW)
		return m.highlight(plain, w, true, m.focus == paneList)
	}
	nameStyle := lipgloss.NewStyle()
	if t.Completed {
		nameStyle = m.st.done
	}
	dueStyle := m.st.dim
	switch {
	case t.DueOn != "" && t.DueOn < today && !t.Completed:
		dueStyle = m.st.overdue
	case t.DueOn == today:
		dueStyle = m.st.today
	}
	// Style the task name and project separately so the project reads as secondary.
	styledName := nameStyle.Render(ansi.Truncate(t.Name, nameW, "…"))
	if rest := nameW - ansi.StringWidth(t.Name); rest > 0 && extra != "" {
		styledName += m.st.dim.Render(ansi.Truncate(extra, rest, "…"))
	}
	return check + " " + padRight(styledName, nameW) + " " + dueStyle.Render(padLeft(due, dueW))
}

func (m *Model) highlight(s string, w int, selected, focused bool) string {
	if !selected {
		return s
	}
	s = padRight(ansi.Truncate(s, w, "…"), w)
	if focused {
		return m.st.selected.Render(s)
	}
	return m.st.selectedBlur.Render(s)
}

// --- detail ---

func (m *Model) refreshDetailView() {
	m.detailView.SetContent(m.renderDetail(max(10, m.detailView.Width())))
}

func (m *Model) renderDetail(w int) string {
	d := m.detail
	m.detailItems = m.detailItems[:0]
	if d == nil {
		return m.st.dim.Render("タスクを選択してください")
	}
	active := m.inDetail() // items are selectable (and empty fields shown)
	m.renderedActive = active
	var b strings.Builder
	line := func() int { return strings.Count(b.String(), "\n") }
	// item marks the next line as a selectable item and returns whether it is selected.
	item := func(kind itemKind, sub int) bool {
		m.detailItems = append(m.detailItems, detailItem{kind: kind, sub: sub, line: line()})
		return active && len(m.detailItems)-1 == m.detailCursor
	}
	row := func(selected bool, text string) {
		if selected {
			b.WriteString(m.highlight(ansi.Truncate(text, w, "…"), w, true, true) + "\n")
		} else {
			b.WriteString(text + "\n")
		}
	}

	if len(m.detailStack) > 1 {
		b.WriteString(m.st.dim.Render("← backspace で戻る") + "\n")
	}
	title := d.Name
	if d.Completed {
		title = "☑ " + title
	}
	sel := item(itemName, 0)
	if sel {
		row(true, title+m.st.dim.Render("  enter で名前・説明を編集"))
	} else {
		b.WriteString(lipgloss.NewStyle().Width(w).Bold(true).Render(title) + "\n")
	}
	b.WriteString("\n")

	const labelW = 12 // display width; "プロジェクト" is 12 columns
	field := func(kind itemKind, label, value, empty string) {
		if value == "" {
			if !active {
				return
			}
			value = m.st.dim.Render(empty)
		}
		text := m.st.label.Render(padRight(label, labelW)) + " " + value
		row(item(kind, 0), text)
	}
	assignee := ""
	if d.Assignee != nil {
		assignee = d.Assignee.Name
	}
	field(itemAssignee, "担当", assignee, "未割り当て")
	field(itemDue, "期日", longDue(d.DueOn, d.DueAt), "なし")
	if m.startOffered {
		field(itemStart, "開始日", d.StartOn, "なし")
	} else if d.StartOn != "" { // the plan lacks start dates (a recent 402): shown, not offered
		b.WriteString(m.st.label.Render(padRight("開始日", labelW)) + " " + d.StartOn + "\n")
	}
	for i, ms := range d.Memberships {
		v := ms.ProjectName
		if ms.SectionName != "" && ms.SectionName != "(no section)" && ms.SectionName != "Untitled section" {
			v += " / " + ms.SectionName
		}
		label := "プロジェクト"
		if i > 0 {
			label = ""
		}
		row(item(itemSection, i), m.st.label.Render(padRight(label, labelW))+" "+v)
	}
	if active {
		label := ""
		if len(d.Memberships) == 0 {
			label = "プロジェクト"
		}
		row(item(itemProjects, 0), m.st.label.Render(padRight(label, labelW))+" "+m.st.dim.Render("＋ プロジェクトに追加 / 外す"))
	}
	tags := ""
	if len(d.Tags) > 0 {
		tags = m.st.tag.Render("#" + strings.Join(d.Tags, " #"))
	}
	field(itemTags, "タグ", tags, "なし")
	var followers []string
	for _, f := range d.Followers {
		followers = append(followers, f.Name)
	}
	field(itemFollowers, "フォロワー", strings.Join(followers, "、"), "なし")
	likes := ""
	if d.NumLikes > 0 || d.Liked {
		likes = fmt.Sprintf("♥ %d", d.NumLikes)
		if d.Liked {
			likes += "（いいね済み）"
		}
	}
	field(itemLike, "いいね", likes, "♡ なし")
	for i, f := range d.CustomFields {
		value := f.Display
		if value == "" {
			if !active {
				continue
			}
			value = m.st.dim.Render("なし")
		}
		if active && !f.Editable {
			value += m.st.dim.Render("  （変更不可）")
		}
		row(item(itemCustomField, i), m.st.label.Render(padRight(ansi.Truncate(f.Name, labelW, "…"), labelW))+" "+value)
	}
	if active {
		done := "☐ 未完了"
		if d.Completed {
			done = "☑ 完了"
		}
		field(itemCompleted, "状態", done, "")
	}
	if subtype := d.ResourceSubtype; active || (subtype != "" && subtype != "default_task") {
		label := task.SubtypeLabel(subtype)
		if subtype == "milestone" {
			label = "◆ " + label
		}
		field(itemSubtype, "種類", label, "タスク")
	}
	if d.ResourceSubtype == "approval" {
		status := d.ApprovalStatus
		if status == "" {
			status = "pending"
		}
		field(itemApproval, "承認", task.ApprovalLabel(status), "")
	}
	if d.Parent != nil {
		field(itemParent, "親タスク", d.Parent.Name, "")
	}

	imageNo := map[string]int{}
	for i, gid := range taskImages(d) {
		imageNo[gid] = i + 1
	}
	md := textMarkdown(d.HTMLNotes, d.Notes)
	if active {
		b.WriteString("\n")
		hint := "説明"
		if md == "" {
			hint += m.st.dim.Render("  なし（enter で書く）")
		}
		row(item(itemNotes, 0), m.st.sectionHeader.Render(hint))
	}
	if md != "" {
		if !active {
			b.WriteString("\n")
		}
		b.WriteString(m.withImages(m.markdown(md, w), w, imageNo) + "\n")
	}
	if n := len(imageNo); n > 0 {
		b.WriteString(m.st.dim.Render(fmt.Sprintf("画像 %d 件（space i で全画面表示）", n)) + "\n")
	}
	if len(d.Attachments) > 0 || active {
		b.WriteString("\n" + m.st.sectionHeader.Render(fmt.Sprintf("添付ファイル (%d)", len(d.Attachments))) + "\n")
		for i, a := range d.Attachments {
			kind := "[ファイル]"
			if a.IsImage {
				kind = "[画像]"
			}
			text := kind + " " + a.Name
			if sz := formatSize(a.Size); sz != "" {
				text += "  " + m.st.dim.Render(sz)
			}
			if a.LocalPath != "" {
				text += m.st.dim.Render("  ✓保存済み")
			}
			if localgid.IsLocal(a.GID) {
				text += " ↑"
			}
			row(item(itemAttachment, i), ansi.Truncate(text, w, "…"))
		}
		if active {
			row(item(itemAddAttachment, 0), m.st.dim.Render("＋ ファイルを添付"))
		}
	}

	if len(d.Subtasks) > 0 || active {
		b.WriteString("\n" + m.st.sectionHeader.Render(fmt.Sprintf("サブタスク (%d)", len(d.Subtasks))) + "\n")
		for i, s := range d.Subtasks {
			check := "☐"
			if s.Completed {
				check = "☑"
			}
			text := check + " " + s.Name
			if s.NumSubtasks > 0 {
				text += fmt.Sprintf(" ↳%d", s.NumSubtasks)
			}
			if s.Assignee != nil {
				text += "  " + m.st.dim.Render(s.Assignee.Name)
			}
			if s.DueOn != "" {
				text += "  " + m.st.dim.Render(shortDue(s.DueOn, m.deps.Now()))
			}
			if s.Pending {
				text += " ↑"
			}
			row(item(itemSubtask, i), ansi.Truncate(text, w, "…"))
		}
		if active {
			row(item(itemAddSubtask, 0), m.st.dim.Render("＋ サブタスクを追加"))
		}
	}

	// Dependencies: headed lists when there are some; otherwise just the "add" rows.
	if len(d.Dependencies)+len(d.Dependents) > 0 || active {
		b.WriteString("\n")
	}
	for _, sec := range []struct {
		title     string
		deps      []readmodel.DependencyView
		kind, add itemKind
		addLabel  string
	}{
		{"待っているタスク", d.Dependencies, itemDependency, itemAddDependency, "＋ 待つタスクを追加（依存関係）"},
		{"待たれているタスク", d.Dependents, itemDependent, itemAddDependent, "＋ このタスクを待つタスクを追加"},
	} {
		if len(sec.deps) > 0 {
			b.WriteString(m.st.sectionHeader.Render(fmt.Sprintf("%s (%d)", sec.title, len(sec.deps))) + "\n")
		}
		for i, dep := range sec.deps {
			row(item(sec.kind, i), ansi.Truncate(m.dependencyLine(dep), w, "…"))
		}
		if active {
			row(item(sec.add, 0), m.st.dim.Render(sec.addLabel))
		}
	}

	var comments []readmodel.StoryView
	for _, s := range d.Stories {
		if s.Type == "comment" {
			comments = append(comments, s)
		}
	}
	if len(comments) > 0 || active {
		b.WriteString("\n" + m.st.sectionHeader.Render(fmt.Sprintf("コメント (%d)", len(comments))) + "\n")
		if active {
			row(item(itemAddComment, 0), m.st.dim.Render("＋ コメントを書く（エディタ）"))
		}
	}
	switch {
	case len(comments) > 0:
		// In the side pane show only the latest few; the full-screen view shows them all.
		shown := comments
		if !m.zoom && len(comments) > sidePaneComments {
			shown = comments[len(comments)-sidePaneComments:]
			b.WriteString(m.st.dim.Render(fmt.Sprintf("… 古いコメント %d 件（z で全部表示）", len(comments)-len(shown))) + "\n")
		}
		rule := m.st.dim.Render(strings.Repeat("─", max(1, w)))
		for i, c := range shown {
			if i > 0 {
				b.WriteString(rule + "\n")
			}
			who := "?"
			if c.CreatedBy != nil {
				who = c.CreatedBy.Name
			}
			meta := m.st.headerScope.Render(who) + "  " + m.st.dim.Render(commentTime(c.CreatedAt, m.deps.Now()))
			if c.Pending {
				meta += "  " + m.st.today.Render("（未送信）")
			}
			if idx := storyIndex(d.Stories, c.GID); active && idx >= 0 && m.isMyComment(c) {
				// my own comment: enter rewrites or deletes it
				row(item(itemComment, idx), meta+m.st.dim.Render("  （enter で編集・削除）"))
			} else {
				b.WriteString(meta + "\n")
			}
			text := m.withImages(m.markdown(textMarkdown(c.HTMLText, c.Text), max(10, w-2)), max(10, w-2), imageNo)
			b.WriteString(indentLines(text, 2) + "\n")
			if r := readmodel.ReactionsLine(c.Reactions); r != "" {
				b.WriteString("  " + m.st.dim.Render(ansi.Truncate(r, max(1, w-2), "…")) + "\n")
			}
		}
	case d.DetailsFetchedAt == "":
		b.WriteString(m.st.dim.Width(w).Render("コメントは未取得です（オンライン時に自動で取得します）") + "\n")
	}
	return b.String()
}

func indentLines(s string, n int) string {
	pad := strings.Repeat(" ", n)
	return pad + strings.ReplaceAll(s, "\n", "\n"+pad)
}

// sidePaneComments is how many of the latest comments the (unzoomed) detail pane shows.
const sidePaneComments = 3

// commentTime formats a comment timestamp: "今日 14:03", "11/02 09:15" or "2024/03/01 09:15".
func commentTime(s string, now time.Time) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	t = t.Local()
	switch {
	case t.Format(time.DateOnly) == now.Format(time.DateOnly):
		return "今日 " + t.Format("15:04")
	case t.Year() == now.Year():
		return t.Format("01/02 15:04")
	}
	return t.Format("2006/01/02 15:04")
}

// --- formatting ---

var weekdays = [...]string{"日", "月", "火", "水", "木", "金", "土"}

func shortDue(dueOn string, now time.Time) string {
	if dueOn == "" {
		return ""
	}
	d, err := time.Parse(time.DateOnly, dueOn)
	if err != nil {
		return dueOn
	}
	today, _ := time.Parse(time.DateOnly, now.Format(time.DateOnly))
	switch days := int(d.Sub(today).Hours() / 24); {
	case days == 0:
		return "今日"
	case days == 1:
		return "明日"
	case days == -1:
		return "昨日"
	case days > 1 && days < 7:
		return weekdays[d.Weekday()] + "曜"
	}
	if d.Year() != now.Year() {
		return d.Format("06/1/2")
	}
	return d.Format("1/2")
}

func longDue(dueOn, dueAt string) string {
	if dueAt != "" {
		if t, err := time.Parse(time.RFC3339, dueAt); err == nil {
			t = t.Local()
			return t.Format("2006/01/02") + "(" + weekdays[t.Weekday()] + ") " + t.Format("15:04")
		}
	}
	if d, err := time.Parse(time.DateOnly, dueOn); err == nil {
		return d.Format("2006/01/02") + "(" + weekdays[d.Weekday()] + ")"
	}
	return dueOn
}

func padRight(s string, w int) string {
	return s + strings.Repeat(" ", max(0, w-ansi.StringWidth(s)))
}

func padLeft(s string, w int) string {
	return strings.Repeat(" ", max(0, w-ansi.StringWidth(s))) + s
}
