package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/domain/tag"
	"github.com/amasok/asagao/internal/readmodel"
)

// Creating, deleting and reordering sections, and renaming, recoloring and deleting tags
// (#33). They are done on the section the cursor is on (a board column, a list group).
// Deleting cannot be undone in Asana, so it asks first. A feature Asana refused with a 402
// (the workspace's plan lacks it) is not offered (planOff, refreshed with the outbox).

// planLoadedMsg says which features the workspace's plan lacks.
type planLoadedMsg struct{ off map[string]bool }

func (m *Model) loadPlan() tea.Cmd {
	st := m.deps.Store
	return func() tea.Msg {
		off := map[string]bool{}
		if !(&app.Sections{Store: st}).Available(m.ctx) {
			off[readmodel.PlanSections] = true
		}
		if !(&app.Tags{Store: st}).Available(m.ctx) {
			off[readmodel.PlanTags] = true
		}
		if !(&app.TimeTracking{Store: st, Clock: m.deps.Now}).Available(m.ctx) {
			off[readmodel.PlanTimeTracking] = true
		}
		goals := &app.Goals{Store: st, Clock: m.deps.Now}
		if !goals.Available(m.ctx) {
			off[readmodel.PlanGoals] = true
		}
		if !goals.PortfoliosAvailable(m.ctx) {
			off[readmodel.PlanPortfolios] = true
		}
		// Project templates are listed and made in the default workspace. (Task templates
		// are hidden per project: templates.go.)
		ws, _ := st.DefaultWorkspace(m.ctx)
		if !app.TemplatesOffered(m.ctx, st, readmodel.PlanProjectTemplates, ws, m.deps.Now()) {
			off[readmodel.PlanProjectTemplates] = true
		}
		return planLoadedMsg{off: off}
	}
}

// planOK reports whether feature is offered (see planLoadedMsg).
func (m *Model) planOK(feature string) bool { return !m.planOff[feature] }

func (m *Model) tagUseCases(now time.Time) *app.Tags {
	return &app.Tags{Store: m.deps.Store, Clock: func() time.Time { return now }}
}

// updateSectionTags handles the messages of this file.
func (m *Model) updateSectionTags(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case planLoadedMsg:
		m.planOff = msg.off
		return nil, true
	case tagsManageMsg:
		if msg.err != nil {
			m.status = "タグを読み込めませんでした: " + msg.err.Error()
			return nil, true
		}
		m.showTagManager(msg.tags)
		return nil, true
	}
	return nil, false
}

// --- sections ---

// sectionHere returns the project open and the section the cursor is on in it ("" when
// none: the list is empty, or the cursor is in その他).
func (m *Model) sectionHere() (project, section, name string) {
	project = m.currentProject()
	if project == "" {
		return "", "", ""
	}
	section, name = m.sectionUnderCursor()
	if !slices.ContainsFunc(m.currentScope().sections, func(s readmodel.SectionView) bool { return s.GID == section }) {
		return project, "", ""
	}
	return project, section, name
}

func (m *Model) startNewSection() tea.Cmd {
	if m.currentProject() == "" {
		m.status = "セクションはプロジェクトを開いて追加してください"
		return nil
	}
	_, section, name := m.sectionHere()
	m.openPrompt(promptNewSection)
	m.newSectionAfter = section
	if section != "" {
		m.prompt.Placeholder = "「" + name + "」の後に追加（Enter で追加 / Esc で取り消し）"
	}
	return m.prompt.Focus()
}

func (m *Model) createSection(name string) tea.Cmd {
	project, after := m.currentProject(), m.newSectionAfter
	if project == "" {
		return nil
	}
	sections := m.sections(m.deps.Now())
	return func() tea.Msg {
		if _, err := sections.CreateHere(m.ctx, project, name, "", after); err != nil {
			return writeDoneMsg{err: err}
		}
		return writeDoneMsg{status: "セクションを追加しました: " + name}
	}
}

// moveSection moves the section under the cursor one place up (by -1) or down (by +1).
func (m *Model) moveSection(by int) tea.Cmd {
	_, section, name := m.sectionHere()
	if section == "" {
		m.status = "動かすセクションが決まりません（プロジェクトのリストかボードで、セクションの上で使ってください）"
		return nil
	}
	secs := m.currentScope().sections
	i := slices.IndexFunc(secs, func(s readmodel.SectionView) bool { return s.GID == section })
	j := i + by
	if j < 0 || j >= len(secs) {
		m.status = "これ以上は動かせません: " + name
		return nil
	}
	var before, after string
	if by < 0 {
		before = secs[j].GID
	} else {
		after = secs[j].GID
	}
	sections := m.sections(m.deps.Now())
	if m.boardActive() {
		m.boardCol = j // keep the moved column selected
	}
	return func() tea.Msg {
		if err := sections.ReorderHere(m.ctx, section, before, after); err != nil {
			return writeDoneMsg{err: err}
		}
		return writeDoneMsg{status: "セクションを動かしました: " + name}
	}
}

func (m *Model) confirmDeleteSection() tea.Cmd {
	_, section, name := m.sectionHere()
	if section == "" {
		m.status = "削除するセクションが決まりません（プロジェクトのリストかボードで、セクションの上で使ってください）"
		return nil
	}
	m.picker = &picker{kind: pickDeleteSection, title: "セクション「" + name + "」を削除しますか？（空のセクションだけ。Asana でも元に戻せません）",
		items: []pickerItem{{label: "やめる"}, {label: "削除する", value: section, hint: name}}}
	return nil
}

