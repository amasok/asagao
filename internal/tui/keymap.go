package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	localgid "github.com/amasok/asagao/internal/domain/gid"
	"github.com/amasok/asagao/internal/domain/plan"
)

// Keys are organised in three layers:
//
//  1. Movement, the same everywhere: j/k ↑↓, h/l ←→, g/G, ctrl+d/u, enter, esc/backspace, tab.
//  2. A few frequent actions, also the same everywhere: x 完了, a 追加, e 編集, c コメント, / 検索.
//  3. Everything else lives in the space menu (which-key style): press space, see what can
//     be done here, press the key. Most menu items also work directly (their shortcut).
//
// The registry below drives the menu, the help screen (?) and the footer, so what is shown
// is always what works.

type keyCtx int

const (
	ctxSidebar keyCtx = 1 << iota
	ctxList
	ctxBoard
	ctxDetail
	ctxCalendar

	ctxTask = ctxList | ctxBoard | ctxDetail // a task is selected / shown
	ctxAll  = ctxSidebar | ctxTask | ctxCalendar
)

func (m *Model) keyContext() keyCtx {
	switch {
	case m.inDetail():
		return ctxDetail
	case m.focus == paneSidebar:
		return ctxSidebar
	case m.boardActive():
		return ctxBoard
	case m.calendarActive():
		return ctxCalendar
	}
	return ctxList
}

type action struct {
	id          string   // name used in config.toml [keys] to rebind the shortcut
	key         string   // key in the space menu
	shortcut    []string // keys that work directly
	group       string
	label       string
	ctx         keyCtx
	needTask    bool                // needs a selected (or shown) task
	needProject bool                // needs a project to be open
	needInbox   bool                // only in the inbox (notifications)
	plan        string              // a feature the workspace's plan may lack (plan.*): hidden without it
	bulk        bool                // also acts on the marked tasks (bulk.go), so usable with them and no selection
	ready       func(m *Model) bool // offered only when it returns true (nil: always)
	run         func(m *Model) tea.Cmd
}

const (
	groupTask    = "タスク"
	groupView    = "表示"
	groupProject = "プロジェクト"
	groupOther   = "その他"
)

var groupOrder = []string{groupTask, groupView, groupProject, groupOther}

