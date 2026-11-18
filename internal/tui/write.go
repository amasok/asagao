package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/richtext"
)

// Writes (complete / create / comment) are applied to the local store immediately and
// queued in the outbox; a sync is then requested to send them. Offline, they simply wait.

type promptKind int

const (
	promptNone promptKind = iota
	promptCreate
	promptComment
	promptRenameSection
	promptSubtask
	promptDue
	promptNewTag
	promptDuplicate
	promptAttach
	promptNewProject
	promptField     // a custom field (fields.go)
	promptDepSearch // search for a task to link (deps.go)
	promptNewSection
	promptRenameTag
	promptTimeAdd  // record time on a task (timetracking.go)
	promptTimeEdit // change a time tracking entry
)

type outboxCounts struct{ pending, conflict, failed int }

type (
	outboxLoadedMsg struct {
		counts   outboxCounts
		ops      []outbox.Op
		discards []readmodel.Discard // dropped changes to announce (now marked as notified)
		recent   []readmodel.Discard // recently dropped changes, to show in full
		// discardErr: the dropped changes could not be read; the outbox itself still opens.
		discardErr error
		err        error
	}
	writeDoneMsg struct {
		status  string
		gid     string // task to select afterwards
		refresh string // task to fetch again from Asana (after dropping a local change)
		err     error
	}
)

func (m *Model) loadOutbox() tea.Cmd {
	return tea.Batch(m.loadOutboxOnly(), m.loadPlan())
}

func (m *Model) loadOutboxOnly() tea.Cmd {
	st := m.deps.Store
	return func() tea.Msg {
		var c outboxCounts
		var err error
		c.pending, c.conflict, c.failed, err = queryOf(st).OutboxCounts(m.ctx)
		if err != nil {
			return outboxLoadedMsg{err: err}
		}
		ops, err := queryOf(st).ListOps(m.ctx)
		if err != nil {
			return outboxLoadedMsg{err: err}
		}
		// Changes dropped because their task was deleted in Asana are announced once
		// (here or by the CLI); if marking fails they are announced again next time.
		// Changes deleted by a reset are not announced: the user confirmed them and was
		// shown the backup (they are still listed in the overlay).
		// The dropped changes are extra information: if they cannot be read (e.g. one bad
		// row), the outbox still opens so conflicts and failures can be resolved.
		pending, discardErr := queryOf(st).PendingDiscards(m.ctx)
		var discards []readmodel.Discard
		if discardErr == nil && len(pending) > 0 {
			ids := make([]int64, len(pending))
			for i, d := range pending {
				ids[i] = d.ID
				if !readmodel.IsResetDiscard(d) {
					discards = append(discards, d)
				}
			}
			if err := st.MarkDiscardsNotified(m.ctx, ids); err != nil {
				discards = nil
			}
		}
		recent, err := queryOf(st).RecentDiscards(m.ctx, recentDiscards)
		if err != nil {
			discardErr = err
		}
		return outboxLoadedMsg{counts: c, ops: ops, discards: discards, recent: recent, discardErr: discardErr}
	}
}

// requestSync sends queued writes as soon as possible.
func (m *Model) requestSync() tea.Cmd {
	if m.syncing {
		m.syncAgain = true
		return nil
	}
	return m.startSync()
}

// queryOf reads the local cache (app.Query over the store).
func queryOf(st Store) app.Query { return app.Query{Store: st} }

// tasks returns the task use cases on the cache, with the clock stopped at now: the time
// the key was pressed, which the TUI's writes use. The use cases only write locally and
// queue in the outbox; sending is afterWrite's sync, as for every other write.
func (m *Model) tasks(now time.Time) *app.Tasks {
	return &app.Tasks{Store: m.deps.Store, Text: richtext.Converter{}, Clock: func() time.Time { return now }}
}

// comments is the comment use case, with a clock that reads now (see tasks).
func (m *Model) comments(now time.Time) *app.Comments {
	return &app.Comments{Store: m.deps.Store, Text: richtext.Converter{}, Clock: func() time.Time { return now }}
}

// statusUpdates is the status update use cases, with a clock that reads now (see tasks).
func (m *Model) statusUpdates(now time.Time) *app.Status {
	return &app.Status{Store: m.deps.Store, Text: richtext.Converter{}, Clock: func() time.Time { return now }}
}

