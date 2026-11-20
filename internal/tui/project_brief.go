package tui

import (
	"errors"
	"fmt"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/richtext"
)

// The project's brief (#60), from the overview: b writes it in the editor (a new one when
// the project has none), B deletes it after a confirmation. Both are queued like any other
// change (offline too); a brief changed in Asana meanwhile is a conflict in the outbox.

// briefDoc is a brief as the editor shows it.
type briefDoc struct {
	Title, Body string
}

const briefHelp = `# asagao: プロジェクトブリーフ（概要のページ）です。保存してエディタを閉じると変更します（オフラインでも積み、同期で送ります）。
# title: ブリーフのタイトル
# 下の --- のあとが本文（マークダウン。見出し・罫線・画像も使えます。| a | b | の表は Asana の表にします）です。
# ` + "```" + `asana-html のブロックは、表や埋め込み（Asana の HTML のまま）です。表の文字はその中で直せます。ブロックを消すと、表や埋め込みも消えます。`

func (d briefDoc) String() string {
	return "---\n" + briefHelp + "\ntitle: " + d.Title + "\n---\n\n" + d.Body + "\n"
}

func parseBriefDoc(s string) (briefDoc, error) {
	var d briefDoc
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
	for _, l := range lines[1:end] {
		if strings.HasPrefix(strings.TrimSpace(l), "#") || strings.TrimSpace(l) == "" {
			continue
		}
		key, value, ok := strings.Cut(l, ":")
		if !ok {
			return d, fmt.Errorf("読めない行があります: %q", l)
		}
		if strings.TrimSpace(key) != "title" {
			return d, fmt.Errorf("知らない項目です: %q", strings.TrimSpace(key))
		}
		d.Title = strings.TrimSpace(value)
	}
	d.Body = strings.TrimSpace(strings.Join(lines[end+1:], "\n"))
	return d, nil
}

// input is what changed from original, as the use case's input (nil: unchanged).
func (d briefDoc) input(original briefDoc) app.BriefInput {
	var in app.BriefInput
	if d.Title != original.Title {
		in.Title = &d.Title
	}
	if d.Body != strings.TrimSpace(original.Body) {
		in.Markdown = &d.Body
	}
	return in
}

type (
	briefEditReadyMsg struct {
		project, path string
		original      briefDoc
		isNew         bool
		err           error
	}
	briefEditDoneMsg struct {
		project, path string
		original      briefDoc
		isNew         bool
		err           error
	}
)

func (m *Model) briefs() *app.Briefs {
	return &app.Briefs{Store: m.deps.Store, Text: richtext.Converter{}, Clock: m.deps.Now}
}

// editBrief opens the current project's brief in the editor (an empty one if it has none).
func (m *Model) editBrief() tea.Cmd {
	project := m.currentProject()
	if project == "" || m.deps.RunEditor == nil {
		return nil
	}
	briefs := m.briefs()
	st := m.deps.Store
	return func() tea.Msg {
		if err := briefs.Available(m.ctx, project); err != nil {
			return briefEditReadyMsg{err: err}
		}
		o, err := queryOf(st).ProjectOverview(m.ctx, project)
		if err != nil {
			return briefEditReadyMsg{err: err}
		}
		var doc briefDoc
		if o.Brief != nil {
			doc.Title = o.Brief.Title
			if o.Brief.HTMLText != "" {
				doc.Body = richtext.BriefToEditableMarkdown(o.Brief.HTMLText, nil)
			} else {
				doc.Body = editableBody("", o.Brief.Text, nil)
			}
		}
		path, err := writeTemp("asagao-brief-*.md", doc.String())
		return briefEditReadyMsg{project: project, path: path, original: doc, isNew: o.Brief == nil, err: err}
	}
}

func (m *Model) applyBriefEdit(msg briefEditDoneMsg) tea.Cmd {
	if msg.err != nil {
		m.status = "エディタがエラーで終了しました: " + msg.err.Error()
		return nil
	}
	data, err := os.ReadFile(msg.path)
	if err != nil {
		m.status = err.Error()
		return nil
	}
	doc, err := parseBriefDoc(string(data))
	if err != nil {
		m.status = "読めませんでした（" + err.Error() + "）。内容は " + msg.path + " に残っています"
		return nil
	}
	in := doc.input(msg.original)
	if in == (app.BriefInput{}) || msg.isNew && doc.Title == "" && doc.Body == "" {
		os.Remove(msg.path)
		m.status = "変更はありません"
		return nil
	}
	briefs, path, gid := m.briefs(), msg.path, msg.project
	return func() tea.Msg {
		changed, err := briefs.SaveHere(m.ctx, gid, in)
		if err != nil {
			var fe *app.FieldError
			if errors.As(err, &fe) {
				err = fe.Err
			}
			return projectEditedMsg{project: gid, err: fmt.Errorf("%w（内容は %s に残っています）", err, path)}
		}
		os.Remove(path)
		if !changed {
			return projectEditedMsg{project: gid, status: "変更はありません"}
		}
		return projectEditedMsg{project: gid, status: "ブリーフを変更しました"}
	}
}

// confirmDeleteBrief asks before deleting the current project's brief.
func (m *Model) confirmDeleteBrief() tea.Cmd {
	o := m.overviewView.data
	if o == nil || o.Brief == nil {
		m.status = "このプロジェクトにはブリーフがありません"
		return nil
	}
	m.picker = &picker{kind: pickConfirmDel, title: "ブリーフを削除しますか？（Asana に送ったあとは取り消せません）",
		items: []pickerItem{{label: "やめる"}, {label: "削除する", value: "delete-brief\x00" + o.GID}}}
	return nil
}

// deleteBrief queues the delete of project gid's brief.
func (m *Model) deleteBrief(gid string) tea.Cmd {
	briefs := m.briefs()
	return func() tea.Msg {
		if err := briefs.DeleteHere(m.ctx, gid); err != nil {
			return projectEditedMsg{project: gid, err: err}
		}
		return projectEditedMsg{project: gid, status: "ブリーフを削除しました"}
	}
}

// updateBrief handles the messages of this file.
func (m *Model) updateBrief(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case briefEditReadyMsg:
		if msg.err != nil {
			m.status = msg.err.Error()
			return nil, true
		}
		return m.deps.RunEditor(msg.path, func(err error) tea.Msg {
			return briefEditDoneMsg{project: msg.project, path: msg.path, original: msg.original, isNew: msg.isNew, err: err}
		}), true
	case briefEditDoneMsg:
		return m.applyBriefEdit(msg), true
	}
	return nil, false
}
