package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/dates"
	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/termsafe"
)

// Custom fields. Values are kept in the form Asana accepts when setting them (PUT
// /tasks/{gid} custom_fields), so a local change is sent as it is stored:
//
//	text: string, number: float64, enum: option gid, multi_enum / people: sorted []string
//	of gids, date: "YYYY-MM-DD"; nil when empty.
//
// A change is queued as a set_custom_field entry (one per task and field), conflict-checked
// against Asana like update_task. Workspaces without custom fields (e.g. free plans) simply
// have none: nothing is shown and nothing fails.

const (
	// The types of custom fields are readmodel's.
	CFText      = readmodel.CFText
	CFNumber    = readmodel.CFNumber
	CFEnum      = readmodel.CFEnum
	CFMultiEnum = readmodel.CFMultiEnum
	CFDate      = readmodel.CFDate
	CFPeople    = readmodel.CFPeople

	// OpSetCustomField sets one custom field of a task (outbox.KindSetCustomField).
	OpSetCustomField = outbox.KindSetCustomField
)

// editableCFTypes are the types whose values asagao can set.
var editableCFTypes = []string{CFText, CFNumber, CFEnum, CFMultiEnum, CFDate, CFPeople}

// EnumOptionView and CustomFieldView are readmodel's (aliases, so that internal/app can
// use them without importing the store). A CustomFieldView's Value is in the form above.
type (
	EnumOptionView  = readmodel.EnumOptionView
	CustomFieldView = readmodel.CustomFieldView
)

// fieldDef is a custom field definition from the store.
type fieldDef struct {
	GID, Name, Type string
	Precision       int
	ReadOnly        bool
	Options         []EnumOptionView
	OptionsKnown    bool
}

func (d fieldDef) editable() bool { return !d.ReadOnly && slices.Contains(editableCFTypes, d.Type) }

func (d fieldDef) option(gid string) (EnumOptionView, bool) {
	for _, o := range d.Options {
		if o.GID == gid {
			return o, true
		}
	}
	return EnumOptionView{}, false
}

func (d fieldDef) enabledOptions(current any) []EnumOptionView {
	var cur []string
	switch v := current.(type) {
	case string:
		cur = []string{v}
	case []string:
		cur = v
	}
	var out []EnumOptionView
	for _, o := range d.Options {
		if o.Enabled || slices.Contains(cur, o.GID) {
			out = append(out, o)
		}
	}
	return out
}

// --- values ---

// The stored form of values from Asana is readmodel's (e1), so that sync can use it
// without importing the store.
var (
	// CustomFieldValue returns the value of a custom field from Asana in the stored form.
	CustomFieldValue = readmodel.CustomFieldValue
	// CustomFieldAPIValue is a stored value as PUT /tasks sends it.
	CustomFieldAPIValue = readmodel.CustomFieldAPIValue
	// ServerCustomField finds a field on a task fetched from Asana.
	ServerCustomField = readmodel.ServerCustomField
)

// gidList sorts gids; nil when there are none.
func gidList(gids []string) any { return readmodel.GIDList(gids) }

// normalizeCFValue turns a value decoded from JSON into the stored form.
func normalizeCFValue(v any) any { return readmodel.NormalizeCustomFieldValue(v) }

// formatNumber formats a number field value with the field's precision.
func formatNumber(v float64, precision int) string {
	return strconv.FormatFloat(v, 'f', max(0, min(precision, 6)), 64)
}

// cfDisplay is the text of a value set locally.
func (s *Store) cfDisplay(ctx context.Context, q dbQuerier, d fieldDef, v any) string {
	return valueDisplay(ctx, q, d, v)
}

// valueDisplay is the text of a value set locally (people's names read with q).
func valueDisplay(ctx context.Context, q dbQuerier, d fieldDef, v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case float64:
		return formatNumber(x, d.Precision)
	case string:
		if d.Type == CFEnum {
			if o, ok := d.option(x); ok {
				return o.Name
			}
		}
		return x
	case []string:
		var names []string
		for _, gid := range x {
			name := gid
			if d.Type == CFPeople {
				_ = q.QueryRowContext(ctx, "SELECT name FROM users WHERE gid = ?", gid).Scan(&name)
			} else if o, ok := d.option(gid); ok {
				name = o.Name
			}
			names = append(names, name)
		}
		return strings.Join(names, ", ")
	}
	return fmt.Sprint(v)
}

type dbQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// --- storing what Asana returned ---

// upsertFieldDef stores a field definition. Enum options are replaced only when f has them.
func upsertFieldDef(ctx context.Context, tx *sql.Tx, f model.CustomField) error {
	options := ""
	if f.EnumOptions != nil {
		views := make([]EnumOptionView, 0, len(f.EnumOptions))
		for _, o := range f.EnumOptions {
			views = append(views, EnumOptionView{GID: o.GID, Name: o.Name, Color: o.Color, Enabled: o.Enabled == nil || *o.Enabled})
		}
		// Options added offline stay until Asana created them (or they are discarded).
		local, err := localOptions(ctx, tx, f.GID)
		if err != nil {
			return err
		}
		views = append(views, local...)
		b, err := json.Marshal(views)
		if err != nil {
			return err
		}
		options = string(b)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO custom_fields(gid, name, type, precision, read_only, enum_options)
		VALUES(?, ?, ?, ?, ?, ?)
		ON CONFLICT(gid) DO UPDATE SET name = excluded.name, type = excluded.type, precision = excluded.precision,
			read_only = excluded.read_only,
			enum_options = CASE WHEN excluded.enum_options <> '' THEN excluded.enum_options ELSE custom_fields.enum_options END`,
		f.GID, f.Name, f.Subtype(), f.Precision, f.IsFormulaField || f.IsValueReadOnly, options)
	return err
}

// upsertTaskCustomFields replaces a task's custom field values (if the response had them).
func upsertTaskCustomFields(ctx context.Context, tx *sql.Tx, t model.Task) error {
	if t.CustomFields == nil {
		return nil
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM task_custom_fields WHERE task_gid = ?", t.GID); err != nil {
		return err
	}
	for i, f := range t.CustomFields {
		if f.GID == "" || (f.Enabled != nil && !*f.Enabled) {
			continue
		}
		if err := upsertFieldDef(ctx, tx, f); err != nil {
			return err
		}
		for _, u := range f.PeopleValue {
			if _, err := tx.ExecContext(ctx, `INSERT INTO users(gid, name) VALUES(?, ?)
				ON CONFLICT(gid) DO UPDATE SET name = CASE WHEN excluded.name <> '' THEN excluded.name ELSE users.name END`, u.GID, u.Name); err != nil {
				return err
			}
		}
		v := CustomFieldValue(f)
		display := ""
		if f.DisplayValue != nil {
			display = *f.DisplayValue
		}
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO task_custom_fields(task_gid, field_gid, position, value, display)
			VALUES(?, ?, ?, ?, ?)`, t.GID, f.GID, i, string(b), display); err != nil {
			return err
		}
	}
	return nil
}

