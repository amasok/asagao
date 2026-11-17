package tui

import (
	"errors"
	"fmt"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/sync"
)

// Project-level actions: status updates (view / write in the editor, offline too),
// creating, archiving and members (online only).

func (m *Model) currentProject() string {
	if sc := m.currentScope(); sc.kind == scopeProject {
		return sc.project
	}
	return ""
}

// online returns the syncer for operations that must reach Asana now.
func (m *Model) online() (*sync.Syncer, bool) {
	if m.syncer == nil {
		m.status = "オフラインのためできません（オンラインになって同期が終わってから試してください）"
		return nil, false
	}
	return m.syncer, true
}

// --- status updates ---

type statusesLoadedMsg struct {
	project string
	updates []readmodel.StatusUpdateView
	err     error
}

func (m *Model) openStatuses() tea.Cmd {
	project := m.currentProject()
	if project == "" {
		return nil
	}
	m.statusView = true
	m.statusOffset = 0
	st, syncer := m.deps.Store, m.syncer
	load := func() tea.Msg {
		updates, err := queryOf(st).StatusUpdates(m.ctx, project)
		return statusesLoadedMsg{project: project, updates: updates, err: err}
	}
	if syncer == nil {
		return load
	}
	return tea.Batch(load, func() tea.Msg { // refresh from Asana, then show again
		_ = syncer.FetchStatusUpdates(m.ctx, project)
		return load()
	})
}

func (m *Model) handleStatusKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc", "q", "U":
		m.statusView = false
	case "j", "down":
		m.statusOffset++
	case "k", "up":
		m.statusOffset = max(0, m.statusOffset-1)
	case "g", "home":
		m.statusOffset = 0
	case "ctrl+d", "pgdown":
		m.statusOffset += 10
	case "ctrl+u", "pgup":
		m.statusOffset = max(0, m.statusOffset-10)
	case "W", "a":
		m.statusView = false
		return m, m.writeStatus()
	}
	return m, nil
}

func (m *Model) renderStatuses(w, h int) string {
	lines := []string{m.st.dim.Render("j/k スクロール  W 新しいステータスを書く  esc 閉じる"), ""}
	if len(m.statuses) == 0 {
		lines = append(lines, "ステータス更新はまだありません（W で書く）")
	}
	for i, u := range m.statuses {
		if i > 0 {
			lines = append(lines, m.st.dim.Render(strings.Repeat("─", max(1, w-4))))
		}
		badge := m.statusBadge(u.StatusType)
		meta := badge + "  " + m.st.headerScope.Render(u.Title) + "  " + m.st.dim.Render(u.Author+"  "+commentTime(u.CreatedAt, m.deps.Now()))
		if u.Pending {
			meta += "  " + m.st.today.Render("（未送信）")
		}
		lines = append(lines, meta)
		lines = append(lines, strings.Split(m.markdown(textMarkdown(u.HTMLText, u.Text), max(10, w-6)), "\n")...)
	}
	m.statusOffset = clamp(m.statusOffset, 0, max(0, len(lines)-(h-3)))
	return m.pane("ステータス更新（"+m.currentScope().name+"）", lines[m.statusOffset:], w, h, true)
}

func (m *Model) statusBadge(t string) string {
	label := "[" + readmodel.StatusLabel(t) + "]"
	switch t {
	case "on_track", "complete":
		return m.st.tag.Render(label)
	case "at_risk", "on_hold":
		return m.st.today.Render(label)
	case "off_track":
		return m.st.overdue.Render(label)
	}
	return label
}

const statusHelp = `# asagao: プロジェクトのステータス更新です。保存してエディタを閉じると投稿します（空のまま閉じると取り消し）。
# status: on_track（順調）/ at_risk（要注意）/ off_track（遅れ）/ on_hold（保留）/ complete（完了）
# 本文はマークダウンで書けます。`

type (
	statusEditReadyMsg struct {
		project, path string
		err           error
	}
	statusEditDoneMsg struct {
		project, path string
		err           error
	}
)

func (m *Model) writeStatus() tea.Cmd {
	project := m.currentProject()
	if project == "" || m.deps.RunEditor == nil {
		return nil
	}
	last := "on_track"
	if sc := m.currentScope(); sc.status != "" {
		last = sc.status
	}
	title := m.deps.Now().Format("1/2") + " の状況"
	return func() tea.Msg {
		path, err := writeTemp("asagao-status-*.md", "---\n"+statusHelp+"\nstatus: "+last+"\ntitle: "+title+"\n---\n\n")
		return statusEditReadyMsg{project: project, path: path, err: err}
	}
}

func parseStatusDoc(s string) (status, title, body string, err error) {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", "", "", errors.New("先頭の --- が見つかりません")
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return "", "", "", errors.New("項目の終わりの --- が見つかりません")
	}
	for _, l := range lines[1:end] {
		if strings.HasPrefix(strings.TrimSpace(l), "#") || strings.TrimSpace(l) == "" {
			continue
		}
		k, v, _ := strings.Cut(l, ":")
		switch strings.TrimSpace(k) {
		case "status":
			status = strings.TrimSpace(v)
		case "title":
			title = strings.TrimSpace(v)
		}
	}
	return status, title, strings.TrimSpace(strings.Join(lines[end+1:], "\n")), nil
}