func (m *Model) actions() []action {
	return []action{
		// task
		{id: "add_task", key: "a", shortcut: []string{"a"}, group: groupTask, label: "タスクを追加", ctx: ctxAll, run: func(m *Model) tea.Cmd { return m.writeKey("a") }},
		{id: "add_subtask", key: "s", shortcut: []string{"S"}, group: groupTask, label: "サブタスクを追加", ctx: ctxTask, needTask: true, run: (*Model).startSubtask},
		{id: "complete", key: "x", shortcut: []string{"x"}, group: groupTask, label: "完了 / 未完了", ctx: ctxTask, needTask: true, bulk: true, run: orMarked((*Model).bulkComplete, func(m *Model) tea.Cmd { return m.writeKey("x") })},
		{id: "edit", key: "e", shortcut: []string{"e"}, group: groupTask, label: "名前・説明をエディタで編集", ctx: ctxTask, needTask: true, run: func(m *Model) tea.Cmd { return m.writeKey("e") }},
		{id: "due", key: "d", group: groupTask, label: "期日を変更", ctx: ctxTask, needTask: true, bulk: true, run: orMarked((*Model).bulkDue, (*Model).openDuePicker)},
		{id: "assignee", key: "u", shortcut: []string{"@"}, group: groupTask, label: "担当者を変更", ctx: ctxTask, needTask: true, bulk: true, run: orMarked((*Model).bulkAssignee, (*Model).openAssigneePicker)},
		{id: "tags", key: "t", group: groupTask, label: "タグを付ける / 外す", ctx: ctxTask, needTask: true, run: (*Model).openTagEditor},
		{id: "manage_tags", key: "y", group: groupTask, label: "タグの名前・色を変える / 削除", ctx: ctxAll, plan: plan.Tags, run: (*Model).openTagManager},
		{id: "move", key: "m", shortcut: []string{"m"}, group: groupTask, label: "セクションを移動", ctx: ctxTask, needTask: true, bulk: true, run: orMarked((*Model).bulkMove, func(m *Model) tea.Cmd { m.openSectionPicker(); return nil })},
		{id: "comment", key: "c", shortcut: []string{"c"}, group: groupTask, label: "コメント（1 行）", ctx: ctxTask, needTask: true, run: func(m *Model) tea.Cmd { return m.writeKey("c") }},
		{id: "comment_editor", key: "C", shortcut: []string{"C"}, group: groupTask, label: "コメントをエディタで書く", ctx: ctxTask, needTask: true, run: func(m *Model) tea.Cmd { return m.writeKey("C") }},
		{id: "like", key: "L", group: groupTask, label: "いいね / 取り消し", ctx: ctxTask, needTask: true, run: (*Model).toggleLike},
		{id: "followers", key: "w", group: groupTask, label: "フォロワーを追加 / 外す", ctx: ctxTask, needTask: true, run: (*Model).openFollowers},
		{id: "projects", key: "P", group: groupTask, label: "ほかのプロジェクトに追加 / 外す", ctx: ctxTask, needTask: true, run: (*Model).openProjects},
		{id: "duplicate", key: "D", group: groupTask, label: "複製", ctx: ctxTask, needTask: true, run: (*Model).startDuplicate},
		{id: "set_parent", key: "p", group: groupTask, label: "親タスクを付け替える", ctx: ctxTask, needTask: true, run: (*Model).openParentPicker},
		{id: "delete_task", key: "Z", group: groupTask, label: "タスクを削除（確認あり）", ctx: ctxTask, needTask: true, run: (*Model).confirmDeleteTask},
		{id: "time", key: "h", group: groupTask, label: "作業時間を見る / 記録する", ctx: ctxTask, needTask: true, plan: plan.TimeTracking, run: (*Model).openTimeEntries},
		{id: "attach", key: "A", group: groupTask, label: "ファイルを添付", ctx: ctxTask, needTask: true, run: (*Model).startAttach},
		{id: "images", key: "i", shortcut: []string{"i"}, group: groupTask, label: "画像を全画面で見る", ctx: ctxTask, needTask: true, run: (*Model).viewImages},
		{id: "browser", key: "o", shortcut: []string{"o"}, group: groupTask, label: "ブラウザで開く", ctx: ctxTask, needTask: true, run: (*Model).openInBrowser},
		// view
		{id: "filter", key: "f", shortcut: []string{"f"}, group: groupView, label: "絞り込み（未完了・期限切れ…）", ctx: ctxAll, run: (*Model).openFilterPicker},
		{id: "tag_filter", key: "T", shortcut: []string{"T"}, group: groupView, label: "タグで絞り込む", ctx: ctxAll, run: (*Model).openTagPicker},
		{id: "search_asana", key: "g", group: groupView, label: "Asana でも検索（キャッシュにないタスク。/ で語を入れてから）", ctx: ctxAll, run: (*Model).searchAsana},
		{id: "saved_search", key: "/", group: groupView, label: "保存した検索を呼び出す", ctx: ctxAll, run: (*Model).openSavedSearchPicker},
		{id: "field_filter", key: "F", group: groupView, label: "カスタムフィールドで絞り込む", ctx: ctxAll, run: (*Model).openFieldFilterPicker},
		{id: "view", key: "v", shortcut: []string{"v"}, group: groupView, label: "表示を切り替え（リスト→ボード→タイムライン→カレンダー）", ctx: ctxList | ctxBoard | ctxCalendar, run: func(m *Model) tea.Cmd { return m.viewKey("v") }},
		{id: "zoom", key: "z", shortcut: []string{"z"}, group: groupView, label: "詳細を全画面 / 戻す", ctx: ctxTask, needTask: true, run: func(m *Model) tea.Cmd { return m.viewKey("z") }},
		{id: "collapse", key: "-", shortcut: []string{"-"}, group: groupView, label: "このセクションを折りたたむ / 開く", ctx: ctxList, run: (*Model).toggleGroup},
		{id: "collapse_all", key: "_", shortcut: []string{"_"}, group: groupView, label: "すべて折りたたむ", ctx: ctxList, run: func(m *Model) tea.Cmd { return m.setAllGroups(true) }},
		{id: "expand_all", key: "+", shortcut: []string{"+", "="}, group: groupView, label: "すべて開く", ctx: ctxList, run: func(m *Model) tea.Cmd { return m.setAllGroups(false) }},
		{id: "sidebar", key: "b", shortcut: []string{"b"}, group: groupView, label: "プロジェクト一覧を隠す / 出す", ctx: ctxAll, run: func(m *Model) tea.Cmd { return m.layoutKey("b") }},
		{id: "wider", key: ">", shortcut: []string{">"}, group: groupView, label: "詳細を広く", ctx: ctxAll, run: func(m *Model) tea.Cmd { return m.layoutKey(">") }},
		{id: "narrower", key: "<", shortcut: []string{"<"}, group: groupView, label: "詳細を狭く", ctx: ctxAll, run: func(m *Model) tea.Cmd { return m.layoutKey("<") }},
		{id: "mark", key: "V", shortcut: []string{"V"}, group: groupView, label: "印を付ける / 外す（まとめて操作）", ctx: ctxList, run: (*Model).toggleMark},
		{id: "mark_all", key: "*", shortcut: []string{"*"}, group: groupView, label: "表示中のタスクすべてに印 / 外す", ctx: ctxList, run: (*Model).toggleMarkAll},
		{id: "inbox_read", key: "R", shortcut: []string{"R"}, group: groupView, label: "通知を既読 / 未読にする", ctx: ctxList, needInbox: true, run: (*Model).toggleNotificationRead},
		{id: "inbox_read_all", key: "Y", group: groupView, label: "通知をすべて既読にする", ctx: ctxList, needInbox: true, run: (*Model).markAllNotificationsRead},
		// project
		{id: "rename_section", key: "E", shortcut: []string{"E"}, group: groupProject, label: "セクション名を変更", ctx: ctxList | ctxBoard | ctxDetail, run: (*Model).startRenameSection},
		{id: "new_section", key: "n", group: groupProject, label: "セクションを追加（このセクションの後）", ctx: ctxList | ctxBoard, needProject: true, plan: plan.Sections, run: (*Model).startNewSection},
		{id: "section_up", key: "K", group: groupProject, label: "セクションを上（左）へ", ctx: ctxList | ctxBoard, needProject: true, plan: plan.Sections, run: func(m *Model) tea.Cmd { return m.moveSection(-1) }},
		{id: "section_down", key: "J", group: groupProject, label: "セクションを下（右）へ", ctx: ctxList | ctxBoard, needProject: true, plan: plan.Sections, run: func(m *Model) tea.Cmd { return m.moveSection(+1) }},
		{id: "delete_section", key: "H", group: groupProject, label: "空のセクションを削除", ctx: ctxList | ctxBoard, needProject: true, plan: plan.Sections, run: (*Model).confirmDeleteSection},
		{id: "new_project", key: "N", group: groupProject, label: "新しいプロジェクト（オンライン）", ctx: ctxAll, run: (*Model).startNewProject},
		{id: "task_from_template", key: "G", group: groupProject, label: "テンプレートからタスクを作る（オンライン）", ctx: ctxList | ctxBoard, needProject: true, ready: (*Model).hasTaskTemplates, run: (*Model).startTaskFromTemplate},
		{id: "project_from_template", key: "B", group: groupProject, label: "テンプレートからプロジェクトを作る（オンライン）", ctx: ctxAll, plan: plan.ProjectTemplates, ready: (*Model).hasProjectTemplates, run: (*Model).startProjectFromTemplate},
		{id: "duplicate_project", key: "S", group: groupProject, label: "このプロジェクトを複製（オンライン）", ctx: ctxAll, needProject: true, run: (*Model).startDuplicateProject},
		{id: "overview", key: "O", group: groupProject, label: "プロジェクトの概要（説明・期日・進み具合）/ 編集", ctx: ctxAll, needProject: true, run: (*Model).openOverview},
		{id: "statuses", key: "U", group: groupProject, label: "ステータス更新を見る", ctx: ctxAll, needProject: true, run: (*Model).openStatuses},
		{id: "write_status", key: "W", group: groupProject, label: "ステータスを書く（エディタ）", ctx: ctxAll, needProject: true, run: (*Model).writeStatus},
		{id: "project_fields", key: "#", group: groupProject, label: "カスタムフィールドを追加 / 外す", ctx: ctxAll, needProject: true, plan: plan.CustomFields, run: (*Model).openProjectFields},
		{id: "members", key: "M", group: groupProject, label: "メンバーを追加 / 外す（オンライン）", ctx: ctxAll, needProject: true, run: (*Model).openMembers},
		{id: "goals", key: "Q", group: groupProject, label: "ゴール（期間・進み具合）", ctx: ctxAll, plan: plan.Goals, run: (*Model).openGoals},
		{id: "portfolios", key: "l", group: groupProject, label: "ポートフォリオ（自分がオーナーのもの）", ctx: ctxAll, plan: plan.Portfolios, run: (*Model).openPortfolios},
		{id: "archive", key: "X", group: groupProject, label: "プロジェクトをアーカイブ（オンライン）", ctx: ctxAll, needProject: true, run: (*Model).confirmArchive},
		// other
		{id: "sync", key: "r", shortcut: []string{"r"}, group: groupOther, label: "今すぐ同期", ctx: ctxAll, run: (*Model).syncNow},
		{id: "reset", key: "I", group: groupOther, label: "ローカルデータを初期化（別のアカウントで使う）", ctx: ctxAll, run: (*Model).startReset},
		{id: "outbox", key: "!", shortcut: []string{"!"}, group: groupOther, label: "未送信の変更・競合", ctx: ctxAll, run: func(m *Model) tea.Cmd { return m.writeKey("!") }},
		{id: "help", key: "?", group: groupOther, label: "ヘルプ（すべてのキー）", ctx: ctxAll, run: func(m *Model) tea.Cmd { m.openOverlay(overlayHelp); return nil }},
		{id: "quit", key: "q", group: groupOther, label: "終了", ctx: ctxAll, run: func(*Model) tea.Cmd { return tea.Quit }},
	}
}