// ReplaceProjectCustomFields stores the custom fields of a project (with their enum options).
func (s *Store) ReplaceProjectCustomFields(ctx context.Context, projectGID string, settings []model.CustomFieldSetting) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM project_custom_fields WHERE project_gid = ?", projectGID); err != nil {
			return err
		}
		for i, st := range settings {
			f := st.CustomField
			if f.GID == "" {
				continue
			}
			if f.EnumOptions == nil && (f.Subtype() == CFEnum || f.Subtype() == CFMultiEnum) {
				f.EnumOptions = []model.EnumOption{} // the settings list every option: none is none
			}
			if err := upsertFieldDef(ctx, tx, f); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO project_custom_fields(project_gid, field_gid, position, is_important)
				VALUES(?, ?, ?, ?)`, projectGID, f.GID, i, st.IsImportant); err != nil {
				return err
			}
		}
		return reapplyFieldSettingOps(ctx, tx, projectGID)
	})
}

// --- reading ---

func loadFieldDefs(ctx context.Context, q dbQuerier) (map[string]fieldDef, error) {
	rows, err := q.QueryContext(ctx, "SELECT gid, name, type, precision, read_only, enum_options FROM custom_fields")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	defs := map[string]fieldDef{}
	for rows.Next() {
		var d fieldDef
		var options string
		if err := rows.Scan(&d.GID, &d.Name, &d.Type, &d.Precision, &d.ReadOnly, &options); err != nil {
			return nil, err
		}
		if options != "" {
			d.OptionsKnown = true
			if err := termsafe.Unmarshal([]byte(options), &d.Options); err != nil {
				return nil, fmt.Errorf("custom field %s: %w", d.GID, err)
			}
		}
		defs[d.GID] = d
	}
	return defs, rows.Err()
}

func getFieldDef(ctx context.Context, q dbQuerier, gid string) (fieldDef, error) {
	defs, err := loadFieldDefs(ctx, q)
	if err != nil {
		return fieldDef{}, err
	}
	d, ok := defs[gid]
	if !ok {
		return d, fmt.Errorf("custom field %s: %w", gid, ErrNotFound)
	}
	return d, nil
}

// importantFields returns the fields some project marks as important.
func importantFields(ctx context.Context, q dbQuerier) (map[string]bool, error) {
	rows, err := q.QueryContext(ctx, "SELECT DISTINCT field_gid FROM project_custom_fields WHERE is_important = 1")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var gid string
		if err := rows.Scan(&gid); err != nil {
			return nil, err
		}
		out[gid] = true
	}
	return out, rows.Err()
}

func (d fieldDef) view(value any, display string, important bool) CustomFieldView {
	v := CustomFieldView{GID: d.GID, Name: d.Name, Type: d.Type, Value: value, Display: display,
		Important: important, Editable: d.editable()}
	if g, ok := value.(string); ok && d.Type == CFEnum {
		if o, ok := d.option(g); ok {
			v.Color = o.Color
			if v.Display == "" {
				v.Display = o.Name
			}
		}
	}
	return v
}

func decodeCFValue(s string) (any, error) {
	var v any
	if err := termsafe.Unmarshal([]byte(s), &v); err != nil {
		return nil, err
	}
	return normalizeCFValue(v), nil
}

// fillCustomFields adds the set custom field values to listed tasks.
func (s *Store) fillCustomFields(ctx context.Context, idx map[string]*TaskSummary, in string, ids []any) error {
	rows, err := s.db.QueryContext(ctx, `SELECT task_gid, field_gid, value, display FROM task_custom_fields
		WHERE task_gid IN `+in+` AND (value <> 'null' OR display <> '') ORDER BY task_gid, position`, ids...)
	if err != nil {
		return err
	}
	type row struct{ task, field, value, display string }
	var found []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.task, &r.field, &r.value, &r.display); err != nil {
			rows.Close()
			return err
		}
		found = append(found, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(found) == 0 {
		return err
	}
	defs, err := loadFieldDefs(ctx, s.db)
	if err != nil {
		return err
	}
	important, err := importantFields(ctx, s.db)
	if err != nil {
		return err
	}
	for _, r := range found {
		d, ok := defs[r.field]
		if !ok {
			continue
		}
		v, err := decodeCFValue(r.value)
		if err != nil {
			return err
		}
		idx[r.task].CustomFields = append(idx[r.task].CustomFields, d.view(v, r.display, important[r.field]))
	}
	return nil
}

// taskCustomFields returns every custom field of a task: the ones it has values for, then
// the fields of its projects it has no row for yet (e.g. a task created offline).
func (s *Store) taskCustomFields(ctx context.Context, gid string) ([]CustomFieldView, error) {
	defs, err := loadFieldDefs(ctx, s.db)
	if err != nil || len(defs) == 0 {
		return nil, err
	}
	important, err := importantFields(ctx, s.db)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT field_gid, value, display FROM (
			SELECT field_gid, value, display, 0 AS grp, position, '' AS project FROM task_custom_fields WHERE task_gid = ?1
			UNION ALL
			SELECT pf.field_gid, 'null', '', 1, pf.position, pf.project_gid FROM project_custom_fields pf
			JOIN task_memberships m ON m.project_gid = pf.project_gid AND m.task_gid = ?1
			WHERE pf.field_gid NOT IN (SELECT field_gid FROM task_custom_fields WHERE task_gid = ?1))
		ORDER BY grp, project, position`, gid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CustomFieldView
	seen := map[string]bool{}
	for rows.Next() {
		var field, value, display string
		if err := rows.Scan(&field, &value, &display); err != nil {
			return nil, err
		}
		d, ok := defs[field]
		if !ok || seen[field] {
			continue
		}
		seen[field] = true
		v, err := decodeCFValue(value)
		if err != nil {
			return nil, err
		}
		view := d.view(v, display, important[field])
		if d.Type == CFEnum || d.Type == CFMultiEnum {
			view.Options = d.enabledOptions(v)
			if view.Editable && d.OptionsKnown {
				view.CanAddOption, err = s.optionsAddable(ctx, field)
				if err != nil {
					return nil, err
				}
			}
		}
		out = append(out, view)
	}
	return out, rows.Err()
}

