package tui

import (
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/termsafe"
)

// Custom fields themselves (#37): adding an option to an enum field from its chooser
// ("＋ 選択肢を足して設定する…"), and adding a field of the workspace to the project, or
// removing it (space G). Both go through the outbox; what Asana refused (402 / 403) is not
// offered again.

// newOptionValue is the chooser item that adds an option.
const newOptionValue = "\x00new"

func (m *Model) fieldOptions(now time.Time) *app.FieldOptions {
	return m.deps.App.FieldOptions(func() time.Time { return now })
}

func (m *Model) projectFields(now time.Time) *app.ProjectFields {
	return m.deps.App.ProjectFields(func() time.Time { return now })
}

// startNewOption asks for the name of an option to add to field f and set on task t.
func (m *Model) startNewOption(t readmodel.TaskSummary, f readmodel.CustomFieldView) tea.Cmd {
	m.openPrompt(promptFieldOption)
	m.prompt.fieldTask, m.prompt.fieldEdit = t, f
	m.prompt.input.Prompt = "新しい選択肢（" + f.Name + "）: "
	m.prompt.input.Placeholder = "名前（Enter で選択肢に足して設定する / Esc で取り消し）"
	return m.prompt.input.Focus()
}

func (m *Model) submitNewOption(name string) tea.Cmd {
	options, t, f := m.fieldOptions(m.deps.Now()), m.prompt.fieldTask, m.prompt.fieldEdit
	return func() tea.Msg {
		display, err := options.SetWithNewOption(m.ctx, t.GID, f, name)
		if err != nil {
			return fieldSetMsg{task: t, field: f.GID, err: err}
		}
		return fieldSetMsg{task: t, field: f.GID, status: fmt.Sprintf("「%s」を「%s」にしました（選択肢「%s」は同期のときに Asana に足します）", f.Name, display, name)}
	}
}

// --- the project's fields ---

type projectFieldsMsg struct {
	project string
	fields  []readmodel.ProjectFieldView
	err     error
}

func (m *Model) openProjectFields() tea.Cmd {
	project := m.currentProject()
	if project == "" {
		return nil
	}
	return m.loadProjectFields(project)
}

func (m *Model) loadProjectFields(project string) tea.Cmd {
	pf := m.projectFields(m.deps.Now())
	return func() tea.Msg {
		if err := pf.Changeable(m.ctx, project); err != nil {
			return projectFieldsMsg{err: err}
		}
		_, fields, err := pf.List(m.ctx, project, true)
		return projectFieldsMsg{project: project, fields: fields, err: err}
	}
}

func (m *Model) showProjectFields(msg projectFieldsMsg) {
	if len(msg.fields) == 0 {
		m.status = "このワークスペースのカスタムフィールドがまだ取得されていません（同期すると選べます）"
		return
	}
	cursor := 0
	if m.picker != nil && m.picker.kind == pickProjectFields {
		cursor = m.picker.cursor
	}
	p := &picker{kind: pickProjectFields, project: msg.project, cursor: cursor,
		title: "カスタムフィールド（" + m.currentScope().name + "）  enter / space で追加・外す・esc で閉じる"}
	for _, f := range msg.fields {
		mark := map[bool]string{true: "[✓] ", false: "[ ] "}[f.Attached]
		item := pickerItem{label: mark + termsafe.String(f.Name), value: f.GID, checked: f.Attached, hint: f.Type}
		if f.Pending {
			item.hint = f.Type + "・未送信"
		}
		p.items = append(p.items, item)
	}
	p.cursor = clamp(p.cursor, 0, len(p.items)-1)
	m.picker = p
}

// toggleProjectField adds the field to the project, or (after asking) removes it.
func (m *Model) toggleProjectField(p *picker, item pickerItem) tea.Cmd {
	if item.checked {
		name := pickerName(item.label)
		m.picker = &picker{kind: pickRemoveField, project: p.project,
			title: fmt.Sprintf("カスタムフィールド「%s」をこのプロジェクトから外しますか？（このプロジェクトのタスクから、このフィールドと値が外れます）", name),
			items: []pickerItem{{label: "やめる"}, {label: "外す", value: item.value, hint: name}}}
		return nil
	}
	return m.setProjectField(p.project, item.value, pickerName(item.label), true)
}

// pickerName is a toggle item's label without its mark.
func pickerName(label string) string {
	for _, mark := range []string{"[✓] ", "[ ] "} {
		if len(label) >= len(mark) && label[:len(mark)] == mark {
			return label[len(mark):]
		}
	}
	return label
}

func (m *Model) setProjectField(project, field, name string, add bool) tea.Cmd {
	pf := m.projectFields(m.deps.Now())
	reload := m.loadProjectFields(project)
	return func() tea.Msg {
		if err := pf.SetHere(m.ctx, project, field, add); err != nil {
			return writeDoneMsg{err: err}
		}
		status := fmt.Sprintf("カスタムフィールド「%s」をプロジェクトに追加しました", name)
		if !add {
			status = fmt.Sprintf("カスタムフィールド「%s」をプロジェクトから外しました", name)
		}
		msg := reload().(projectFieldsMsg)
		return projectFieldSetMsg{status: status, reload: msg}
	}
}

type projectFieldSetMsg struct {
	status string
	reload projectFieldsMsg
}

// updateProjectFields handles the messages of this file.
func (m *Model) updateProjectFields(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case projectFieldsMsg:
		if msg.err != nil {
			m.status = "エラー: " + msg.err.Error()
			return nil, true
		}
		m.showProjectFields(msg)
		return nil, true
	case projectFieldSetMsg:
		m.status = msg.status
		if m.picker != nil && m.picker.kind == pickProjectFields && msg.reload.err == nil {
			m.showProjectFields(msg.reload)
		}
		return m.afterWrite(), true
	}
	return nil, false
}