// withBindings applies the user's [keys] overrides to the shortcuts.
func (m *Model) withBindings(actions []action) []action {
	for i, a := range actions {
		if keys, ok := m.deps.KeyBindings[a.id]; ok {
			actions[i].shortcut = keys
		}
	}
	return actions
}

// ActionIDs lists the names that can be used in config.toml [keys].
func ActionIDs() []string {
	var ids []string
	for _, a := range (&Model{}).actions() {
		ids = append(ids, a.id)
	}
	return ids
}

// available returns the actions usable right now, in menu order.
func (m *Model) available() []action {
	ctx := m.keyContext()
	hasTask := m.actionTask() != nil
	hasMarked := len(m.markedTasks()) > 0
	var out []action
	for _, a := range m.withBindings(m.actions()) {
		if a.ctx&ctx == 0 || (a.needTask && !hasTask && !(a.bulk && hasMarked)) || (a.needProject && m.currentProject() == "") ||
			(a.needInbox && m.currentScope().kind != scopeInbox) || (a.plan != "" && !m.planOK(a.plan)) ||
			(a.ready != nil && !a.ready(m)) {
			continue
		}
		out = append(out, a)
	}
	return out
}

// runShortcut runs the action bound directly to key in this context, if any.
func (m *Model) runShortcut(key string) (tea.Cmd, bool) {
	for _, a := range m.available() {
		for _, s := range a.shortcut {
			if s == key {
				return a.run(m), true
			}
		}
	}
	return nil, false
}