// optionsAddable reports whether options can be added to a field: unless Asana refused it
// for this field before (403). Whether the workspace's plan has it (402) is the use
// case's to say (app.FieldOptions.Addable).
func (s *Store) optionsAddable(ctx context.Context, fieldGID string) (bool, error) {
	v, err := s.SyncState(ctx, readmodel.KeyFieldOptionsLocked+fieldGID)
	return v == "", err
}

// ResolveCustomField finds a custom field of a task by gid or name (exact, ignoring case,
// or a unique part of it).
func (s *Store) ResolveCustomField(ctx context.Context, taskGID, arg string) (CustomFieldView, error) {
	fields, err := s.taskCustomFields(ctx, taskGID)
	if err != nil {
		return CustomFieldView{}, err
	}
	arg = strings.TrimSpace(arg)
	var partial []CustomFieldView
	for _, f := range fields {
		if f.GID == arg || strings.EqualFold(f.Name, arg) {
			return f, nil
		}
		if strings.Contains(strings.ToLower(f.Name), strings.ToLower(arg)) {
			partial = append(partial, f)
		}
	}
	switch len(partial) {
	case 1:
		return partial[0], nil
	case 0:
		var names []string
		for _, f := range fields {
			names = append(names, f.Name)
		}
		if len(names) == 0 {
			return CustomFieldView{}, fmt.Errorf("カスタムフィールド %q: このタスクにはカスタムフィールドがありません: %w", arg, ErrNotFound)
		}
		return CustomFieldView{}, fmt.Errorf("カスタムフィールド %q（このタスクのフィールド: %s）: %w", arg, strings.Join(names, ", "), ErrNotFound)
	}
	var names []string
	for _, f := range partial {
		names = append(names, f.Name)
	}
	return CustomFieldView{}, fmt.Errorf("カスタムフィールド %q matches %s: %w", arg, strings.Join(names, ", "), ErrAmbiguous)
}