func (m *Model) updateProjects(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case statusesLoadedMsg:
		if msg.err == nil && msg.project == m.currentProject() {
			m.statuses = msg.updates
		}
		return nil, true
	case statusEditReadyMsg:
		if msg.err != nil {
			m.status = msg.err.Error()
			return nil, true
		}
		return m.deps.RunEditor(msg.path, func(err error) tea.Msg {
			return statusEditDoneMsg{project: msg.project, path: msg.path, err: err}
		}), true
	case statusEditDoneMsg:
		return m.applyStatus(msg), true
	case projectDoneMsg:
		if msg.err != nil {
			m.status = msg.err.Error()
			return nil, true
		}
		m.status = msg.status
		if msg.selectProject != "" {
			m.selectProjectAfter = msg.selectProject
		}
		if msg.toMyTasks {
			m.scopeCursor = 0
		}
		if msg.selectProject != "" || msg.toMyTasks {
			m.saved = nil // the sidebar moves: leave a saved search (searches.go)
		}
		return tea.Batch(m.loadScopes(), m.loadTasks()), true
	case membersMsg:
		if msg.err != nil {
			m.status = msg.err.Error()
		} else {
			m.showMembers(msg)
		}
		return nil, true
	}
	return nil, false
}

func (m *Model) applyStatus(msg statusEditDoneMsg) tea.Cmd {
	if msg.err != nil {
		m.status = "エディタがエラーで終了しました: " + msg.err.Error()
		return nil
	}
	data, err := os.ReadFile(msg.path)
	if err != nil {
		m.status = err.Error()
		return nil
	}
	status, title, body, err := parseStatusDoc(string(data))
	if err != nil {
		m.status = "読めませんでした（" + err.Error() + "）。内容は " + msg.path + " に残っています"
		return nil
	}
	os.Remove(msg.path)
	if body == "" {
		m.status = "ステータス更新を取り消しました"
		return nil
	}
	statuses, in := m.statusUpdates(m.deps.Now()), app.StatusInput{Project: msg.project, Status: status, Title: title, Text: body}
	return func() tea.Msg {
		if _, err := statuses.CreateHere(m.ctx, in); err != nil {
			return writeDoneMsg{err: err}
		}
		return writeDoneMsg{status: "ステータスを投稿しました（" + readmodel.StatusLabel(status) + "）"}
	}
}

// --- create / archive (online only) ---

type projectDoneMsg struct {
	status        string
	selectProject string
	toMyTasks     bool
	err           error
}

func (m *Model) startNewProject() tea.Cmd {
	if _, ok := m.online(); !ok {
		return nil
	}
	m.openPrompt(promptNewProject)
	return m.prompt.Focus()
}

func (m *Model) createProject(name string) tea.Cmd {
	syncer, ok := m.online()
	if !ok {
		return nil
	}
	return func() tea.Msg {
		p, err := syncer.CreateProject(m.ctx, name)
		if err != nil {
			return projectDoneMsg{err: err}
		}
		return projectDoneMsg{status: "プロジェクトを作りました: " + p.Name, selectProject: p.GID}
	}
}

func (m *Model) confirmArchive() tea.Cmd {
	project := m.currentProject()
	if project == "" {
		return nil
	}
	if _, ok := m.online(); !ok {
		return nil
	}
	m.picker = &picker{kind: pickArchive, title: "「" + m.currentScope().name + "」をアーカイブしますか？（Asana で元に戻せます）",
		items: []pickerItem{{label: "やめる"}, {label: "アーカイブする", value: project}}}
	return nil
}

func (m *Model) archive(project string) tea.Cmd {
	syncer, ok := m.online()
	if !ok || project == "" {
		return nil
	}
	name := m.currentScope().name
	return func() tea.Msg {
		if err := syncer.SetProjectArchived(m.ctx, project, true); err != nil {
			return projectDoneMsg{err: err}
		}
		return projectDoneMsg{status: "アーカイブしました: " + name, toMyTasks: true}
	}
}

// --- members (online only, multi-select) ---

type membersMsg struct {
	project string
	users   []readmodel.UserRef
	members map[string]bool
	err     error
}

func (m *Model) openMembers() tea.Cmd {
	project := m.currentProject()
	if project == "" {
		return nil
	}
	if _, ok := m.online(); !ok {
		return nil
	}
	return m.loadMembers(project)
}

func (m *Model) loadMembers(project string) tea.Cmd {
	st := m.deps.Store
	return func() tea.Msg {
		users, err := queryOf(st).ListUsers(m.ctx)
		if err != nil {
			return membersMsg{err: err}
		}
		cur, err := queryOf(st).ProjectMembers(m.ctx, project)
		has := map[string]bool{}
		for _, u := range cur {
			has[u.GID] = true
		}
		return membersMsg{project: project, users: users, members: has, err: err}
	}
}

func (m *Model) showMembers(msg membersMsg) {
	cursor := 0
	if m.picker != nil && m.picker.kind == pickMembers {
		cursor = m.picker.cursor
	}
	p := &picker{kind: pickMembers, project: msg.project, cursor: cursor,
		title: "メンバー（" + m.currentScope().name + "）  enter / space で追加・外す（すぐ Asana に反映）・esc で閉じる"}
	for _, u := range msg.users {
		mark := "[ ]"
		if msg.members[u.GID] {
			mark = "[✓]"
		}
		p.items = append(p.items, pickerItem{label: mark + " " + u.Name, value: u.GID, checked: msg.members[u.GID]})
	}
	p.cursor = clamp(p.cursor, 0, len(p.items)-1)
	m.picker = p
}

func (m *Model) toggleMember(project string, item pickerItem) tea.Cmd {
	syncer, ok := m.online()
	if !ok {
		return nil
	}
	return func() tea.Msg {
		var err error
		if item.checked {
			err = syncer.SetProjectMembers(m.ctx, project, nil, []string{item.value})
		} else {
			err = syncer.SetProjectMembers(m.ctx, project, []string{item.value}, nil)
		}
		if err != nil {
			return membersMsg{err: fmt.Errorf("メンバーを変更できませんでした: %w", err)}
		}
		return m.loadMembers(project)()
	}
}
