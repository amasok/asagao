package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/domain/rich"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/richtext"
)

// Editing in $EDITOR: e writes the task as a Markdown file with front matter, opens the
// editor (the TUI is suspended), and applies whatever changed when the editor exits.
// C does the same for a new comment. The .md extension lets editor plugins (e.g. Markdown
// preview in Neovim) work as usual.

// taskDoc is the editable form of a task.
type taskDoc struct {
	Name       string
	Due        string
	Assignee   string // "me", a user name, or "" (unassigned)
	Completed  bool
	Section    string // only when the task is in exactly one project
	HasSection bool
	Body       string // editable Markdown
}

const editHelp = `# asagao: 保存してエディタを閉じると Asana に反映します（変えた項目だけ）。
# due: YYYY-MM-DD / today / tomorrow / +3d、空欄で期日なし
# assignee: me / メンバーの名前、空欄で未割り当て
# completed: true / false
# 本文はマークダウンで書けます。画像は ![画像](asana:ID) の行を残してください。`

func (d taskDoc) String() string {
	var b strings.Builder
	b.WriteString("---\n" + editHelp + "\n")
	fmt.Fprintf(&b, "name: %s\n", d.Name)
	fmt.Fprintf(&b, "due: %s\n", d.Due)
	fmt.Fprintf(&b, "assignee: %s\n", d.Assignee)
	fmt.Fprintf(&b, "completed: %t\n", d.Completed)
	if d.HasSection {
		fmt.Fprintf(&b, "section: %s\n", d.Section)
	}
	b.WriteString("---\n\n")
	b.WriteString(d.Body)
	if d.Body != "" {
		b.WriteString("\n")
	}
	return b.String()
}

// parseTaskDoc reads an edited file back.
func parseTaskDoc(s string) (taskDoc, error) {
	var d taskDoc
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return d, errors.New("先頭の --- が見つかりません")
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return d, errors.New("項目の終わりの --- が見つかりません")
	}
	seen := map[string]bool{}
	for _, l := range lines[1:end] {
		if strings.HasPrefix(strings.TrimSpace(l), "#") || strings.TrimSpace(l) == "" {
			continue
		}
		key, value, ok := strings.Cut(l, ":")
		if !ok {
			return d, fmt.Errorf("読めない行があります: %q", l)
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		seen[key] = true
		switch key {
		case "name":
			d.Name = value
		case "due":
			d.Due = value
		case "assignee":
			d.Assignee = value
		case "completed":
			switch strings.ToLower(value) {
			case "true", "yes", "x", "完了":
				d.Completed = true
			case "false", "no", "", "未完了":
			default:
				return d, fmt.Errorf("completed は true か false で書いてください: %q", value)
			}
		case "section":
			d.Section, d.HasSection = value, true
		default:
			return d, fmt.Errorf("知らない項目です: %q", key)
		}
	}
	if !seen["name"] || d.Name == "" {
		return d, errors.New("name（タスク名）が空です")
	}
	d.Body = strings.TrimSpace(strings.Join(lines[end+1:], "\n"))
	return d, nil
}

// editableBody returns the task description as editable Markdown. imagePath gives the
// local file of an image (so editor plugins can display it), or "" to use asana:<gid>.
func editableBody(htmlNotes, plain string, imagePath func(gid string) string) string {
	if htmlNotes != "" {
		return richtext.ToEditableMarkdownWith(htmlNotes, imagePath)
	}
	md := richtext.FromPlainEditable(rich.ReplaceInlineAssets(plain, func(gid string) string {
		return "\n\n" + richtext.ImageToken(gid) + "\n\n"
	}))
	return richtext.ImageTokenRe.ReplaceAllStringFunc(md, func(tok string) string {
		gid := richtext.ImageTokenRe.FindStringSubmatch(tok)[1]
		return "![画像](" + richtext.EditableImageDest(gid, imagePath) + ")"
	})
}

func docFor(ctx context.Context, st *app.UseCases, d *readmodel.TaskDetail, imagePath func(string) string) taskDoc {
	doc := taskDoc{Name: d.Name, Due: d.DueOn, Completed: d.Completed, Body: editableBody(d.HTMLNotes, d.Notes, imagePath)}
	if d.Assignee != nil {
		doc.Assignee = d.Assignee.Name
		if me, _ := st.Query().MeGID(ctx); me == d.Assignee.GID {
			doc.Assignee = "me"
		}
	}
	if len(d.Memberships) == 1 {
		doc.HasSection = true
		doc.Section = d.Memberships[0].SectionName
	}
	return doc
}

type (
	editReadyMsg struct {
		gid, path string
		original  taskDoc
		err       error
	}
	editDoneMsg struct {
		gid, path string
		original  taskDoc
		err       error
	}
	// commentReadyMsg / commentDoneMsg: a new comment on task gid, or with story set, my
	// comment story rewritten (original: its Markdown before).
	commentReadyMsg struct {
		gid, path       string
		story, original string
		err             error
	}
	commentDoneMsg struct {
		gid, path       string
		story, original string
		err             error
	}
)