// ParseCustomFieldValue turns user input into a value of field fieldGID. input is a string
// (an option name or gid, several separated by commas for multi_enum / people; a number; a
// date such as 2026-12-01 / 明日 / +3d; a person as for ResolveUser), a number, a list of
// strings, or nil. Empty input clears the field.
func (s *Store) ParseCustomFieldValue(ctx context.Context, fieldGID string, input any, now time.Time) (any, error) {
	d, err := getFieldDef(ctx, s.db, fieldGID)
	if err != nil {
		return nil, err
	}
	if !d.editable() {
		return nil, fmt.Errorf("カスタムフィールド「%s」は変更できません（%s）", d.Name, d.Type)
	}
	var parts []string
	switch x := input.(type) {
	case nil:
		return nil, nil
	case float64:
		if d.Type == CFNumber {
			return x, nil
		}
		parts = []string{formatNumber(x, 6)}
	case int:
		return s.ParseCustomFieldValue(ctx, fieldGID, float64(x), now)
	case string:
		if strings.TrimSpace(x) == "" {
			return nil, nil
		}
		if d.Type == CFText {
			return x, nil
		}
		if d.Type == CFMultiEnum || d.Type == CFPeople {
			parts = strings.Split(x, ",")
		} else {
			parts = []string{x}
		}
	case []string:
		parts = x
	case []any:
		for _, e := range x {
			parts = append(parts, fmt.Sprint(e))
		}
	default:
		return nil, fmt.Errorf("カスタムフィールド「%s」に設定できない値です: %v", d.Name, input)
	}
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	parts = slices.DeleteFunc(parts, func(p string) bool { return p == "" })
	if len(parts) == 0 {
		return nil, nil
	}
	if len(parts) > 1 && d.Type != CFMultiEnum && d.Type != CFPeople {
		return nil, fmt.Errorf("カスタムフィールド「%s」には値を 1 つだけ指定してください", d.Name)
	}
	switch d.Type {
	case CFText:
		return parts[0], nil
	case CFNumber:
		n, err := strconv.ParseFloat(strings.ReplaceAll(parts[0], ",", ""), 64)
		if err != nil {
			return nil, fmt.Errorf("カスタムフィールド「%s」は数値です: %q", d.Name, parts[0])
		}
		return n, nil
	case CFDate:
		day, err := dates.Parse(parts[0], now)
		if err != nil {
			return nil, fmt.Errorf("カスタムフィールド「%s」: %w", d.Name, err)
		}
		return day, nil
	case CFPeople:
		var gids []string
		for _, p := range parts {
			gid, err := s.ResolveUser(ctx, p)
			if err != nil {
				return nil, err
			}
			if gid != "" {
				gids = append(gids, gid)
			}
		}
		return gidList(gids), nil
	}
	// enum / multi_enum
	if !d.OptionsKnown {
		return nil, fmt.Errorf("カスタムフィールド「%s」の選択肢がまだ取得されていません（オンラインで同期してください）", d.Name)
	}
	var gids []string
	for _, p := range parts {
		o, err := d.resolveOption(p)
		if err != nil {
			return nil, err
		}
		gids = append(gids, o.GID)
	}
	if d.Type == CFEnum {
		return gids[0], nil
	}
	return gidList(gids), nil
}

// resolveOption finds an enabled option by gid, name (ignoring case), or a unique part of the name.
func (d fieldDef) resolveOption(arg string) (EnumOptionView, error) {
	var partial []EnumOptionView
	var names []string
	for _, o := range d.Options {
		if !o.Enabled {
			continue
		}
		if o.GID == arg || strings.EqualFold(o.Name, arg) {
			return o, nil
		}
		names = append(names, o.Name)
		if strings.Contains(strings.ToLower(o.Name), strings.ToLower(arg)) {
			partial = append(partial, o)
		}
	}
	if len(partial) == 1 {
		return partial[0], nil
	}
	if len(partial) > 1 {
		var ns []string
		for _, o := range partial {
			ns = append(ns, o.Name)
		}
		return EnumOptionView{}, fmt.Errorf("「%s」の選択肢 %q matches %s: %w", d.Name, arg, strings.Join(ns, ", "), ErrAmbiguous)
	}
	return EnumOptionView{}, fmt.Errorf("「%s」の選択肢 %q（選択肢: %s）: %w", d.Name, arg, strings.Join(names, ", "), ErrNotFound)
}

// --- local changes ---