// relations is the use cases on a task's relations, with a clock that reads now (see tasks).
func (m *Model) relations(now time.Time) *app.Relations {
	return &app.Relations{Store: m.deps.Store, Clock: func() time.Time { return now }}
}

// sections is the use cases on sections, with a clock that reads now (see tasks).
func (m *Model) sections(now time.Time) *app.Sections {
	return &app.Sections{Store: m.deps.Store, Clock: func() time.Time { return now }}
}

// copies is the duplicate and attach use cases, with a clock that reads now (see tasks).
func (m *Model) copies(now time.Time) *app.Copies {
	return &app.Copies{Store: m.deps.Store, CacheDir: m.deps.CacheDir, Clock: func() time.Time { return now }}
}

// notifications is the inbox use cases, with a clock that reads now (see tasks).
func (m *Model) notifications(now time.Time) *app.Notifications {
	return &app.Notifications{Store: m.deps.Store, Clock: func() time.Time { return now }}
}

// afterWrite reloads everything a write can change and asks for a sync.
func (m *Model) afterWrite() tea.Cmd {
	cmds := []tea.Cmd{m.loadScopes(), m.loadTasks(), m.loadOutbox(), m.requestSync()}
	if gid := m.detailGID(); gid != "" {
		cmds = append(cmds, m.loadDetail(gid))
	}
	return tea.Batch(cmds...)
}

func (m *Model) updateWrite(msg tea.Msg) (tea.Model, tea.Cmd, bool) {
	if cmd, ok := m.updateBulk(msg); ok {
		return m, cmd, true
	}
	if cmd, ok := m.updateFields(msg); ok {
		return m, cmd, true
	}
	if cmd, ok := m.updateDeps(msg); ok {
		return m, cmd, true
	}
	if cmd, ok := m.updateTaskOps(msg); ok {
		return m, cmd, true
	}
	if cmd, ok := m.updateSectionTags(msg); ok {
		return m, cmd, true
	}
	if cmd, ok := m.updateTimeTracking(msg); ok {
		return m, cmd, true
	}
	switch msg := msg.(type) {
	case outboxLoadedMsg:
		if msg.err == nil {
			m.outbox, m.ops = msg.counts, msg.ops
			m.opCursor = clamp(m.opCursor, 0, max(0, len(m.ops)-1))
			m.discards = msg.recent
			if len(msg.discards) > 0 {
				m.status = m.discardStatus(msg.discards)
			}
			if msg.discardErr != nil {
				m.status = "破棄した変更の記録を読み込めませんでした（未送信の変更の一覧は使えます）: " + msg.discardErr.Error()
			}
		}
		return m, nil, true
	case taskTagsMsg:
		if msg.err != nil {
			m.status = "タグを読み込めませんでした: " + msg.err.Error()
		} else {
			m.showTagEditor(msg)
		}
		return m, nil, true
	case followersMsg:
		if msg.err != nil {
			m.status = "メンバーを読み込めませんでした: " + msg.err.Error()
		} else {
			m.showFollowers(msg)
		}
		return m, nil, true
	case relationToggledMsg:
		if msg.kind == pickFollowers {
			return m, tea.Batch(m.afterWrite(), m.openFollowersFor(msg.task)), true
		}
		m.showProjects(msg.task)
		return m, m.afterWrite(), true
	case tagToggledMsg:
		return m, tea.Batch(m.afterWrite(), m.openTagEditorFor(msg.task)), true
	case imagesReadyMsg:
		return m, m.onImagesReady(msg), true
	case usersLoadedMsg:
		if msg.err != nil {
			m.status = "メンバーを読み込めませんでした: " + msg.err.Error()
		} else {
			m.showAssigneePicker(msg.users)
		}
		return m, nil, true
	case tagsLoadedMsg:
		if msg.err != nil {
			m.status = "タグを読み込めませんでした: " + msg.err.Error()
		} else {
			m.showTagPicker(msg.tags)
		}
		return m, nil, true
	case resetAskMsg, resetDoneMsg:
		cmd, _ := m.updateReset(msg)
		return m, cmd, true
	case writeDoneMsg:
		if msg.err != nil {
			m.status = "エラー: " + msg.err.Error()
			return m, nil, true
		}
		m.status = msg.status
		if msg.gid != "" {
			m.selectAfter = msg.gid
		}
		if msg.refresh != "" {
			return m, tea.Batch(m.afterWrite(), m.fetchDetails(msg.refresh)), true
		}
		return m, m.afterWrite(), true
	}
	return m, nil, false
}