func (m *Model) editTask(gid string) tea.Cmd {
	if m.deps.RunEditor == nil {
		m.status = "エディタを起動できません"
		return nil
	}
	st := m.deps.App
	syncer := m.syncer
	if syncer == nil {
		syncer = m.deps.LocalSyncer() // offline: cached images only
	}
	return func() tea.Msg {
		d, err := st.Query().GetTask(m.ctx, gid)
		if err != nil {
			return editReadyMsg{err: err}
		}
		// Download the images first so the file can point at them (editor image plugins
		// such as snacks.nvim need a real path).
		paths := map[string]string{}
		for _, img := range taskImages(d) {
			if p, err := syncer.DownloadAttachment(m.ctx, gid, img); err == nil {
				paths[img] = p
			}
		}
		doc := docFor(m.ctx, st, d, func(img string) string { return paths[img] })
		path, err := writeTemp("asagao-task-*.md", doc.String())
		return editReadyMsg{gid: gid, path: path, original: doc, err: err}
	}
}

func (m *Model) editComment(gid string) tea.Cmd {
	if m.deps.RunEditor == nil {
		m.status = "エディタを起動できません"
		return nil
	}
	return func() tea.Msg {
		path, err := writeTemp("asagao-comment-*.md", commentTemplate)
		return commentReadyMsg{gid: gid, path: path, err: err}
	}
}

const commentTemplate = "<!-- asagao: コメントをマークダウンで書いて保存してください。空のまま閉じると取り消します。 -->\n\n"

func writeTemp(pattern, content string) (string, error) {
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", err
	}
	defer f.Close()
	_, err = f.WriteString(content)
	return f.Name(), err
}

func (m *Model) updateEdit(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case editReadyMsg:
		if msg.err != nil {
			m.status = "編集を開始できませんでした: " + msg.err.Error()
			return nil, true
		}
		return m.deps.RunEditor(msg.path, func(err error) tea.Msg {
			return editDoneMsg{gid: msg.gid, path: msg.path, original: msg.original, err: err}
		}), true
	case editDoneMsg:
		return m.applyEdit(msg), true
	case commentReadyMsg:
		if msg.err != nil {
			m.status = "編集を開始できませんでした: " + msg.err.Error()
			return nil, true
		}
		return m.deps.RunEditor(msg.path, func(err error) tea.Msg {
			return commentDoneMsg{gid: msg.gid, path: msg.path, story: msg.story, original: msg.original, err: err}
		}), true
	case commentDoneMsg:
		return m.applyComment(msg), true
	}
	return nil, false
}

// applyEdit reads the edited file and queues the changes.
func (m *Model) applyEdit(msg editDoneMsg) tea.Cmd {
	if msg.err != nil {
		m.status = "エディタがエラーで終了しました: " + msg.err.Error()
		return nil
	}
	data, err := os.ReadFile(msg.path)
	if err != nil {
		m.status = err.Error()
		return nil
	}
	doc, err := parseTaskDoc(string(data))
	if err != nil {
		m.status = "編集内容を読めませんでした（" + err.Error() + "）。内容は " + msg.path + " に残っています"
		return nil
	}
	os.Remove(msg.path)
	orig := msg.original
	now, gid := m.deps.Now(), msg.gid
	tasks, sections := m.tasks(now), m.sections(now)
	return func() tea.Msg {
		ctx := m.ctx
		var in app.UpdateTaskInput
		if doc.Name != orig.Name {
			in.Name = &doc.Name
		}
		if doc.Due != orig.Due {
			in.Due = &doc.Due
		}
		if doc.Assignee != orig.Assignee {
			in.Assignee = &doc.Assignee
		}
		if doc.Completed != orig.Completed {
			in.Completed = &doc.Completed
		}
		if strings.TrimSpace(doc.Body) != strings.TrimSpace(orig.Body) {
			in.Notes = &doc.Body
		}
		// A *app.FieldError reads "due: …" and "assignee: …", as the screen says it.
		u, err := tasks.ResolveUpdate(ctx, in)
		if err != nil {
			return writeDoneMsg{err: err}
		}
		moved := false
		if doc.HasSection && orig.HasSection && doc.Section != orig.Section {
			project, section, err := sections.ResolveMove(ctx, gid, "", doc.Section)
			if err != nil {
				return writeDoneMsg{err: fmt.Errorf("section: %w", err)}
			}
			if err := sections.MoveHere(ctx, gid, project, section); err != nil {
				return writeDoneMsg{err: err}
			}
			moved = true
		}
		if err := tasks.UpdateHere(ctx, gid, u); err != nil {
			return writeDoneMsg{err: err}
		}
		if u.Count() == 0 && !moved {
			return statusMsg("変更はありません")
		}
		return writeDoneMsg{status: fmt.Sprintf("タスクを更新しました（%d 項目）", u.Count()+btoi(moved))}
	}
}

func (m *Model) applyComment(msg commentDoneMsg) tea.Cmd {
	if msg.err != nil {
		m.status = "エディタがエラーで終了しました: " + msg.err.Error()
		return nil
	}
	data, err := os.ReadFile(msg.path)
	if err != nil {
		m.status = err.Error()
		return nil
	}
	os.Remove(msg.path)
	text := strings.TrimSpace(strings.Replace(string(data), strings.TrimSpace(commentTemplate), "", 1))
	if msg.story != "" {
		return m.applyCommentEdit(msg, text)
	}
	if text == "" {
		m.status = "コメントを取り消しました"
		return nil
	}
	comments, gid := m.comments(m.deps.Now()), msg.gid
	return func() tea.Msg {
		if _, err := comments.Add(m.ctx, gid, text); err != nil {
			return writeDoneMsg{err: err}
		}
		return writeDoneMsg{status: "コメントしました"}
	}
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}
