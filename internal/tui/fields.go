package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asagao/internal/domain/plan"
	"github.com/amasok/asagao/internal/readmodel"
)

// Custom fields: shown in the detail view (enter changes one: a chooser for enum fields,
// a prompt for the others), as short badges in the list and on the board (the fields a
// project marks as important), and usable as a list filter (space F).

// fieldFilter keeps tasks whose custom field has a value ("" field: no filter).
type fieldFilter struct {
	field, fieldName, value string
}

func (f fieldFilter) label() string { return f.fieldName + "=" + f.value }

// fieldBadges is the short text of a task's important enum values, e.g. " [高]".
func fieldBadges(t *readmodel.TaskSummary) string {
	var b strings.Builder
	for _, f := range t.CustomFields {
		if f.Important && f.Display != "" && (f.Type == readmodel.CFEnum || f.Type == readmodel.CFMultiEnum) {
			b.WriteString(" [" + f.Display + "]")
		}
	}
	return b.String()
}

// editCustomField starts changing field f of task t.
func (m *Model) editCustomField(t readmodel.TaskSummary, f readmodel.CustomFieldView) tea.Cmd {
	if !f.Editable {
		m.status = fmt.Sprintf("「%s」は変更できません（%s）", f.Name, f.Type)
		return nil
	}
	switch f.Type {
	case readmodel.CFEnum, readmodel.CFMultiEnum:
		m.showFieldPicker(t, f)
		return nil
	}
	m.openPrompt(promptField)
	m.prompt.fieldTask, m.prompt.fieldEdit = t, f
	m.prompt.input.Prompt = f.Name + ": "
	switch f.Type {
	case readmodel.CFNumber:
		m.prompt.input.Placeholder = "数値（空欄で空にする）"
	case readmodel.CFDate:
		m.prompt.input.Placeholder = "2026-12-01 / 明日 / +3d（空欄で空にする）"
	case readmodel.CFPeople:
		m.prompt.input.Placeholder = "me / 名前（カンマ区切りで複数。空欄で空にする）"
	default:
		m.prompt.input.Placeholder = "テキスト（空欄で空にする）"
	}
	m.prompt.input.SetValue(fieldInput(f))
	m.prompt.input.CursorEnd()
	return m.prompt.input.Focus()
}

// fieldInput is a field's value as the prompt takes it back: the raw number (the display
// may be formatted, e.g. "¥1,000" or "50%"), the date without a time, else the display.
func fieldInput(f readmodel.CustomFieldView) string {
	switch v := f.Value.(type) {
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case string:
		if f.Type == readmodel.CFDate || f.Type == readmodel.CFText {
			return v
		}
	}
	return f.Display
}

// showFieldPicker opens the chooser of an enum field (multi_enum: toggles, stays open).
func (m *Model) showFieldPicker(t readmodel.TaskSummary, f readmodel.CustomFieldView) {
	if len(f.Options) == 0 && !(f.CanAddOption && m.planOK(plan.CustomFields)) {
		m.status = fmt.Sprintf("「%s」の選択肢がまだ取得されていません（同期すると選べます）", f.Name)
		return
	}
	multi := f.Type == readmodel.CFMultiEnum
	kind, title := pickField, f.Name+"（"+truncateRunes(t.Name, 30)+"）"
	if multi {
		kind, title = pickFieldMulti, title+"  enter / space で付け外し・esc で閉じる"
	}
	p := &picker{kind: kind, title: title, task: t, field: f}
	if m.picker != nil && m.picker.kind == kind && m.picker.field.GID == f.GID {
		p.cursor = m.picker.cursor // refresh in place after a toggle
	}
	selected := map[string]bool{}
	switch v := f.Value.(type) {
	case string:
		selected[v] = true
	case []string:
		for _, g := range v {
			selected[g] = true
		}
	}
	for i, o := range f.Options {
		item := pickerItem{label: o.Name, value: o.GID, checked: selected[o.GID]}
		if multi {
			item.label = map[bool]string{true: "[✓] ", false: "[ ] "}[item.checked] + o.Name
		} else if item.checked {
			item.hint = "現在"
			p.cursor = i
		}
		p.items = append(p.items, item)
	}
	if f.CanAddOption && m.planOK(plan.CustomFields) {
		p.items = append(p.items, pickerItem{label: "＋ 選択肢を足して設定する…", value: newOptionValue})
	}
	if !multi {
		none := pickerItem{label: "（空にする）", value: ""}
		if len(selected) == 0 {
			none.hint = "現在"
			p.cursor = len(p.items)
		}
		p.items = append(p.items, none)
	}
	p.cursor = min(p.cursor, len(p.items)-1)
	m.picker = p
}

