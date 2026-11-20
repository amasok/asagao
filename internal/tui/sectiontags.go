package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/domain/plan"
	"github.com/amasok/asagao/internal/domain/tag"
	"github.com/amasok/asagao/internal/readmodel"
)

// Creating, deleting and reordering sections, and renaming, recoloring and deleting tags
// (#33). They are done on the section the cursor is on (a board column, a list group).
// Deleting cannot be undone in Asana, so it asks first. A feature Asana refused with a 402
// (the workspace's plan lacks it) is not offered while its mark holds (planOff, refreshed
// with the outbox and when opening the menu, and checked against Now so marks expire after
// plan.RecheckAfter even without any write, #68).

// planWindow is the time interval [from, until) during which a 402 hides a feature
// (plan.OffAt: age >= 0 && age < plan.RecheckAfter). When several workspaces
// have 402s, the feature is hidden only while every workspace's 402 holds: from the latest
// refusal until the earliest one expires.
type planWindow struct {
	from, until time.Time
}

func (w planWindow) off(now time.Time) bool {
	return !w.until.IsZero() && !now.Before(w.from) && now.Before(w.until)
}

// planOffIn returns the window during which a recent 402 hides feature in workspaceGID at
// now (false when the feature is offered).
func planOffIn(ctx context.Context, st Store, feature, workspaceGID string, now time.Time) (planWindow, bool) {
	at, ok, err := st.PlanRefusedAt(ctx, feature, workspaceGID)
	if err != nil || !ok {
		return planWindow{}, false
	}
	from, until, off := plan.Window(at, now)
	if !off {
		return planWindow{}, false
	}
	return planWindow{from: from, until: until}, true
}

// planOffEverywhere returns the window during which a recent 402 hides feature in every
// workspace at now (false when at least one workspace offers it, or none are known).
func planOffEverywhere(ctx context.Context, st Store, feature string, now time.Time) (planWindow, bool) {
	wss, err := st.WorkspaceGIDs(ctx)
	if err != nil || len(wss) == 0 {
		return planWindow{}, false
	}
	var win planWindow
	for i, ws := range wss {
		w, off := planOffIn(ctx, st, feature, ws, now)
		if !off {
			return planWindow{}, false
		}
		if i == 0 {
			win = w
			continue
		}
		if w.from.After(win.from) {
			win.from = w.from
		}
		if w.until.Before(win.until) {
			win.until = w.until
		}
	}
	return win, win.off(now)
}

// planLoadedMsg says which features the workspace's plan lacks (and until when).
type planLoadedMsg struct {
	seq uint64
	off map[string]planWindow
}

func (m *Model) loadPlan() tea.Cmd {
	st := m.deps.Store
	if st == nil {
		return nil
	}
	m.planSeq++
	seq, ctx, now := m.planSeq, m.ctx, m.now()
	return func() tea.Msg {
		off := map[string]planWindow{}
		// Sections, tags and time tracking: unless a recent 402 hides them in every
		// workspace (each change checks its own workspace).
		for _, f := range []string{plan.Sections, plan.Tags, plan.TimeTracking} {
			if w, hidden := planOffEverywhere(ctx, st, f, now); hidden {
				off[f] = w
			}
		}
		// Goals, portfolios, project templates and custom fields: the default workspace's.
		// (Task templates are hidden per project: templates.go.)
		ws, wsErr := st.DefaultWorkspace(ctx)
		for _, f := range []string{plan.Goals, plan.Portfolios, plan.ProjectTemplates} {
			if w, hidden := planOffIn(ctx, st, f, ws, now); hidden {
				off[f] = w
			}
		}
		if wsErr == nil {
			if w, hidden := planOffIn(ctx, st, plan.CustomFields, ws, now); hidden {
				off[plan.CustomFields] = w
			}
		}
		return planLoadedMsg{seq: seq, off: off}
	}
}

func (m *Model) now() time.Time {
	if m.deps.Now == nil {
		return time.Now()
	}
	return m.deps.Now()
}

// planOK reports whether feature is offered now (see planLoadedMsg).
func (m *Model) planOK(feature string) bool {
	w, ok := m.planOff[feature]
	return !ok || !w.off(m.now())
}

func (m *Model) tagUseCases(now time.Time) *app.Tags {
	return &app.Tags{Store: m.deps.Store, Clock: func() time.Time { return now }}
}

// updateSectionTags handles the messages of this file.
func (m *Model) updateSectionTags(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case planLoadedMsg:
		if msg.seq != m.planSeq {
			return nil, true
		}
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
	m.prompt.newSectionAfter = section
	if section != "" {
		m.prompt.input.Placeholder = "「" + name + "」の後に追加（Enter で追加 / Esc で取り消し）"
	}
	return m.prompt.input.Focus()
}

func (m *Model) createSection(name string) tea.Cmd {
	project, after := m.currentProject(), m.prompt.newSectionAfter
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
		m.board.col = j // keep the moved column selected
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

// openTagColors offers the colors Asana takes in a write (tag.Colors), each labelled with
// the name Asana now shows for it. The current color is marked: the same name if the cache
// holds a written one, else the first color Asana shows by that name (tag.WriteColor; the
// cache holds what a pull got, red, blue, ...).
func (m *Model) openTagColors(t readmodel.TagCount) {
	current := "none"
	if tag.CanonicalColor(t.Color) != "" {
		current, _ = tag.WriteColor(tag.CanonicalColor(t.Color))
		if w, ok := tag.WriteColor(t.Color); ok {
			current = w
		}
	}
	items := []pickerItem{{label: "色なし", value: "none"}}
	for _, c := range tag.Colors {
		items = append(items, pickerItem{label: c + "（" + tag.CanonicalColor(c) + "）", value: c})
	}
	cursor := 0
	for i := range items {
		if items[i].value == current {
			items[i].hint, cursor = "いまの色", i
		}
	}
	m.picker = &picker{kind: pickTagColor, title: "#" + t.Name + " の色", items: items, cursor: cursor}
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
			m.prompt.input.SetValue(t.Name)
			m.prompt.input.CursorEnd()
			return m.prompt.input.Focus(), true
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