// runMenuKey runs the menu entry for key (after space).
func (m *Model) runMenuKey(key string) tea.Cmd {
	if key == "esc" || key == "space" {
		return nil
	}
	for _, a := range m.available() {
		if a.key == key {
			return a.run(m)
		}
	}
	m.status = fmt.Sprintf("「%s」はここでは使えません（space でメニュー）", key)
	return nil
}

func (m *Model) openInBrowser() tea.Cmd {
	t := m.actionTask()
	if t == nil {
		return nil
	}
	if localgid.IsLocal(t.GID) {
		m.status = "まだ Asana に送信されていないタスクです"
		return nil
	}
	return m.openURL(t.PermalinkURL)
}

func (m *Model) syncNow() tea.Cmd {
	if m.syncing {
		m.status = "同期中です"
		return nil
	}
	m.status = "同期しています…"
	return m.startSync()
}

// --- rendering ---

// menuLine is one line of the menu: a group title, an entry, or the blank between groups.
type menuLine struct {
	text  string
	title bool
}

// menuPages lays out the which-key style menu: the actions available here, grouped, in
// as many columns as it takes to be at most maxH lines high (labels are cut to fit the
// columns when it has to be narrow). When even the narrowest columns are too high, the
// menu is split into pages of that many columns, turned with the menuNext/menuPrev keys,
// so every entry can be seen on a short screen too.
func (m *Model) menuPages(w, maxH int) [][]string {
	maxH = max(1, maxH)
	groups := map[string][]action{}
	for _, a := range m.available() {
		groups[a.group] = append(groups[a.group], a)
	}
	var lines []menuLine
	for _, g := range groupOrder {
		if len(groups[g]) == 0 {
			continue
		}
		if len(lines) > 0 {
			lines = append(lines, menuLine{})
		}
		lines = append(lines, menuLine{text: m.st.paneTitle.Render(g), title: true})
		for _, a := range groups[g] {
			lines = append(lines, menuLine{text: m.st.footerKey.Render(padRight(a.key, 2)) + " " + a.label})
		}
	}
	if len(lines) == 0 {
		return [][]string{nil}
	}
	inner := w - 4
	widest := 0
	for _, l := range lines {
		widest = max(widest, ansi.StringWidth(l.text))
	}
	// as many columns as fit at full width, more (narrower) when still too high
	n := max(1, min(len(lines), (inner+menuGap)/(widest+menuGap)))
	for (len(lines)+n-1)/n > maxH && (inner-menuGap*n)/(n+1) >= menuMinWidth {
		n++
	}
	cw := max(1, min(widest, (inner-menuGap*(n-1))/n))
	// the lowest columns that hold everything in n of them; maxH (and pages) when none do
	h := min(maxH, (len(lines)+n-1)/n)
	cols := flowMenu(lines, h)
	for len(cols) > n && h < maxH {
		h++
		cols = flowMenu(lines, h)
	}
	var pages [][]string
	for len(cols) > 0 {
		k := min(n, len(cols))
		pages = append(pages, joinMenuColumns(cols[:k], h, cw))
		cols = cols[k:]
	}
	return pages
}

