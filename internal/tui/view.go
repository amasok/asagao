package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/amasok/asana-tui/internal/store"
)

const sidebarWidth = 28

func (m *Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.WindowTitle = "asana-tui"
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
	} else {
		body = m.renderBody(bodyH)
	}
	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
}

// --- layout ---

// paneWidths returns the outer widths of sidebar, list and detail (0 = hidden).
func (m *Model) paneWidths() (int, int, int) {
	if m.narrow() {
		w := [3]int{}
		w[m.focus] = m.width
		return w[0], w[1], w[2]
	}
	side := sidebarWidth
	rest := m.width - side
	detail := rest * 2 / 5
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
		if m.listOffset > 0 && m.rows[m.listOffset-1].task == nil {
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
	var panes []string
	if sw > 0 {
		panes = append(panes, m.pane("プロジェクト", m.sidebarLines(sw-4, h-3), sw, h, m.focus == paneSidebar))
	}
	if lw > 0 {
		panes = append(panes, m.pane(m.listTitle(), m.listLines(lw-4, h-3), lw, h, m.focus == paneList))
	}
	if dw > 0 {
		m.detailView.SetWidth(max(1, dw-4))
		m.detailView.SetHeight(h - 3)
		title := "詳細"
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
	left := m.st.header.Render(" asana-tui ") + " " + m.st.headerScope.Render(m.currentScope().name) +
		"  " + m.st.headerFilter.Render("["+m.filter.String()+"]")
	if m.query != "" {
		left += "  " + m.st.headerFilter.Render("検索: "+m.query)
	}
	status, ok := m.syncStatus()
	style := m.st.dim
	if !ok {
		style = m.st.errorText
	}
	right := style.Render(status + " ")
	gap := max(1, m.width-ansi.StringWidth(left)-ansi.StringWidth(right))
	return ansi.Truncate(left+strings.Repeat(" ", gap)+right, m.width, "")
}

func (m *Model) renderFooter() string {
	if m.searching {
		m.search.SetWidth(max(10, m.width-4))
		return m.search.View()
	}
	if m.status != "" {
		return ansi.Truncate(" "+m.st.footer.Render(m.status), m.width, "…")
	}
	keys := [][2]string{{"j/k", "移動"}, {"tab", "ペイン"}, {"enter", "開く"}, {"/", "検索"}, {"f", "絞り込み"}, {"o", "ブラウザ"}, {"r", "同期"}, {"?", "ヘルプ"}, {"q", "終了"}}
	var parts []string
	for _, k := range keys {
		parts = append(parts, m.st.footerKey.Render(k[0])+" "+m.st.footer.Render(k[1]))
	}
	return ansi.Truncate(" "+strings.Join(parts, "  "), m.width, "…")
}

func (m *Model) renderHelp(w, h int) string {
	lines := []string{
		"移動",
		"  j / k, ↓ / ↑      上下に移動",
		"  g / G             先頭 / 末尾",
		"  ctrl+d / ctrl+u   半ページ移動",
		"  tab / shift+tab   ペインを切り替え",
		"  h / l, ← / →      左右のペインへ",
		"  enter             選択 / 詳細を開く",
		"",
		"表示",
		"  /                 検索（タスク名・説明・コメント。全プロジェクトが対象）",
		"  esc               検索を解除 / 戻る",
		"  f / F             絞り込みを切り替え（未完了 → 7日以内 → 期限切れ → 完了済み → すべて）",
		"  o                 選択中のタスクをブラウザで開く",
		"  r                 今すぐ同期",
		"",
		"  q                 終了（狭い画面では 1 つ前のペインへ）",
		"  ?                 このヘルプ（何かキーを押すと閉じる）",
		"",
		"タスクの完了・作成・コメントは次のバージョン（M4）で対応します。",
	}
	return m.pane("ヘルプ", lines, w, h, true)
}

// --- pane contents ---

func (m *Model) sidebarLines(w, h int) []string {
	lines := make([]string, 0, len(m.scopes))
	for i, s := range m.scopes {
		label := s.name
		if s.kind == scopeMyTasks {
			label = "★ " + label
		} else {
			label = "  " + label
		}
		lines = append(lines, m.highlight(label, w, i == m.scopeCursor, m.focus == paneSidebar))
	}
	offset := clamp(m.scopeCursor-h+1, 0, max(0, len(lines)-h))
	return lines[offset:]
}

func (m *Model) listTitle() string {
	n := 0
	for _, r := range m.rows {
		if r.task != nil {
			n++
		}
	}
	if m.query != "" {
		return fmt.Sprintf("検索 (%d)", n)
	}
	return fmt.Sprintf("タスク (%d)", n)
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
		if r.task == nil {
			lines = append(lines, m.st.sectionHeader.Render("▾ "+r.header))
			continue
		}
		lines = append(lines, m.taskLine(r.task, w, today, i == m.listCursor))
	}
	return lines
}

func (m *Model) taskLine(t *store.TaskSummary, w int, today string, selected bool) string {
	check := "☐"
	if t.Completed {
		check = "☑"
	}
	due := shortDue(t.DueOn, m.deps.Now())
	extra := ""
	if t.NumSubtasks > 0 {
		extra = fmt.Sprintf(" ↳%d", t.NumSubtasks)
	}
	if m.currentScope().kind == scopeMyTasks || m.query != "" {
		if len(t.Memberships) > 0 {
			extra += "  " + t.Memberships[0].ProjectName
		}
	}
	dueW := 7
	nameW := max(4, w-2-dueW-1)
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
	if d == nil {
		return m.st.dim.Render("タスクを選択してください")
	}
	var b strings.Builder
	wrap := lipgloss.NewStyle().Width(w)
	title := d.Name
	if d.Completed {
		title = "☑ " + title
	}
	b.WriteString(wrap.Bold(true).Render(title) + "\n\n")

	field := func(label, value string) {
		if value == "" {
			return
		}
		const labelW = 12 // display width; "プロジェクト" is 12 columns
		value = lipgloss.NewStyle().Width(max(1, w-labelW-1)).Render(value)
		value = strings.ReplaceAll(value, "\n", "\n"+strings.Repeat(" ", labelW+1))
		b.WriteString(m.st.label.Render(padRight(label, labelW)) + " " + value + "\n")
	}
	if d.Assignee != nil {
		field("担当", d.Assignee.Name)
	} else {
		field("担当", m.st.dim.Render("未割り当て"))
	}
	field("期日", longDue(d.DueOn, d.DueAt))
	field("開始日", d.StartOn)
	for _, ms := range d.Memberships {
		v := ms.ProjectName
		if ms.SectionName != "" && ms.SectionName != "(no section)" && ms.SectionName != "Untitled section" {
			v += " / " + ms.SectionName
		}
		field("プロジェクト", v)
	}
	if len(d.Tags) > 0 {
		field("タグ", m.st.tag.Render("#"+strings.Join(d.Tags, " #")))
	}
	if d.Parent != nil {
		field("親タスク", d.Parent.Name)
	}
	if d.Notes != "" {
		b.WriteString("\n" + wrap.Render(strings.TrimRight(d.Notes, "\n")) + "\n")
	}
	if len(d.Subtasks) > 0 {
		b.WriteString("\n" + m.st.sectionHeader.Render(fmt.Sprintf("サブタスク (%d)", len(d.Subtasks))) + "\n")
		for _, s := range d.Subtasks {
			check := "☐"
			if s.Completed {
				check = "☑"
			}
			line := check + " " + s.Name
			if s.DueOn != "" {
				line += "  " + m.st.dim.Render(shortDue(s.DueOn, m.deps.Now()))
			}
			b.WriteString(wrap.Render(line) + "\n")
		}
	}
	var comments []store.StoryView
	for _, s := range d.Stories {
		if s.Type == "comment" {
			comments = append(comments, s)
		}
	}
	switch {
	case len(comments) > 0:
		b.WriteString("\n" + m.st.sectionHeader.Render(fmt.Sprintf("コメント (%d)", len(comments))) + "\n")
		for _, c := range comments {
			who := "?"
			if c.CreatedBy != nil {
				who = c.CreatedBy.Name
			}
			when := ""
			if t, err := time.Parse(time.RFC3339, c.CreatedAt); err == nil {
				when = t.Local().Format("01/02 15:04")
			}
			b.WriteString(m.st.label.Render(who+"  "+when) + "\n")
			b.WriteString(wrap.Render(strings.TrimRight(c.Text, "\n")) + "\n\n")
		}
	case d.DetailsFetchedAt == "":
		b.WriteString("\n" + m.st.dim.Width(w).Render("コメントは未取得です（オンライン時に自動で取得します）") + "\n")
	}
	return b.String()
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