func (m *Model) deleteSection(section, name string) tea.Cmd {
	sections := m.sections(m.deps.Now())
	return func() tea.Msg {
		if err := sections.DeleteHere(m.ctx, section); err != nil {
			return writeDoneMsg{err: err}
		}
		return writeDoneMsg{status: "セクションを削除しました: " + name}
	}
}

// --- tags ---

type tagsManageMsg struct {
	tags []readmodel.TagCount
	err  error
}

func (m *Model) openTagManager() tea.Cmd {
	st := m.deps.Store
	return func() tea.Msg {
		tags, err := queryOf(st).ListTags(m.ctx)
		return tagsManageMsg{tags: tags, err: err}
	}
}

func (m *Model) showTagManager(tags []readmodel.TagCount) {
	if len(tags) == 0 {
		m.status = "タグがありません"
		return
	}
	items := make([]pickerItem, 0, len(tags))
	for _, t := range tags {
		hint := fmt.Sprintf("%d 件", t.Total)
		if t.Color != "" {
			hint += "  " + t.Color
		}
		items = append(items, pickerItem{label: "#" + t.Name, value: t.GID, hint: hint})
	}
	m.tagList = tags
	m.picker = &picker{kind: pickTagManage, title: "どのタグを変えますか", items: items}
}

// tagByGID returns the tag gid from the list shown by showTagManager.
func (m *Model) tagByGID(gid string) (readmodel.TagCount, bool) {
	i := slices.IndexFunc(m.tagList, func(t readmodel.TagCount) bool { return t.GID == gid })
	if i < 0 {
		return readmodel.TagCount{}, false
	}
	return m.tagList[i], true
}

func (m *Model) openTagActions(t readmodel.TagCount) {
	m.tagTarget = t
	m.picker = &picker{kind: pickTagAction, title: "#" + t.Name, items: []pickerItem{
		{label: "名前を変える", value: "rename"},
		{label: "色を変える", value: "color"},
		{label: "削除する", value: "delete", hint: fmt.Sprintf("キャッシュにあるタスク %d 件から外れます", t.Total)},
	}}
}

func (m *Model) openTagColors(t readmodel.TagCount) {
	items := []pickerItem{{label: "色なし", value: "none"}}
	for _, c := range tag.Colors {
		item := pickerItem{label: c, value: c}
		if c == t.Color {
			item.hint = "いまの色"
		}
		items = append(items, item)
	}
	m.picker = &picker{kind: pickTagColor, title: "#" + t.Name + " の色", items: items}
}

func (m *Model) updateTag(t readmodel.TagCount, changes map[string]any, done string) tea.Cmd {
	tags := m.tagUseCases(m.deps.Now())
	return func() tea.Msg {
		if err := tags.UpdateHere(m.ctx, t.GID, changes); err != nil {
			return writeDoneMsg{err: err}
		}
		return writeDoneMsg{status: done}
	}
}

func (m *Model) deleteTag(t readmodel.TagCount) tea.Cmd {
	tags := m.tagUseCases(m.deps.Now())
	return func() tea.Msg {
		if err := tags.DeleteHere(m.ctx, t.GID); err != nil {
			return writeDoneMsg{err: err}
		}
		return writeDoneMsg{status: fmt.Sprintf("タグを削除しました: #%s（キャッシュにあるタスク %d 件から外しました）", t.Name, t.Total)}
	}
}

// handleSectionTagPicker handles a choice in the pickers of this file.
func (m *Model) handleSectionTagPicker(p *picker, item pickerItem) (tea.Cmd, bool) {
	switch p.kind {
	case pickDeleteSection:
		if item.value == "" {
			return nil, true // やめる
		}
		return m.deleteSection(item.value, item.hint), true
	case pickTagManage:
		if t, ok := m.tagByGID(item.value); ok {
			m.openTagActions(t)
		}
		return nil, true
	case pickTagAction:
		t := m.tagTarget
		switch item.value {
		case "rename":
			m.openPrompt(promptRenameTag)
			m.prompt.SetValue(t.Name)
			m.prompt.CursorEnd()
			return m.prompt.Focus(), true
		case "color":
			m.openTagColors(t)
		case "delete":
			m.picker = &picker{kind: pickDeleteTag,
				title: fmt.Sprintf("タグ「%s」を削除しますか？（キャッシュにあるタスク %d 件から外れます。Asana でも元に戻せません）", t.Name, t.Total),
				items: []pickerItem{{label: "やめる"}, {label: "削除する", value: t.GID}}}
		}
		return nil, true
	case pickTagColor:
		label := item.value
		if label == "none" {
			label = "色なし"
		}
		return m.updateTag(m.tagTarget, map[string]any{"color": item.value}, "タグの色を変えました: #"+m.tagTarget.Name+" → "+label), true
	case pickDeleteTag:
		if item.value == "" || item.value != m.tagTarget.GID {
			return nil, true // やめる
		}
		if m.tagFilter == m.tagTarget.GID {
			m.tagFilter = "" // the list filtered by it would be empty
		}
		return m.deleteTag(m.tagTarget), true
	}
	return nil, false
}

// submitTagName renames the tag chosen in the tag manager.
func (m *Model) submitTagName(name string) tea.Cmd {
	name = strings.TrimSpace(name)
	if name == m.tagTarget.Name {
		return nil
	}
	return m.updateTag(m.tagTarget, map[string]any{"name": name}, "タグの名前を変えました: #"+name)
}
