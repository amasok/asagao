package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/richtext"
)

// Rewriting and deleting my own comments, and deleting attachments (#34). enter on one of
// my comments or on an attachment opens a short list of actions; a delete, which cannot
// be undone once sent, is confirmed first ("やめる" is the default).

// isMyComment reports whether c was written by the logged-in user.
func (m *Model) isMyComment(c readmodel.StoryView) bool {
	return c.Type == "comment" && c.CreatedBy != nil && m.detailMe != "" && c.CreatedBy.GID == m.detailMe
}

func storyIndex(stories []readmodel.StoryView, gid string) int {
	for i, s := range stories {
		if s.GID == gid {
			return i
		}
	}
	return -1
}

func (m *Model) openCommentActions(c readmodel.StoryView) tea.Cmd {
	if !m.isMyComment(c) {
		return nil
	}
	m.picker = &picker{kind: pickItemAction, title: "コメント「" + truncateRunes(strings.Join(strings.Fields(c.Text), " "), 30) + "」",
		items: []pickerItem{
			{label: "書き直す（エディタ）", value: "edit\x00" + c.GID},
			{label: "削除する…", value: "delete-comment\x00" + c.GID},
		}}
	return nil
}

func (m *Model) openAttachmentActions(a readmodel.AttachmentView) tea.Cmd {
	m.picker = &picker{kind: pickItemAction, title: "添付「" + truncateRunes(a.Name, 40) + "」",
		items: []pickerItem{
			{label: "開く", value: "open\x00" + a.GID},
			{label: "削除する…", value: "delete-attachment\x00" + a.GID},
		}}
	return nil
}

// handleItemPicker runs the choice made in pickItemAction or pickConfirmDel.
func (m *Model) handleItemPicker(p *picker, item pickerItem) (tea.Cmd, bool) {
	switch p.kind {
	case pickItemAction:
		action, gid, _ := strings.Cut(item.value, "\x00")
		d := m.detail
		if d == nil {
			return nil, true
		}
		switch action {
		case "open":
			for _, a := range d.Attachments {
				if a.GID == gid {
					return m.openAttachment(a), true
				}
			}
		case "edit":
			if i := storyIndex(d.Stories, gid); i >= 0 {
				return m.rewriteComment(d.Stories[i]), true
			}
		case "delete-comment", "delete-attachment":
			what := "このコメント"
			if action == "delete-attachment" {
				what = "この添付"
			}
			m.picker = &picker{kind: pickConfirmDel, title: what + "を削除しますか？（Asana に送ったあとは取り消せません）",
				items: []pickerItem{{label: "やめる"}, {label: "削除する", value: item.value}}}
		}
		return nil, true
	case pickConfirmDel:
		action, gid, _ := strings.Cut(item.value, "\x00")
		switch action {
		case "delete-comment":
			comments := m.comments(m.deps.Now())
			return func() tea.Msg {
				if err := comments.Delete(m.ctx, gid); err != nil {
					return writeDoneMsg{err: err}
				}
				return writeDoneMsg{status: "コメントを削除しました"}
			}, true
		case "delete-attachment":
			copies := m.copies(m.deps.Now())
			return func() tea.Msg {
				if err := copies.Detach(m.ctx, gid); err != nil {
					return writeDoneMsg{err: err}
				}
				return writeDoneMsg{status: "添付を削除しました"}
			}, true
		}
		return nil, true // やめる
	}
	return nil, false
}

// rewriteComment opens my comment c in the editor, as Markdown.
func (m *Model) rewriteComment(c readmodel.StoryView) tea.Cmd {
	if m.deps.RunEditor == nil {
		m.status = "エディタを起動できません"
		return nil
	}
	md := strings.TrimSpace(c.Text)
	if c.HTMLText != "" {
		md = strings.TrimSpace(richtext.ToEditableMarkdown(c.HTMLText))
	}
	return func() tea.Msg {
		path, err := writeTemp("asagao-comment-*.md", commentTemplate+md+"\n")
		return commentReadyMsg{gid: m.detail.GID, story: c.GID, original: md, path: path, err: err}
	}
}

// applyCommentEdit queues the rewritten comment (msg.story) read from the editor's file.
func (m *Model) applyCommentEdit(msg commentDoneMsg, text string) tea.Cmd {
	if text == "" {
		m.status = "書き直しを取り消しました（削除は enter →「削除する」）"
		return nil
	}
	if text == msg.original {
		m.status = "コメントは変わっていません"
		return nil
	}
	comments, story := m.comments(m.deps.Now()), msg.story
	return func() tea.Msg {
		if err := comments.Edit(m.ctx, story, text); err != nil {
			return writeDoneMsg{err: err}
		}
		return writeDoneMsg{status: "コメントを書き直しました"}
	}
}
