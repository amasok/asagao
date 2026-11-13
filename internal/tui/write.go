package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asana-tui/internal/dates"
	"github.com/amasok/asana-tui/internal/richtext"
	"github.com/amasok/asana-tui/internal/store"
	"github.com/amasok/asana-tui/internal/sync"
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
)

type outboxCounts struct{ pending, conflict, failed int }

type (
	outboxLoadedMsg struct {
		counts outboxCounts
		ops    []store.Op
		err    error
	}
	writeDoneMsg struct {
		status  string
		gid     string // task to select afterwards
		refresh string // task to fetch again from Asana (after dropping a local change)
		err     error
	}
)

func (m *Model) loadOutbox() tea.Cmd {
	st := m.deps.Store
	return func() tea.Msg {
		var c outboxCounts
		var err error
		c.pending, c.conflict, c.failed, err = st.OutboxCounts(m.ctx)
		if err != nil {
			return outboxLoadedMsg{err: err}
		}
		ops, err := st.ListOps(m.ctx)
		return outboxLoadedMsg{counts: c, ops: ops, err: err}
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

// afterWrite reloads everything a write can change and asks for a sync.
func (m *Model) afterWrite() tea.Cmd {
	cmds := []tea.Cmd{m.loadScopes(), m.loadTasks(), m.loadOutbox(), m.requestSync()}
	if gid := m.detailGID(); gid != "" {
		cmds = append(cmds, m.loadDetail(gid))
	}
	return tea.Batch(cmds...)
}

func (m *Model) updateWrite(msg tea.Msg) (tea.Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case outboxLoadedMsg:
		if msg.err == nil {
			m.outbox, m.ops = msg.counts, msg.ops
			m.opCursor = clamp(m.opCursor, 0, max(0, len(m.ops)-1))
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
		m.showOutbox = true
		return m.loadOutbox()
	}
	return nil
}

func (m *Model) toggleComplete(t store.TaskSummary) tea.Cmd {
	st, now := m.deps.Store, m.deps.Now()
	return func() tea.Msg {
		done := !t.Completed
		if err := st.UpdateTaskLocal(m.ctx, t.GID, map[string]any{"completed": done}, now); err != nil {
			return writeDoneMsg{err: err}
		}
		if done {
			return writeDoneMsg{status: "完了にしました: " + t.Name}
		}
		return writeDoneMsg{status: "未完了に戻しました: " + t.Name}
	}
}

func (m *Model) openPrompt(kind promptKind) {
	m.promptMode = kind
	m.prompt.Reset()
	switch kind {
	case promptCreate:
		m.prompt.Prompt = "新しいタスク: "
		m.prompt.Placeholder = "タスク名  （例: 牛乳を買う due:明日 / due:+3d / due:2026-12-01）"
	case promptComment:
		m.prompt.Prompt = "コメント: "
		m.prompt.Placeholder = "Enter で投稿 / Esc で取り消し"
	case promptRenameSection:
		m.prompt.Prompt = "セクション名: "
		m.prompt.Placeholder = "Enter で変更 / Esc で取り消し"
	case promptSubtask:
		m.prompt.Prompt = "サブタスク: "
		m.prompt.Placeholder = "サブタスク名  （例: 見積もりを取る due:+3d）"
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
		return m, nil
	case "enter":
		value := strings.TrimSpace(m.prompt.Value())
		kind := m.promptMode
		m.promptMode = promptNone
		m.prompt.Blur()
		if kind == promptDue { // empty clears the due date
			return m, m.submitDue(value)
		}
		if value == "" {
			return m, nil
		}
		switch kind {
		case promptCreate:
			return m, m.createTask(value)
		case promptRenameSection:
			return m, m.renameSection(m.renameGID, value)
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
// of the selected task), or — in My Tasks — the default workspace, assigned to me.
func (m *Model) createTask(input string) tea.Cmd {
	name, due, err := parseCreateInput(input, m.deps.Now())
	if err != nil {
		return func() tea.Msg { return writeDoneMsg{err: err} }
	}
	if due == "" && m.calendarActive() {
		due = m.calDay.Format(time.DateOnly) // the day selected in the calendar
	}
	sc := m.currentScope()
	var section string
	if sc.kind == scopeProject {
		if t := m.selectedTask(); t != nil {
			for _, ms := range t.Memberships {
				if ms.ProjectGID == sc.project {
					section = ms.SectionGID
				}
			}
		}
	}
	st, now := m.deps.Store, m.deps.Now()
	return func() tea.Msg {
		nt := store.NewTask{Name: name, DueOn: due}
		var err error
		if sc.kind == scopeProject {
			nt.ProjectGID, nt.SectionGID = sc.project, section
			if nt.SectionGID == "" {
				if nt.SectionGID, err = st.FirstSection(m.ctx, sc.project); err != nil {
					return writeDoneMsg{err: err}
				}
			}
		} else {
			if nt.WorkspaceGID, err = st.DefaultWorkspace(m.ctx); err != nil {
				return writeDoneMsg{err: err}
			}
			if nt.AssigneeGID, err = st.MeGID(m.ctx); err != nil {
				return writeDoneMsg{err: err}
			}
		}
		gid, err := st.CreateTaskLocal(m.ctx, nt, now)
		if err != nil {
			return writeDoneMsg{err: err}
		}
		return writeDoneMsg{status: "タスクを作成しました: " + name, gid: gid}
	}
}

func (m *Model) addComment(taskGID, text string) tea.Cmd {
	st, now := m.deps.Store, m.deps.Now()
	return func() tea.Msg {
		me, err := st.Me(m.ctx)
		if err != nil {
			return writeDoneMsg{err: err}
		}
		// Markdown -> Asana rich text, like the comment editor (C), the CLI and MCP
		if _, err := st.AddRichCommentLocal(m.ctx, taskGID, richtext.PlainFromMarkdown(text), richtext.FromMarkdown(text), me, now); err != nil {
			return writeDoneMsg{err: err}
		}
		return writeDoneMsg{status: "コメントしました"}
	}
}

// parseCreateInput splits "name due:<date>" (date: 今日/明日/today/tomorrow/+Nd/YYYY-MM-DD).
func parseCreateInput(input string, now time.Time) (name, due string, err error) {
	var words []string
	for _, w := range strings.Fields(input) {
		v, ok := strings.CutPrefix(w, "due:")
		if !ok {
			words = append(words, w)
			continue
		}
		if due, err = dates.Parse(v, now); err != nil {
			return "", "", err
		}
	}
	return strings.Join(words, " "), due, nil
}

// --- outbox overlay ---

func (m *Model) handleOutboxKey(key string) (tea.Model, tea.Cmd) {
	var op *store.Op
	if m.opCursor < len(m.ops) {
		op = &m.ops[m.opCursor]
	}
	resolve := func(action sync.ResolveAction, status string) tea.Cmd {
		if op == nil {
			return nil
		}
		st, id := m.deps.Store, op.ID
		return func() tea.Msg {
			r, err := sync.Resolve(m.ctx, st, id, action)
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
	case "k", "up":
		m.opCursor = clamp(m.opCursor-1, 0, max(0, len(m.ops)-1))
	case "l":
		return m, resolve(sync.KeepLocal, "こちらの値で上書きします")
	case "s":
		return m, resolve(sync.KeepRemote, "Asana の値を採用しました")
	case "d":
		return m, resolve(sync.Discard, "取り消しました")
	case "R":
		return m, resolve(sync.Retry, "もう一度送ります")
	}
	return m, nil
}

func (m *Model) renderOutbox(w, h int) string {
	lines := []string{
		m.st.dim.Render("j/k 移動   l こちらの値で上書き（競合）   s Asana の値を採用（競合）   d 取り消し   R 再送（失敗）   esc 閉じる"),
		"",
	}
	if len(m.ops) == 0 {
		lines = append(lines, "未送信の変更はありません")
	}
	for i, op := range m.ops {
		style := m.st.dim // pending, sending (Push has it; nothing to resolve)
		switch op.State {
		case store.OpConflict:
			style = m.st.today
		case store.OpFailed:
			style = m.st.errorText
		}
		line := style.Render(padRight("["+store.OpLabel(op)+"]", 10)) + " " + store.DescribeOp(op)
		lines = append(lines, m.highlight(line, w-4, i == m.opCursor, true))
		for k, v := range op.Conflict {
			lines = append(lines, fmt.Sprintf("           %s: こちら=%v / Asana=%v", store.FieldLabel(k), op.Payload[k], v))
		}
		if op.LastError != "" {
			note := m.st.dim // e.g. waiting to check Asana
			if op.State == store.OpFailed {
				note = m.st.errorText
			}
			lines = append(lines, note.Render("           "+op.LastError))
		}
	}
	return m.pane("未送信の変更", lines, w, h, true)
}