// flowMenu pours the menu lines into columns at most h high. A column does not start
// with the blank between groups nor end with a group's title.
func flowMenu(lines []menuLine, h int) [][]menuLine {
	var cols [][]menuLine
	for i := 0; i < len(lines); {
		if lines[i].text == "" {
			i++
			continue
		}
		end := min(len(lines), i+h)
		if end < len(lines) && end-1 > i && lines[end-1].title {
			end--
		}
		cols = append(cols, lines[i:end])
		i = end
	}
	return cols
}

// joinMenuColumns draws columns of width cw side by side, h lines high.
func joinMenuColumns(cols [][]menuLine, h, cw int) []string {
	out := make([]string, h)
	for c, col := range cols {
		for r := range h {
			cell := ""
			if r < len(col) {
				cell = ansi.Truncate(col[r].text, cw, "…")
			}
			if c < len(cols)-1 {
				cell = padRight(cell, cw) + strings.Repeat(" ", menuGap)
			}
			out[r] += cell
		}
	}
	return out
}

// menuLayout lays the menu out for the screen: below the task list when it fits there on
// one page, otherwise alone in the body (the list would show only its title anyway), in
// pages when it needs them.
func (m *Model) menuLayout() (pages [][]string, alone bool) {
	bodyH := max(3, m.height-2)
	if bodyH-6 >= menuMinLines { // the pane's border and title, and some of the body
		if pages := m.menuPages(m.width, bodyH-6); len(pages) == 1 {
			return pages, false
		}
	}
	return m.menuPages(m.width, bodyH-3), true
}

// currentMenuPage returns the lines of the menu page shown, its number, the count of
// pages and whether the menu takes the whole body (menuLayout).
func (m *Model) currentMenuPage() (lines []string, page, count int, alone bool) {
	pages, alone := m.menuLayout()
	p := min(max(0, m.menuPage), len(pages)-1)
	return pages[p], p, len(pages), alone
}

// turnMenuPage moves to the next (+1) or previous (-1) page of the menu, round.
func (m *Model) turnMenuPage(d int) {
	_, p, n, _ := m.currentMenuPage()
	m.menuPage = ((p+d)%n + n) % n
}

// menuNext and menuPrev turn the menu's pages; none of them is a menu key.
var (
	menuNext = []string{"tab", "j", "down", "pgdown", "ctrl+d"}
	menuPrev = []string{"shift+tab", "k", "up", "pgup", "ctrl+u"}
)

// menuGap is the space between the menu's columns; menuMinWidth the narrowest column;
// menuMinLines the fewest lines the menu gets beside the task list.
const (
	menuGap      = 3
	menuMinWidth = 18
	menuMinLines = 4
)

// layoutColumns places columns side by side, wrapping to more rows of columns when narrow.
func layoutColumns(cols [][]string, w int) []string {
	var out []string
	for len(cols) > 0 {
		used, n := 0, 0
		var widths []int
		for n < len(cols) {
			cw := 0
			for _, l := range cols[n] {
				cw = max(cw, ansi.StringWidth(l))
			}
			if n > 0 && used+cw+3 > w {
				break
			}
			widths = append(widths, cw)
			used += cw + 3
			n++
		}
		height := 0
		for _, c := range cols[:n] {
			height = max(height, len(c))
		}
		for r := range height {
			var b strings.Builder
			for i, c := range cols[:n] {
				cell := ""
				if r < len(c) {
					cell = c[r]
				}
				if i < n-1 {
					cell = padRight(cell, widths[i]) + "   "
				}
				b.WriteString(cell)
			}
			out = append(out, b.String())
		}
		cols = cols[n:]
		if len(cols) > 0 {
			out = append(out, "")
		}
	}
	return out
}

