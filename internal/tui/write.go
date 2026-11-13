package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asana-tui/internal/dates"
	"github.com/amasok/asana-tui/internal/store"
)

// Writes (complete / create / comment) are applied to the local store immediately and
// queued in the outbox; a sync is then requested to send them. Offline, they simply wait.

type promptKind int

const (
	promptNone promptKind = iota
	promptCreate
	promptComment
	promptRenameSection
)

type outboxCounts struct{ pending, conflict, failed int }

type (
	outboxLoadedMsg struct {
		counts outboxCounts
		ops    []store.Op
		err    error
	}
	writeDoneMsg struct {
		status string
		gid    string // task to select afterwards
		err    error
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
	if t := m.selectedTask(); t != nil {
		cmds = append(cmds, m.loadDetail(t.GID))
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
	case imagesReadyMsg:
		return m, m.onImagesReady(msg), true
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
		return m, m.afterWrite(), true
	}
	return m, nil, false
}

func (m *Model) writeKey(key string) tea.Cmd {
	t := m.selectedTask()
	switch key {
	case "x":
		if t == nil || m.focus == paneSidebar {
			return nil
		}
		return m.toggleComplete(*t)
	case "a":
		m.openPrompt(promptCreate)
		return m.prompt.Focus()
	case "c":
		if t == nil || m.focus == paneSidebar {
			return nil
		}
		m.openPrompt(promptComment)
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
		if value == "" {
			return m, nil
		}
		switch kind {
		case promptCreate:
			return m, m.createTask(value)
		case promptRenameSection:
			return m, m.renameSection(m.renameGID, value)
		}
		if t := m.selectedTask(); t != nil {
			return m, m.addComment(*t, value)
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

func (m *Model) addComment(t store.TaskSummary, text string) tea.Cmd {
	st, now := m.deps.Store, m.deps.Now()
	return func() tea.Msg {
		me, err := st.Me(m.ctx)
		if err != nil {
			return writeDoneMsg{err: err}
		}
		if _, err := st.AddCommentLocal(m.ctx, t.GID, text, me, now); err != nil {
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
	st := m.deps.Store
	act := func(fn func() error, status string) tea.Cmd {
		return func() tea.Msg {
			if err := fn(); err != nil {
				return writeDoneMsg{err: err}
			}
			return writeDoneMsg{status: status}
		}
	}
	switch key {
	case "esc", "q", "!":
		m.showOutbox = false
	case "j", "down":
		m.opCursor = clamp(m.opCursor+1, 0, max(0, len(m.ops)-1))
	case "k", "up":
		m.opCursor = clamp(m.opCursor-1, 0, max(0, len(m.ops)-1))
	case "l": // keep local
		if op != nil && op.State == store.OpConflict {
			id := op.ID
			return m, act(func() error { return st.ResolveKeepLocal(m.ctx, id) }, "こちらの値で上書きします")
		}
	case "s", "d": // keep server / discard
		if op != nil {
			id, gid, kind := op.ID, op.TaskGID, op.Kind
			cmd := act(func() error { return st.DiscardOp(m.ctx, id) }, "取り消しました")
			if kind != store.OpCreateTask && m.syncer != nil {
				return m, tea.Batch(cmd, m.fetchDetails(gid))
			}
			return m, cmd
		}
	case "R":
		if op != nil && op.State == store.OpFailed {
			id := op.ID
			return m, act(func() error { return st.RetryOp(m.ctx, id) }, "もう一度送ります")
		}
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
		state := map[string]string{store.OpPending: "未送信", store.OpConflict: "競合", store.OpFailed: "失敗"}[op.State]
		style := m.st.dim
		switch op.State {
		case store.OpConflict:
			style = m.st.today
		case store.OpFailed:
			style = m.st.errorText
		}
		line := style.Render(padRight("["+state+"]", 8)) + " " + describeOp(op)
		lines = append(lines, m.highlight(line, w-4, i == m.opCursor, true))
		for k, v := range op.Conflict {
			lines = append(lines, fmt.Sprintf("           %s: こちら=%v / Asana=%v", fieldLabel(k), op.Payload[k], v))
		}
		if op.LastError != "" {
			lines = append(lines, m.st.errorText.Render("           "+op.LastError))
		}
	}
	return m.pane("未送信の変更", lines, w, h, true)
}

func fieldLabel(k string) string {
	return map[string]string{"completed": "完了", "name": "名前", "notes": "説明", "due_on": "期日", "assignee": "担当者"}[k]
}

func describeOp(op store.Op) string {
	name := op.TaskName
	if name == "" {
		name = op.TaskGID
	}
	switch op.Kind {
	case store.OpCreateTask:
		return "作成: " + name
	case store.OpMoveTask:
		return fmt.Sprintf("移動: %s → %v", name, op.Payload["section_name"])
	case store.OpRenameSection:
		return fmt.Sprintf("セクション名の変更: %v → %v", op.Base["name"], op.Payload["name"])
	case store.OpAddComment:
		text, _ := op.Payload["text"].(string)
		return fmt.Sprintf("コメント: %s「%s」", name, text)
	}
	var parts []string
	for _, k := range []string{"completed", "name", "notes", "due_on", "assignee"} {
		v, ok := op.Payload[k]
		if !ok {
			continue
		}
		switch {
		case k == "completed" && v == true:
			parts = append(parts, "完了にする")
		case k == "completed":
			parts = append(parts, "未完了に戻す")
		default:
			parts = append(parts, fmt.Sprintf("%s→%v", fieldLabel(k), v))
		}
	}
	return name + ": " + strings.Join(parts, "、")
}