// SetCustomFieldLocal sets a custom field of a task (value in the stored form, e.g. from
// ParseCustomFieldValue; nil clears it) and queues the change. It returns the value as text.
// Setting the field back to its value in Asana removes the queued change.
func (s *Store) SetCustomFieldLocal(ctx context.Context, taskGID, fieldGID string, value any, now time.Time) (string, error) {
	value = normalizeCFValue(value)
	var display string
	err := s.tx(ctx, func(tx *sql.Tx) error {
		// An option added offline that Asana created meanwhile (the caller still held its
		// local gid, e.g. an open chooser): its Asana gid.
		var err error
		if value, err = realOptions(ctx, tx, value); err != nil {
			return err
		}
		var modifiedAt string
		if err := tx.QueryRowContext(ctx, "SELECT modified_at FROM tasks WHERE gid = ?", taskGID).Scan(&modifiedAt); err != nil {
			return notFound(err, "task "+taskGID)
		}
		d, err := getFieldDef(ctx, tx, fieldGID)
		if err != nil {
			return err
		}
		if !d.editable() {
			return fmt.Errorf("カスタムフィールド「%s」は変更できません（%s）", d.Name, d.Type)
		}
		if err := d.check(value); err != nil {
			return err
		}
		display = s.cfDisplay(ctx, tx, d, value)
		curValue, curDisplay, err := currentCustomField(ctx, tx, taskGID, fieldGID)
		if err != nil {
			return err
		}
		if err := applyCustomField(ctx, tx, taskGID, fieldGID, value, display); err != nil {
			return err
		}

		if IsLocalGID(taskGID) {
			op, err := findUnsentCreate(ctx, tx, taskGID)
			if err != nil {
				return err
			}
			if op != nil { // not sent yet: create it with the value
				cfs, _ := op.Payload["custom_fields"].(map[string]any)
				cfs = maps.Clone(cfs)
				if cfs == nil {
					cfs = map[string]any{}
				}
				if value == nil {
					delete(cfs, fieldGID)
				} else {
					cfs[fieldGID] = CustomFieldAPIValue(d.Type, value) // sent as is with the create
				}
				if len(cfs) == 0 {
					delete(op.Payload, "custom_fields")
				} else {
					op.Payload["custom_fields"] = cfs
				}
				return saveOp(ctx, tx, op)
			}
			if err := ensureCreateQueued(ctx, tx, taskGID); err != nil {
				return err
			}
		}

		op, err := findCustomFieldOp(ctx, tx, taskGID, fieldGID)
		if err != nil {
			return err
		}
		if op == nil {
			op = &Op{Kind: OpSetCustomField, TaskGID: taskGID, Base: map[string]any{"value": curValue, "display": curDisplay},
				BaseModifiedAt: modifiedAt, State: OpPending, CreatedAt: formatTime(now)}
		}
		// Back to the value in Asana, or (in a conflict) to the value Asana has now: nothing to send.
		if sameValue(normalizeCFValue(op.Base["value"]), value) ||
			(op.State == OpConflict && sameValue(normalizeCFValue(op.Conflict["value"]), value)) {
			if op.ID != 0 {
				_, err := tx.ExecContext(ctx, "DELETE FROM outbox WHERE id = ?", op.ID)
				return err
			}
			return nil
		}
		op.Payload = map[string]any{"field": fieldGID, "field_name": d.Name, "type": d.Type, "value": value, "display": display}
		return saveOp(ctx, tx, op)
	})
	return display, err
}

// check validates a value in the stored form against the field.
func (d fieldDef) check(v any) error {
	bad := func() error {
		return fmt.Errorf("カスタムフィールド「%s」（%s）に設定できない値です: %v", d.Name, d.Type, v)
	}
	switch x := v.(type) {
	case nil:
		return nil
	case float64:
		if d.Type != CFNumber {
			return bad()
		}
	case string:
		switch d.Type {
		case CFText:
		case CFDate:
			if _, err := time.Parse(time.DateOnly, x); err != nil {
				return bad()
			}
		case CFEnum:
			if d.OptionsKnown {
				if _, ok := d.option(x); !ok {
					return bad()
				}
			}
		default:
			return bad()
		}
	case []string:
		switch d.Type {
		case CFPeople:
		case CFMultiEnum:
			if d.OptionsKnown {
				for _, g := range x {
					if _, ok := d.option(g); !ok {
						return bad()
					}
				}
			}
		default:
			return bad()
		}
	default:
		return bad()
	}
	return nil
}

func currentCustomField(ctx context.Context, tx *sql.Tx, taskGID, fieldGID string) (any, string, error) {
	var value, display string
	err := tx.QueryRowContext(ctx, "SELECT value, display FROM task_custom_fields WHERE task_gid = ? AND field_gid = ?", taskGID, fieldGID).
		Scan(&value, &display)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	v, err := decodeCFValue(value)
	return v, display, err
}