func (m *Model) writeKey(key string) tea.Cmd {
	t := m.actionTask()
	switch key {
	case "x":
		if sub := m.selectedSubtask(); sub != nil {
			return m.toggleComplete(*sub) // the subtask selected in the detail view
		}
		if t == nil || m.focus == paneSidebar {
			return nil
		}
		return m.toggleComplete(*t)
	case "e":
		if t == nil || m.focus == paneSidebar {
			return nil
		}
		return m.editTask(t.GID)
	case "C":
		if t == nil || m.focus == paneSidebar {
			return nil
		}
		return m.editComment(t.GID)
	case "a":
		m.openPrompt(promptCreate)
		return m.prompt.Focus()
	case "c":
		if t == nil || m.focus == paneSidebar {
			return nil
		}
		m.openPrompt(promptComment)
		m.commentGID = t.GID
		return m.prompt.Focus()
	case "!":
		m.showOutbox, m.outboxTop = true, 0
		return m.loadOutbox()
	}
	return nil
}

func (m *Model) toggleComplete(t readmodel.TaskSummary) tea.Cmd {
	done := !t.Completed
	if done {
		return m.updateTask(t, app.TaskUpdate{Completed: &done}, "完了にしました: "+t.Name)
	}
	return m.updateTask(t, app.TaskUpdate{Completed: &done}, "未完了に戻しました: "+t.Name)
}

func (m *Model) openPrompt(kind promptKind) {
	m.promptMode = kind
	m.prompt.Reset()
	switch kind {
	case promptCreate:
		m.prompt.Prompt = "新しいタスク: "
		m.prompt.Placeholder = "タスク名  （例: 牛乳を買う @me 明日 #買い物 / +3d / 2026-12-01）"
	case promptComment:
		m.prompt.Prompt = "コメント: "
		m.prompt.Placeholder = "Enter で投稿 / Esc で取り消し"
	case promptRenameSection:
		m.prompt.Prompt = "セクション名: "
		m.prompt.Placeholder = "Enter で変更 / Esc で取り消し"
	case promptNewSection:
		m.prompt.Prompt = "新しいセクション: "
		m.prompt.Placeholder = "Enter で追加 / Esc で取り消し"
	case promptRenameTag:
		m.prompt.Prompt = "タグ名: "
		m.prompt.Placeholder = "Enter で変更 / Esc で取り消し"
	case promptTimeAdd, promptTimeEdit:
		m.prompt.Prompt = "作業時間: "
		m.prompt.Placeholder = "1h30m / 45m / 1:30 のあとに日付（昨日 / 2026-11-12。省略で今日）"
	case promptSubtask:
		m.prompt.Prompt = "サブタスク: "
		m.prompt.Placeholder = "サブタスク名  （例: 見積もりを取る @me +3d）"
	case promptDue:
		m.prompt.Prompt = "期日: "
		m.prompt.Placeholder = "2026-12-01 / 明日 / +3d（空欄で期日なし）"
	case promptNewTag:
		m.prompt.Prompt = "新しいタグ: "
		m.prompt.Placeholder = "タグ名"
	case promptDuplicate:
		m.prompt.Prompt = "複製したタスクの名前: "
		m.prompt.Placeholder = ""
	case promptNewProject:
		m.prompt.Prompt = "新しいプロジェクト: "
		m.prompt.Placeholder = "プロジェクト名（すぐ Asana に作られます）"
	case promptDepSearch:
		m.prompt.Prompt = "タスクを検索: "
		m.prompt.Placeholder = "タスク名の一部"
	case promptAttach:
		m.prompt.Prompt = "添付するファイル: "
		m.prompt.Placeholder = "~/Downloads/見積書.pdf（ファイルのパス）"
	}
}

