package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/amasok/asana-tui/internal/store"
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

	ctxTask = ctxList | ctxBoard | ctxDetail // a task is selected / shown
	ctxAll  = ctxSidebar | ctxTask
)

func (m *Model) keyContext() keyCtx {
	switch {
	case m.inDetail():
		return ctxDetail
	case m.focus == paneSidebar:
		return ctxSidebar
	case m.boardActive():
		return ctxBoard
	}
	return ctxList
}

type action struct {
	key      string   // key in the space menu
	shortcut []string // keys that work directly
	group    string
	label    string
	ctx      keyCtx
	needTask bool // needs a selected (or shown) task
	run      func(m *Model) tea.Cmd
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
		{key: "a", shortcut: []string{"a"}, group: groupTask, label: "タスクを追加", ctx: ctxAll, run: func(m *Model) tea.Cmd { return m.writeKey("a") }},
		{key: "s", shortcut: []string{"S"}, group: groupTask, label: "サブタスクを追加", ctx: ctxTask, needTask: true, run: (*Model).startSubtask},
		{key: "x", shortcut: []string{"x"}, group: groupTask, label: "完了 / 未完了", ctx: ctxTask, needTask: true, run: func(m *Model) tea.Cmd { return m.writeKey("x") }},
		{key: "e", shortcut: []string{"e"}, group: groupTask, label: "名前・説明をエディタで編集", ctx: ctxTask, needTask: true, run: func(m *Model) tea.Cmd { return m.writeKey("e") }},
		{key: "d", group: groupTask, label: "期日を変更", ctx: ctxTask, needTask: true, run: (*Model).openDuePicker},
		{key: "u", shortcut: []string{"@"}, group: groupTask, label: "担当者を変更", ctx: ctxTask, needTask: true, run: (*Model).openAssigneePicker},
		{key: "t", group: groupTask, label: "タグを付ける / 外す", ctx: ctxTask, needTask: true, run: (*Model).openTagEditor},
		{key: "m", shortcut: []string{"m"}, group: groupTask, label: "セクションを移動", ctx: ctxTask, needTask: true, run: func(m *Model) tea.Cmd { m.openSectionPicker(); return nil }},
		{key: "c", shortcut: []string{"c"}, group: groupTask, label: "コメント（1 行）", ctx: ctxTask, needTask: true, run: func(m *Model) tea.Cmd { return m.writeKey("c") }},
		{key: "C", shortcut: []string{"C"}, group: groupTask, label: "コメントをエディタで書く", ctx: ctxTask, needTask: true, run: func(m *Model) tea.Cmd { return m.writeKey("C") }},
		{key: "i", shortcut: []string{"i"}, group: groupTask, label: "画像を全画面で見る", ctx: ctxTask, needTask: true, run: (*Model).viewImages},
		{key: "o", shortcut: []string{"o"}, group: groupTask, label: "ブラウザで開く", ctx: ctxTask, needTask: true, run: (*Model).openInBrowser},
		// view
		{key: "f", shortcut: []string{"f"}, group: groupView, label: "絞り込み（未完了・期限切れ…）", ctx: ctxAll, run: (*Model).openFilterPicker},
		{key: "T", shortcut: []string{"T"}, group: groupView, label: "タグで絞り込む", ctx: ctxAll, run: (*Model).openTagPicker},
		{key: "v", shortcut: []string{"v"}, group: groupView, label: "ボード / リスト", ctx: ctxList | ctxBoard, run: func(m *Model) tea.Cmd { return m.viewKey("v") }},
		{key: "z", shortcut: []string{"z"}, group: groupView, label: "詳細を全画面 / 戻す", ctx: ctxTask, needTask: true, run: func(m *Model) tea.Cmd { return m.viewKey("z") }},
		{key: "-", shortcut: []string{"-"}, group: groupView, label: "このセクションを折りたたむ / 開く", ctx: ctxList, run: (*Model).toggleGroup},
		{key: "_", shortcut: []string{"_"}, group: groupView, label: "すべて折りたたむ", ctx: ctxList, run: func(m *Model) tea.Cmd { return m.setAllGroups(true) }},
		{key: "+", shortcut: []string{"+", "="}, group: groupView, label: "すべて開く", ctx: ctxList, run: func(m *Model) tea.Cmd { return m.setAllGroups(false) }},
		{key: "b", shortcut: []string{"b"}, group: groupView, label: "プロジェクト一覧を隠す / 出す", ctx: ctxAll, run: func(m *Model) tea.Cmd { return m.layoutKey("b") }},
		{key: ">", shortcut: []string{">"}, group: groupView, label: "詳細を広く", ctx: ctxAll, run: func(m *Model) tea.Cmd { return m.layoutKey(">") }},
		{key: "<", shortcut: []string{"<"}, group: groupView, label: "詳細を狭く", ctx: ctxAll, run: func(m *Model) tea.Cmd { return m.layoutKey("<") }},
		// project
		{key: "E", shortcut: []string{"E"}, group: groupProject, label: "セクション名を変更", ctx: ctxList | ctxBoard | ctxDetail, run: (*Model).startRenameSection},
		// other
		{key: "r", shortcut: []string{"r"}, group: groupOther, label: "今すぐ同期", ctx: ctxAll, run: (*Model).syncNow},
		{key: "!", shortcut: []string{"!"}, group: groupOther, label: "未送信の変更・競合", ctx: ctxAll, run: func(m *Model) tea.Cmd { return m.writeKey("!") }},
		{key: "?", group: groupOther, label: "ヘルプ（すべてのキー）", ctx: ctxAll, run: func(m *Model) tea.Cmd { m.showHelp = true; return nil }},
		{key: "q", group: groupOther, label: "終了", ctx: ctxAll, run: func(*Model) tea.Cmd { return tea.Quit }},
	}
}

// available returns the actions usable right now, in menu order.
func (m *Model) available() []action {
	ctx := m.keyContext()
	hasTask := m.actionTask() != nil
	var out []action
	for _, a := range m.actions() {
		if a.ctx&ctx == 0 || (a.needTask && !hasTask) {
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
	if store.IsLocalGID(t.GID) {
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

// renderMenu draws the which-key style menu: the actions available here, grouped.
func (m *Model) renderMenu(w int) []string {
	groups := map[string][]action{}
	for _, a := range m.available() {
		groups[a.group] = append(groups[a.group], a)
	}
	var cols [][]string
	for _, g := range groupOrder {
		if len(groups[g]) == 0 {
			continue
		}
		col := []string{m.st.paneTitle.Render(g)}
		for _, a := range groups[g] {
			col = append(col, m.st.footerKey.Render(padRight(a.key, 2))+" "+a.label)
		}
		cols = append(cols, col)
	}
	return layoutColumns(cols, w-4)
}

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
	}
	return append([][2]string{{"j/k", "移動"}, {"l/h", "サブタスク"}, {"enter", "詳細"}, {"x", "完了"}, {"a", "追加"}, {"e", "編集"}, {"/", "検索"}}, common...)
}