// fieldSetMsg reports a custom field change; reopen keeps a multi_enum chooser open.
type fieldSetMsg struct {
	task   readmodel.TaskSummary
	field  string
	status string
	reopen bool
	err    error
}

// setField sets field f of task t to value (stored form, nil clears).
func (m *Model) setField(t readmodel.TaskSummary, f readmodel.CustomFieldView, value any, reopen bool) tea.Cmd {
	relations := m.relations(m.deps.Now())
	return func() tea.Msg {
		display, err := relations.SetFieldHere(m.ctx, t.GID, f.GID, value)
		if err != nil {
			return fieldSetMsg{task: t, field: f.GID, reopen: reopen, err: err}
		}
		status := fmt.Sprintf("「%s」を「%s」にしました", f.Name, display)
		if display == "" {
			status = fmt.Sprintf("「%s」を空にしました", f.Name)
		}
		return fieldSetMsg{task: t, field: f.GID, status: status, reopen: reopen}
	}
}

// toggleFieldOption adds or removes an option of a multi_enum field. The chooser is
// updated at once, so quick toggles build on each other; the changes are saved one at a
// time (a toggle made while one is being saved is saved after it, with every toggle so far).
func (m *Model) toggleFieldOption(p *picker, item pickerItem) tea.Cmd {
	if item.value == newOptionValue {
		m.picker = nil
		return m.startNewOption(p.task, p.field)
	}
	var gids []string
	for i := range p.items {
		it := &p.items[i]
		if it.value == item.value {
			it.checked = !it.checked
			it.label = map[bool]string{true: "[✓] ", false: "[ ] "}[it.checked] + strings.TrimPrefix(strings.TrimPrefix(it.label, "[✓] "), "[ ] ")
		}
		if it.checked && it.value != newOptionValue {
			gids = append(gids, it.value)
		}
	}
	key := fieldKey(p.task.GID, p.field.GID)
	if m.fieldSaving == nil {
		m.fieldSaving, m.fieldQueued = map[string]bool{}, map[string]*queuedField{}
	}
	if m.fieldSaving[key] {
		m.fieldQueued[key] = &queuedField{task: p.task, field: p.field, value: gids}
		return nil
	}
	m.fieldSaving[key] = true
	return m.setField(p.task, p.field, gids, true)
}

// fieldKey identifies a field of a task: saves of one field are made one at a time.
func fieldKey(task, field string) string { return task + "\x00" + field }

// queuedField is a multi_enum value to save once the change being saved is done.
type queuedField struct {
	task  readmodel.TaskSummary
	field readmodel.CustomFieldView
	value []string
}

func (m *Model) submitField(input string) tea.Cmd {
	relations, t, f := m.relations(m.deps.Now()), m.prompt.fieldTask, m.prompt.fieldEdit
	value, err := relations.ParseFieldValue(m.ctx, f.GID, input)
	if err != nil {
		return func() tea.Msg { return writeDoneMsg{err: err} }
	}
	return m.setField(t, f, value, false)
}