func applyCustomField(ctx context.Context, tx *sql.Tx, taskGID, fieldGID string, value any, display string) error {
	b, err := json.Marshal(normalizeCFValue(value))
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO task_custom_fields(task_gid, field_gid, position, value, display)
		VALUES(?1, ?2, COALESCE((SELECT max(position) + 1 FROM task_custom_fields WHERE task_gid = ?1), 0), ?3, ?4)
		ON CONFLICT(task_gid, field_gid) DO UPDATE SET value = excluded.value, display = excluded.display`,
		taskGID, fieldGID, string(b), display)
	return err
}

// applyCustomFieldOp writes the value of a set_custom_field entry (or, with undo, its base).
func applyCustomFieldOp(ctx context.Context, tx *sql.Tx, op Op, undo bool) error {
	field, _ := op.Payload["field"].(string)
	src := op.Payload
	if undo {
		src = op.Base
	}
	display, _ := src["display"].(string)
	return applyCustomField(ctx, tx, op.TaskGID, field, src["value"], display)
}

// findCustomFieldOp returns the pending or conflicted entry setting field of task.
func findCustomFieldOp(ctx context.Context, tx *sql.Tx, taskGID, fieldGID string) (*Op, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM outbox WHERE kind = ? AND task_gid = ? AND state IN ('pending', 'conflict')
		AND json_extract(payload, '$.field') = ? ORDER BY id DESC LIMIT 1`, OpSetCustomField, taskGID, fieldGID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return getOpTx(ctx, tx, id)
}

// reapplyCustomFieldOps keeps unsent custom field changes after a pull.
func reapplyCustomFieldOps(ctx context.Context, tx *sql.Tx, taskGID string) error {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM outbox WHERE task_gid = ? AND kind = ? AND state <> 'failed' ORDER BY id`,
		taskGID, OpSetCustomField)
	if err != nil {
		return err
	}
	ids, err := scanIDs(rows)
	if err != nil {
		return err
	}
	for _, id := range ids {
		op, err := getOpTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := applyCustomFieldOp(ctx, tx, *op, false); err != nil {
			return err
		}
	}
	return nil
}

// --- filter ---

// FieldOption is readmodel.FieldOption.
type FieldOption = readmodel.FieldOption

// ListFieldOptions returns the enum options in use by incomplete tasks, by field.
func (s *Store) ListFieldOptions(ctx context.Context) ([]FieldOption, error) {
	defs, err := loadFieldDefs(ctx, s.db)
	if err != nil || len(defs) == 0 {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT cf.field_gid, cf.value FROM task_custom_fields cf
		JOIN tasks t ON t.gid = cf.task_gid WHERE t.completed = 0 AND cf.value <> 'null'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[[2]string]int{}
	for rows.Next() {
		var field, value string
		if err := rows.Scan(&field, &value); err != nil {
			return nil, err
		}
		d := defs[field]
		if d.Type != CFEnum && d.Type != CFMultiEnum {
			continue
		}
		v, err := decodeCFValue(value)
		if err != nil {
			return nil, err
		}
		switch x := v.(type) {
		case string:
			counts[[2]string{field, x}]++
		case []string:
			for _, g := range x {
				counts[[2]string{field, g}]++
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []FieldOption
	for _, d := range defs {
		for _, o := range d.Options {
			if n := counts[[2]string{d.GID, o.GID}]; n > 0 {
				out = append(out, FieldOption{FieldGID: d.GID, FieldName: d.Name, Option: o.Name, Color: o.Color, Tasks: n})
			}
		}
	}
	slices.SortStableFunc(out, func(a, b FieldOption) int { return strings.Compare(a.FieldName, b.FieldName) })
	return out, nil
}

// fieldCondition matches tasks whose custom field (gid or name) has value (its text, or
// one of a multi_enum's options); an empty value matches any value.
func fieldCondition(field, value string) (string, []any) {
	cond := `EXISTS (SELECT 1 FROM task_custom_fields cf JOIN custom_fields d ON d.gid = cf.field_gid
		WHERE cf.task_gid = t.gid AND (d.gid = ? OR d.name = ? COLLATE NOCASE) AND `
	if value == "" {
		return cond + "(cf.value <> 'null' OR cf.display <> ''))", []any{field, field}
	}
	like := "%, " + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(value) + ", %"
	return cond + `(cf.display = ? COLLATE NOCASE OR (', ' || cf.display || ', ') LIKE ? ESCAPE '\'))`,
		[]any{field, field, value, like}
}

// ParseFieldFilter splits "field=value" (or just "field": any value).
func ParseFieldFilter(s string) (field, value string) {
	field, value, _ = strings.Cut(s, "=")
	return strings.TrimSpace(field), strings.TrimSpace(value)
}