func (m *Model) handlePromptKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.promptMode = promptNone
		m.prompt.Blur()
		m.bulkDueTasks = nil // a bulk date prompt is cancelled too
		return m, nil
	case "enter":
		value := strings.TrimSpace(m.prompt.Value())
		kind := m.promptMode
		m.promptMode = promptNone
		m.prompt.Blur()
		if kind == promptDue { // empty clears the due date
			return m, m.submitDue(value)
		}
		if kind == promptField { // empty clears the field
			return m, m.submitField(value)
		}
		if value == "" {
			return m, nil
		}
		switch kind {
		case promptCreate:
			return m, m.createTask(value)
		case promptRenameSection:
			return m, m.renameSection(m.renameGID, value)
		case promptNewSection:
			return m, m.createSection(value)
		case promptRenameTag:
			return m, m.submitTagName(value)
		case promptTimeAdd, promptTimeEdit:
			return m, m.submitTime(kind, value)
		case promptSubtask:
			return m, m.createSubtask(m.subtaskParent, value)
		case promptNewTag:
			return m, m.submitNewTag(value)
		case promptDuplicate:
			return m, m.duplicate(value)
		case promptAttach:
			return m, m.attach(value)
		case promptNewProject:
			return m, m.createProject(value)
		case promptDepSearch:
			return m, m.openDependencyTargets(m.depTask, m.depDependents, value)
		}
		if m.commentGID != "" {
			return m, m.addComment(m.commentGID, value)
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.prompt, cmd = m.prompt.Update(msg)
	return m, cmd
}

// createTask creates a task in the current context: the selected project (and the section
// of the selected task), or — in My Tasks — the default workspace, assigned to me. The
// assignee, due date and tags may be at the end of input (quick add, see quickCreate).
func (m *Model) createTask(input string) tea.Cmd {
	now := m.deps.Now()
	var defaultDue string
	if m.calendarActive() {
		defaultDue = m.calDay.Format(time.DateOnly) // the day selected in the calendar
	}
	sc := m.currentScope()
	var in app.CreateHereInput
	if sc.kind == scopeProject {
		in.Project = sc.project
		if t := m.selectedTask(); t != nil {
			for _, ms := range t.Memberships {
				if ms.ProjectGID == sc.project {
					in.Section = ms.SectionGID
				}
			}
		}
	}
	return m.quickCreate(input, now, in, defaultDue, "タスクを作成しました: ", false)
}

func (m *Model) addComment(taskGID, text string) tea.Cmd {
	comments := m.comments(m.deps.Now())
	return func() tea.Msg {
		// Markdown -> Asana rich text, like the comment editor (C), the CLI and MCP
		if _, err := comments.Add(m.ctx, taskGID, text); err != nil {
			return writeDoneMsg{err: err}
		}
		return writeDoneMsg{status: "コメントしました"}
	}
}

// --- outbox overlay ---

func (m *Model) handleOutboxKey(key string) (tea.Model, tea.Cmd) {
	var op *outbox.Op
	if m.opCursor < len(m.ops) {
		op = &m.ops[m.opCursor]
	}
	resolve := func(action app.ResolveAction, status string) tea.Cmd {
		if op == nil {
			return nil
		}
		if op.State == outbox.StateSending { // the keys do nothing while Push has it
			m.status = "送信中の変更は操作できません（送信が終わると結果が表示されます）"
			return nil
		}
		st, id := m.deps.Store, op.ID
		return func() tea.Msg {
			r, err := (&app.Outbox{Store: st}).Resolve(m.ctx, id, action)
			if err != nil {
				return writeDoneMsg{err: err}
			}
			// afterWrite requests a sync, which sends a requeued entry.
			return writeDoneMsg{status: status, refresh: r.RefreshGID}
		}
	}
	switch key {
	case "esc", "q", "!":
		m.showOutbox = false
	case "j", "down":
		m.opCursor = clamp(m.opCursor+1, 0, max(0, len(m.ops)-1))
		m.showOpCursor()
	case "k", "up":
		m.opCursor = clamp(m.opCursor-1, 0, max(0, len(m.ops)-1))
		m.showOpCursor()
	case "ctrl+d", "pgdown": // scroll, e.g. through the full text of dropped changes
		m.outboxTop += max(1, m.outboxHeight()/2)
	case "ctrl+u", "pgup":
		m.outboxTop = max(0, m.outboxTop-max(1, m.outboxHeight()/2))
	case "l":
		return m, resolve(app.KeepLocal, "こちらの値で上書きします")
	case "s":
		return m, resolve(app.KeepRemote, "Asana の値を採用しました")
	case "d":
		return m, resolve(app.Discard, "取り消しました")
	case "R":
		return m, resolve(app.Retry, "もう一度送ります")
	}
	return m, nil
}

// discardStatus is the one-line notice for changes dropped by a push.
func (m *Model) discardStatus(ds []readmodel.Discard) string {
	s := readmodel.DiscardNotice + ": " + readmodel.DiscardLine(ds[0])
	if len(ds) > 1 {
		s += fmt.Sprintf(" ほか %d 件（%s で一覧）", len(ds)-1, m.actionKey("outbox"))
	}
	return s
}

