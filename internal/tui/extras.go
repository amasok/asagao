package tui

import (
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/imageview"
	"github.com/amasok/asagao/internal/store"
	"github.com/amasok/asagao/internal/sync"
)

// Likes, followers, extra projects, duplicates and file uploads.

func (m *Model) toggleLike() tea.Cmd {
	t := m.actionTask()
	if t == nil {
		return nil
	}
	liked := !t.Liked
	if liked {
		return m.updateTask(*t, app.TaskUpdate{Liked: &liked}, "いいねしました")
	}
	return m.updateTask(*t, app.TaskUpdate{Liked: &liked}, "いいねを取り消しました")
}

// --- followers (multi-select) ---

type followersMsg struct {
	task  store.TaskSummary
	users []store.UserRef
	has   map[string]bool
	err   error
}

func (m *Model) openFollowers() tea.Cmd {
	t := m.actionTask()
	if t == nil {
		return nil
	}
	return m.openFollowersFor(*t)
}

func (m *Model) openFollowersFor(task store.TaskSummary) tea.Cmd {
	st := m.deps.Store
	return func() tea.Msg {
		users, err := st.ListUsers(m.ctx)
		if err != nil {
			return followersMsg{err: err}
		}
		cur, err := st.TaskFollowers(m.ctx, task.GID)
		has := map[string]bool{}
		for _, u := range cur {
			has[u.GID] = true
		}
		return followersMsg{task: task, users: users, has: has, err: err}
	}
}

func (m *Model) showFollowers(msg followersMsg) {
	cursor := 0
	if m.picker != nil && m.picker.kind == pickFollowers {
		cursor = m.picker.cursor
	}
	p := &picker{kind: pickFollowers, title: "フォロワー（" + truncateRunes(msg.task.Name, 30) + "）  enter / space で切り替え・esc で閉じる", task: msg.task, cursor: cursor}
	for _, u := range msg.users {
		mark := "[ ]"
		if msg.has[u.GID] {
			mark = "[✓]"
		}
		p.items = append(p.items, pickerItem{label: mark + " " + u.Name, value: u.GID, checked: msg.has[u.GID]})
	}
	p.cursor = clamp(p.cursor, 0, len(p.items)-1)
	m.picker = p
}

type relationToggledMsg struct {
	task store.TaskSummary
	kind pickerKind
}

func (m *Model) toggleFollower(t store.TaskSummary, item pickerItem) tea.Cmd {
	st, now := m.deps.Store, m.deps.Now()
	return func() tea.Msg {
		if err := st.SetFollowerLocal(m.ctx, t.GID, item.value, !item.checked, now); err != nil {
			return writeDoneMsg{err: err}
		}
		return relationToggledMsg{task: t, kind: pickFollowers}
	}
}

// --- projects (multi-select) ---

func (m *Model) openProjects() tea.Cmd {
	t := m.actionTask()
	if t == nil {
		return nil
	}
	m.showProjects(*t)
	return nil
}

func (m *Model) showProjects(task store.TaskSummary) {
	cursor := 0
	if m.picker != nil && m.picker.kind == pickProjects {
		cursor = m.picker.cursor
	}
	in := map[string]bool{}
	if m.detail != nil && m.detail.GID == task.GID {
		task = m.detail.TaskSummary // freshest memberships
	}
	for _, ms := range task.Memberships {
		in[ms.ProjectGID] = true
	}
	p := &picker{kind: pickProjects, title: "プロジェクト（" + truncateRunes(task.Name, 30) + "）  enter / space で追加・外す・esc で閉じる", task: task, cursor: cursor}
	for _, sc := range m.scopes {
		if sc.kind != scopeProject {
			continue
		}
		mark := "[ ]"
		if in[sc.project] {
			mark = "[✓]"
		}
		p.items = append(p.items, pickerItem{label: mark + " " + sc.name, value: sc.project, checked: in[sc.project]})
	}
	if len(p.items) == 0 {
		m.status = "プロジェクトがありません"
		return
	}
	p.cursor = clamp(p.cursor, 0, len(p.items)-1)
	m.picker = p
}

func (m *Model) toggleProject(t store.TaskSummary, item pickerItem) tea.Cmd {
	st, now := m.deps.Store, m.deps.Now()
	return func() tea.Msg {
		if err := st.SetProjectLocal(m.ctx, t.GID, item.value, "", !item.checked, now); err != nil {
			return writeDoneMsg{err: err}
		}
		d, err := st.GetTask(m.ctx, t.GID)
		if err == nil {
			t = d.TaskSummary
		}
		return relationToggledMsg{task: t, kind: pickProjects}
	}
}

// --- duplicate ---

func (m *Model) startDuplicate() tea.Cmd {
	t := m.actionTask()
	if t == nil {
		return nil
	}
	m.openPrompt(promptDuplicate)
	m.dupSource = t.GID
	m.prompt.SetValue(t.Name + " のコピー")
	m.prompt.CursorEnd()
	return m.prompt.Focus()
}

func (m *Model) duplicate(name string) tea.Cmd {
	st, now, src := m.deps.Store, m.deps.Now(), m.dupSource
	return func() tea.Msg {
		gid, err := st.DuplicateTaskLocal(m.ctx, src, name, now)
		if err != nil {
			return writeDoneMsg{err: err}
		}
		return writeDoneMsg{status: "複製しました（サブタスク・添付・フォロワーは送信後に Asana 側でコピーされます）: " + name, gid: gid}
	}
}

// --- attachments ---

func (m *Model) startAttach() tea.Cmd {
	t := m.actionTask()
	if t == nil {
		return nil
	}
	m.openPrompt(promptAttach)
	m.attachTask = t.GID
	return m.prompt.Focus()
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

func (m *Model) attach(path string) tea.Cmd {
	st, now, task, dir := m.deps.Store, m.deps.Now(), m.attachTask, m.deps.CacheDir
	path = expandHome(strings.Trim(path, `"'`))
	return func() tea.Msg {
		if _, err := st.UploadAttachmentLocal(m.ctx, task, path, dir, now); err != nil {
			return writeDoneMsg{err: err}
		}
		return writeDoneMsg{status: "ファイルを添付しました: " + filepath.Base(path)}
	}
}

// openAttachment shows an image, or opens other files in the OS (downloading first).
func (m *Model) openAttachment(a store.AttachmentView) tea.Cmd {
	if a.IsImage {
		return m.viewImages()
	}
	syncer := m.syncer
	if syncer == nil {
		syncer = &sync.Syncer{Store: m.deps.Store}
	}
	taskGID := m.detail.GID
	return func() tea.Msg {
		path, err := syncer.DownloadAttachment(m.ctx, taskGID, a.GID)
		if err != nil {
			return statusMsg("開けませんでした: " + err.Error())
		}
		revealed, err := imageview.OpenFiles(path)
		if err != nil {
			return statusMsg("開けませんでした: " + err.Error())
		}
		if len(revealed) > 0 {
			return statusMsg("プログラムとして動く種類のファイルなので、開かずにフォルダで表示しました: " + a.Name)
		}
		return statusMsg("開きました: " + a.Name)
	}
}