// navHelp describes movement keys for the current context.
func (m *Model) navHelp() [][2]string {
	switch m.keyContext() {
	case ctxDetail:
		return [][2]string{{"j / k", "項目を移動（担当・期日・タグ・サブタスク…）"}, {"enter", "選んだ項目を変更 / サブタスクを開く"},
			{"ctrl+d / u", "スクロール"}, {"backspace", "親タスクへ戻る"}, {"esc / q", "詳細を閉じる"}}
	case ctxBoard:
		return [][2]string{{"h / l", "列を移動"}, {"j / k", "カードを移動"}, {"H / L", "カードを左右の列へ移す"}, {"enter", "詳細を開く"}}
	case ctxSidebar:
		return [][2]string{{"j / k", "プロジェクトを選ぶ"}, {"enter / l", "タスク一覧へ"}}
	case ctxCalendar:
		return [][2]string{{"h / l", "前日 / 翌日"}, {"j / k", "1 週間後 / 前"}, {"n / p", "翌月 / 前月"}, {".", "今日"},
			{"enter", "その日のタスク（選ぶと詳細）"}, {"a", "その日が期日のタスクを追加"}}
	}
	if m.timelineActive() {
		return [][2]string{{"j / k", "タスクを移動"}, {"l / →", "サブタスクを開く（なければ詳細）"}, {"h / ←", "閉じる / 親へ / プロジェクト一覧へ"},
			{"[ / ]", "1 週前 / 後へ"}, {"{ / }", "1 日の幅を狭く / 広く"}, {".", "今日へ戻る"}, {"enter", "詳細を開く"},
			{"前 / 後", "選んだタスクが待つタスク / 選んだタスクを待つタスク"}, {"待", "ほかのタスクを待っているタスク"}}
	}
	return [][2]string{{"j / k", "タスクを移動"}, {"l / →", "サブタスクを開く"}, {"h / ←", "閉じる / 親へ / プロジェクト一覧へ"},
		{"enter", "詳細を開く"}, {"g / G", "先頭 / 末尾"}, {"tab", "ペインを切り替え"}}
}

// footerHints are the few keys shown at the bottom for the current context.
func (m *Model) footerHints() [][2]string {
	common := [][2]string{{"space", "メニュー"}, {"?", "ヘルプ"}}
	switch m.keyContext() {
	case ctxDetail:
		return append([][2]string{{"j/k", "項目"}, {"enter", "変更"}, {"x", "完了"}, {"e", "編集"}, {"c", "コメント"}, {"backspace", "戻る"}, {"esc", "閉じる"}}, common...)
	case ctxBoard:
		return append([][2]string{{"h/l", "列"}, {"j/k", "カード"}, {"H/L", "移す"}, {"enter", "詳細"}, {"x", "完了"}, {"a", "追加"}}, common...)
	case ctxSidebar:
		return append([][2]string{{"j/k", "選ぶ"}, {"enter", "開く"}, {"/", "検索"}}, common...)
	case ctxCalendar:
		return append([][2]string{{"h/l", "日"}, {"j/k", "週"}, {"n/p", "月"}, {".", "今日"}, {"enter", "その日のタスク"}, {"a", "追加"}, {"v", "表示"}}, common...)
	}
	if n := len(m.markedTasks()); n > 0 {
		return append([][2]string{{fmt.Sprintf("印 %d 件:", n), ""}, {m.actionKey("complete"), "完了"}, {m.actionKey("move"), "移動"},
			{m.actionKey("assignee"), "担当"}, {m.actionKey("due"), "期日"}, {m.actionKey("mark"), "印"}, {"esc", "印を外す"}}, common...)
	}
	if m.timelineActive() {
		return append([][2]string{{"j/k", "移動"}, {"l/h", "サブタスク"}, {"[/]", "週"}, {"{/}", "幅"}, {".", "今日"}, {"enter", "詳細"}, {"v", "表示"}}, common...)
	}
	return append([][2]string{{"j/k", "移動"}, {"l/h", "サブタスク"}, {"enter", "詳細"}, {"x", "完了"}, {"a", "追加"}, {"e", "編集"}, {"/", "検索"}}, common...)
}