// reopenFieldPicker shows the multi_enum chooser again with the stored values.
func (m *Model) reopenFieldPicker(t readmodel.TaskSummary, fieldGID string) tea.Cmd {
	st := m.deps.Store
	return func() tea.Msg {
		d, err := queryOf(st).GetTask(m.ctx, t.GID)
		if err != nil {
			return fieldPickerMsg{err: err}
		}
		for _, f := range d.CustomFields {
			if f.GID == fieldGID {
				return fieldPickerMsg{task: d.TaskSummary, field: f}
			}
		}
		return fieldPickerMsg{}
	}
}

type fieldPickerMsg struct {
	task  readmodel.TaskSummary
	field readmodel.CustomFieldView
	err   error
}

// --- filter ---

type fieldOptionsMsg struct {
	options []readmodel.FieldOption
	err     error
}

func (m *Model) openFieldFilterPicker() tea.Cmd {
	st := m.deps.Store
	return func() tea.Msg {
		opts, err := queryOf(st).ListFieldOptions(m.ctx)
		return fieldOptionsMsg{options: opts, err: err}
	}
}

func (m *Model) showFieldFilterPicker(opts []readmodel.FieldOption) {
	if len(opts) == 0 && m.list.fieldFilter.field == "" {
		m.status = "絞り込みに使える選択式のカスタムフィールドがありません"
		return
	}
	p := &picker{kind: pickFieldFilter, title: "カスタムフィールドで絞り込む", items: []pickerItem{{label: "（すべてのタスク）"}}}
	for _, o := range opts {
		p.items = append(p.items, pickerItem{label: o.FieldName + ": " + o.Option, value: o.FieldGID + "\x00" + o.FieldName + "\x00" + o.Option,
			hint: fmt.Sprintf("未完了 %d", o.Tasks)})
		if o.FieldGID == m.list.fieldFilter.field && o.Option == m.list.fieldFilter.value {
			p.cursor = len(p.items) - 1
		}
	}
	m.picker = p
}

func (m *Model) applyFieldFilter(value string) tea.Cmd {
	m.list.fieldFilter = fieldFilter{}
	if parts := strings.Split(value, "\x00"); len(parts) == 3 {
		m.list.fieldFilter = fieldFilter{field: parts[0], fieldName: parts[1], value: parts[2]}
	}
	m.list.cursor, m.list.offset = 0, 0
	return m.loadTasks()
}

// updateFields handles the messages of this file.
func (m *Model) updateFields(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case fieldSetMsg:
		if msg.err != nil {
			m.status = "エラー: " + msg.err.Error()
			if msg.reopen { // show what is stored; later toggles built on the failed one
				key := fieldKey(msg.task.GID, msg.field)
				delete(m.fieldSaving, key)
				delete(m.fieldQueued, key)
				return m.reopenFieldPicker(msg.task, msg.field), true
			}
			return nil, true
		}
		m.status = msg.status
		if msg.reopen {
			key := fieldKey(msg.task.GID, msg.field)
			if q := m.fieldQueued[key]; q != nil { // toggled meanwhile: save that next
				delete(m.fieldQueued, key)
				return tea.Batch(m.afterWrite(), m.setField(q.task, q.field, q.value, true)), true
			}
			delete(m.fieldSaving, key)
			return tea.Batch(m.afterWrite(), m.reopenFieldPicker(msg.task, msg.field)), true
		}
		return m.afterWrite(), true
	case fieldPickerMsg:
		if msg.err != nil {
			m.status = "エラー: " + msg.err.Error()
		} else if msg.field.GID != "" && m.picker != nil && m.picker.kind == pickFieldMulti &&
			!m.fieldSaving[fieldKey(msg.task.GID, msg.field.GID)] {
			m.showFieldPicker(msg.task, msg.field)
		}
		return nil, true
	case fieldOptionsMsg:
		if msg.err != nil {
			m.status = "カスタムフィールドを読み込めませんでした: " + msg.err.Error()
		} else {
			m.showFieldFilterPicker(msg.options)
		}
		return nil, true
	}
	return nil, false
}
