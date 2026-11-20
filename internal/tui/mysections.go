package tui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asagao/internal/readmodel"
)

// Moving my tasks between the sections of My Tasks (#35). In My Tasks, m chooses one of
// those sections when they are known; otherwise it moves within a project as before.

// openMySectionPicker opens the chooser of My Tasks sections for t. It reports false when
// there is nothing to choose (the sections are not known, or t is not assigned to me).
func (m *Model) openMySectionPicker(t readmodel.TaskSummary) bool {
	secs, err := m.sections(m.deps.Now()).MySections(m.ctx, "")
	if err != nil || len(secs) == 0 {
		return false
	}
	me, _ := m.deps.App.Query().MeGID(m.ctx)
	if t.Assignee == nil || t.Assignee.GID != me {
		return false
	}
	p := &picker{kind: pickMySection, title: "移動先（マイタスクのセクション）", task: t}
	for i, s := range secs {
		item := pickerItem{label: s.Name, value: s.GID}
		if t.MySection != nil && t.MySection.GID == s.GID {
			item.hint = "現在"
			p.cursor = i
		}
		p.items = append(p.items, item)
	}
	m.picker = p
	return true
}

func (m *Model) moveMine(t readmodel.TaskSummary, section, name string) tea.Cmd {
	sections := m.sections(m.deps.Now())
	return func() tea.Msg {
		if err := sections.MoveMineHere(m.ctx, t.GID, section); err != nil {
			return writeDoneMsg{err: err}
		}
		return writeDoneMsg{status: fmt.Sprintf("移動しました: %s → %s（マイタスク）", t.Name, name), gid: t.GID}
	}
}