// actionKey is how to run action id with the user's key bindings: its first shortcut, or
// the space menu key.
func (m *Model) actionKey(id string) string {
	for _, a := range m.withBindings(m.actions()) {
		if a.id == id {
			if len(a.shortcut) > 0 {
				return a.shortcut[0]
			}
			return "space " + a.key
		}
	}
	return "space"
}

// opStateWidth fits the widest state label, "[自動確認中]"; detail lines under an entry
// start where its description does.
const opStateWidth = 12

var opDetailIndent = strings.Repeat(" ", opStateWidth+1)

// recentDiscards is how many dropped changes the outbox overlay lists.
const recentDiscards = 20

// outboxHeight is the number of overlay lines on screen (see View and pane).
func (m *Model) outboxHeight() int { return max(1, max(3, m.height-2)-3) }

// showOpCursor scrolls the overlay so that the selected entry is on screen.
func (m *Model) showOpCursor() {
	line := m.outboxCursorLine()
	if line < m.outboxTop {
		m.outboxTop = line
	} else if line >= m.outboxTop+m.outboxHeight() {
		m.outboxTop = line - m.outboxHeight() + 1
	}
}

// outboxCursorLine is the overlay line of the selected entry (see outboxLines).
func (m *Model) outboxCursorLine() int {
	line := 2 // help, blank
	for _, op := range m.ops[:min(m.opCursor, len(m.ops))] {
		line++
		line += len(readmodel.ConflictLines(op))
		if _, text := readmodel.OpNote(op); text != "" {
			line++
		}
	}
	return line
}

func (m *Model) renderOutbox(w, h int) string {
	lines := m.outboxLines(w - 4)
	visible := max(1, h-3)
	m.outboxTop = clamp(m.outboxTop, 0, max(0, len(lines)-visible))
	return m.pane("未送信の変更", lines[m.outboxTop:], w, h, true)
}

// outboxLines are all the lines of the outbox overlay for an inner width.
func (m *Model) outboxLines(inner int) []string {
	lines := []string{
		m.st.dim.Render("j/k 移動   l こちらの値で上書き（競合）   s Asana の値を採用（競合）   d 取り消し   R 再送（失敗）   ctrl+d/u スクロール   esc 閉じる"),
		"",
	}
	if len(m.ops) == 0 {
		lines = append(lines, "未送信の変更はありません")
	}
	for i, op := range m.ops {
		style := m.st.dim // pending, sending (Push has it; nothing to resolve)
		switch op.State {
		case outbox.StateConflict:
			style = m.st.today
		case outbox.StateFailed:
			style = m.st.errorText
		}
		line := style.Render(padRight("["+readmodel.OpLabel(op)+"]", opStateWidth)) + " " + readmodel.DescribeOp(op)
		lines = append(lines, m.highlight(line, inner, i == m.opCursor, true))
		for _, c := range readmodel.ConflictLines(op) {
			lines = append(lines, opDetailIndent+c)
		}
		if _, text := readmodel.OpNote(op); text != "" {
			note := m.st.dim // e.g. being checked against Asana: nothing to do
			if op.State == outbox.StateFailed {
				note = m.st.errorText
			}
			lines = append(lines, note.Render(opDetailIndent+text))
		}
	}
	if len(m.discards) > 0 {
		lines = append(lines, "", m.st.dim.Render("最近破棄した変更（Asana で削除されていたもの・初期化で消したもの。書いた内容は下に全文があります）"))
		for _, d := range m.discards {
			lines = append(lines, "  "+readmodel.DiscardLine(d)+m.st.dim.Render("  "+shortTime(d.DiscardedAt)))
			for _, dt := range readmodel.DiscardDetails(d, richtext.ToEditableMarkdown) {
				lines = append(lines, m.st.dim.Render("    "+dt.Label+":"))
				for _, l := range strings.Split(ansi.Wrap(dt.Text, max(10, inner-6), ""), "\n") {
					lines = append(lines, "      "+l)
				}
			}
		}
	}
	return lines
}

// shortTime formats an RFC 3339 time as local "01/02 15:04" ("" if it does not parse).
func shortTime(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return ""
	}
	return t.Local().Format("01/02 15:04")
}
