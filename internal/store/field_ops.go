package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/termsafe"
)

func init() {
	registerUndo(OpCreateEnumOption, undoCreateEnumOption)
	registerUndo(OpAddFieldSetting, undoFieldSettingOp)
	registerUndo(OpRemoveFieldSetting, undoFieldSettingOp)
}

// Changes to custom fields themselves (#37): adding an option to an enum / multi_enum
// field, and adding an existing field of the workspace to a project (or removing it).
//
// An option added offline gets a local gid at once, so a task's value can use it right
// away; the value waits in the outbox until Asana created the option, and the local gid is
// then replaced everywhere (like a section created offline).

const (
	OpCreateEnumOption   = outbox.KindCreateEnumOption
	OpAddFieldSetting    = outbox.KindAddFieldSetting
	OpRemoveFieldSetting = outbox.KindRemoveFieldSetting
)

// ProjectFieldView is readmodel's.
type ProjectFieldView = readmodel.ProjectFieldView

// ErrOptionsUnknown is returned when the options of an enum field were never fetched.
var ErrOptionsUnknown = errors.New("選択肢がまだ取得されていません（オンラインで同期してください）")

// --- options ---

// AddEnumOptionLocal adds an option named name to the enum or multi_enum field fieldGID
// and queues its creation. It returns the option's gid: a local one, or the gid of an
// enabled option that already has the name (exactly, or ignoring case), in which case
// nothing is queued (created is false).
func (s *Store) AddEnumOptionLocal(ctx context.Context, fieldGID, name string, now time.Time) (gid string, created bool, err error) {
	name = strings.TrimSpace(termsafe.String(name))
	if name == "" {
		return "", false, errors.New("選択肢の名前が空です")
	}
	err = s.tx(ctx, func(tx *sql.Tx) error {
		d, err := getFieldDef(ctx, tx, fieldGID)
		if err != nil {
			return err
		}
		if d.Type != CFEnum && d.Type != CFMultiEnum {
			return fmt.Errorf("カスタムフィールド「%s」は選択式ではありません（%s）", d.Name, d.Type)
		}
		if d.ReadOnly {
			return fmt.Errorf("カスタムフィールド「%s」は変更できません", d.Name)
		}
		if !d.OptionsKnown {
			return fmt.Errorf("カスタムフィールド「%s」: %w", d.Name, ErrOptionsUnknown)
		}
		if o, ok := d.optionNamed(name); ok {
			gid = o.GID
			return nil
		}
		gid, created = newLocalGID(), true
		if err := writeFieldOptions(ctx, tx, fieldGID, append(d.Options, EnumOptionView{GID: gid, Name: name, Enabled: true})); err != nil {
			return err
		}
		ws, err := fieldWorkspace(ctx, tx, fieldGID)
		if err != nil {
			return err
		}
		return savePayload(ctx, tx, &Op{Kind: OpCreateEnumOption, TaskGID: gid, State: OpPending, CreatedAt: formatTime(now)},
			outbox.CreateEnumOption{Field: fieldGID, FieldName: d.Name, Name: name, Workspace: ws})
	})
	if err != nil {
		return "", false, err
	}
	return gid, created, nil
}

// optionNamed finds an enabled option named name: exactly, else ignoring case.
func (d fieldDef) optionNamed(name string) (EnumOptionView, bool) {
	var folded []EnumOptionView
	for _, o := range d.Options {
		if !o.Enabled {
			continue
		}
		if o.Name == name {
			return o, true
		}
		if strings.EqualFold(o.Name, name) {
			folded = append(folded, o)
		}
	}
	if len(folded) == 1 {
		return folded[0], true
	}
	return EnumOptionView{}, false
}

// writeFieldOptions stores the options of a field.
func writeFieldOptions(ctx context.Context, tx *sql.Tx, fieldGID string, opts []EnumOptionView) error {
	if opts == nil {
		opts = []EnumOptionView{}
	}
	b, err := json.Marshal(opts)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE custom_fields SET enum_options = ? WHERE gid = ?", string(b), fieldGID)
	return err
}

// localOptions returns the options created offline for a field whose creation is still in
// the outbox (in any state: a failed one stays visible until it is discarded), in order.
func localOptions(ctx context.Context, tx *sql.Tx, fieldGID string) ([]EnumOptionView, error) {
	rows, err := tx.QueryContext(ctx, `SELECT task_gid, COALESCE(json_extract(payload, '$.name'), '') FROM outbox
		WHERE kind = ? AND json_extract(payload, '$.field') = ? ORDER BY id`, OpCreateEnumOption, fieldGID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EnumOptionView
	for rows.Next() {
		var o EnumOptionView
		if err := rows.Scan(&o.GID, &o.Name); err != nil {
			return nil, err
		}
		o.Enabled = true
		out = append(out, o)
	}
	return out, rows.Err()
}

// EnumOptionCreateQueued reports whether the creation of the local option gid is still in
// the outbox (pending, being verified or failed): the values that use it wait for it.
func (s *Store) EnumOptionCreateQueued(ctx context.Context, gid string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM outbox WHERE kind = ? AND task_gid = ?", OpCreateEnumOption, gid).Scan(&n)
	return n > 0, err
}

// RealEnumOptionGID returns the Asana gid of an option created offline once it was
// created (or gid itself).
func (s *Store) RealEnumOptionGID(ctx context.Context, gid string) (string, error) {
	if !IsLocalGID(gid) {
		return gid, nil
	}
	v, err := s.SyncState(ctx, resultPrefixEnumOption+gid)
	if err != nil || v == "" {
		return gid, err
	}
	return v, nil
}

// realOptions replaces in a value (stored form) the local gids of options that Asana
// created by their Asana gids.
func realOptions(ctx context.Context, tx *sql.Tx, v any) (any, error) {
	var err error
	out := mapOption(v, func(g string) string {
		if err != nil || !IsLocalGID(g) {
			return g
		}
		var real string
		switch e := tx.QueryRowContext(ctx, "SELECT value FROM sync_state WHERE key = ?", resultPrefixEnumOption+g).Scan(&real); {
		case e == nil && real != "":
			return real
		case e != nil && !errors.Is(e, sql.ErrNoRows):
			err = e
		}
		return g
	})
	return out, err
}

// CompleteCreateEnumOption records that Asana created the option of entry id with gid
// real (and name, as Asana keeps it): the local gid is replaced in the field's options,
// the tasks' values and the queued changes, and the entry is removed.
func (s *Store) CompleteCreateEnumOption(ctx context.Context, id int64, local string, real model.EnumOption) error {
	return s.completeOp(ctx, id, func(tx *sql.Tx) error { return replaceLocalOption(ctx, tx, id, local, real) })
}

func replaceLocalOption(ctx context.Context, tx *sql.Tx, id int64, local string, real model.EnumOption) error {
	op, err := getOpTx(ctx, tx, id)
	if err != nil {
		return err
	}
	field, _ := op.Payload["field"].(string)
	if d, err := getFieldDef(ctx, tx, field); err == nil && d.OptionsKnown {
		var opts []EnumOptionView
		for _, o := range d.Options {
			switch o.GID {
			case real.GID: // a sync already fetched it
				continue
			case local:
				o.GID, o.Color = real.GID, real.Color
				if real.Name != "" {
					o.Name = real.Name
				}
			}
			opts = append(opts, o)
		}
		if err := writeFieldOptions(ctx, tx, field, opts); err != nil {
			return err
		}
	} else if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	swap := func(v any) any {
		return mapOption(v, func(g string) string {
			if g == local {
				return real.GID
			}
			return g
		})
	}
	if _, err := rewriteFieldValues(ctx, tx, local, swap); err != nil {
		return err
	}
	// Anything else that names it (a task created offline with the value): local gids are
	// random and only ever stored as whole JSON strings.
	quoted := func(g string) string { return `"` + g + `"` }
	if _, err := tx.ExecContext(ctx, `UPDATE outbox SET payload = replace(payload, ?1, ?2), base = replace(base, ?1, ?2)
		WHERE id <> ?3 AND (instr(payload, ?1) > 0 OR instr(base, ?1) > 0)`, quoted(local), quoted(real.GID), id); err != nil {
		return err
	}
	return recordResult(ctx, tx, resultPrefixEnumOption, local, real.GID)
}

// mapOption applies f to the option gids of a value in the stored form (multi_enum: sorted again).
func mapOption(v any, f func(string) string) any {
	switch x := v.(type) {
	case string:
		return f(x)
	case []string:
		out := make([]string, 0, len(x))
		for _, g := range x {
			if g = f(g); g != "" {
				out = append(out, g)
			}
		}
		return gidList(out)
	}
	return v
}

// rewriteFieldValues rewrites with f the enum values that hold the local option gid: the
// tasks' values and the queued set_custom_field changes (payload and base).
// It returns the changes it rewrote.
func rewriteFieldValues(ctx context.Context, tx *sql.Tx, local string, f func(any) any) ([]int64, error) {
	like := "%" + `"` + local + `"` + "%"
	rows, err := tx.QueryContext(ctx, "SELECT task_gid, field_gid, value FROM task_custom_fields WHERE value LIKE ?", like)
	if err != nil {
		return nil, err
	}
	type row struct{ task, field, value string }
	var found []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.task, &r.field, &r.value); err != nil {
			rows.Close()
			return nil, err
		}
		found = append(found, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, r := range found {
		v, err := decodeCFValue(r.value)
		if err != nil {
			return nil, err
		}
		b, err := json.Marshal(normalizeCFValue(f(v)))
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE task_custom_fields SET value = ? WHERE task_gid = ? AND field_gid = ?", string(b), r.task, r.field); err != nil {
			return nil, err
		}
	}
	ids, err := scanIDsQuery(ctx, tx, `SELECT id FROM outbox WHERE kind = ? AND (payload LIKE ? OR base LIKE ? OR conflict LIKE ?)`,
		OpSetCustomField, like, like, like)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		op, err := getOpTx(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if err := outbox.EditPayload(op, func(p *outbox.SetCustomField) { p.Value = f(normalizeCFValue(p.Value)) }); err != nil {
			return nil, err
		}
		for _, m := range []map[string]any{op.Base, op.Conflict} {
			if v, ok := m["value"]; ok {
				m["value"] = f(normalizeCFValue(v))
			}
		}
		if err := saveOp(ctx, tx, op); err != nil {
			return nil, err
		}
	}
	return ids, nil
}

func scanIDsQuery(ctx context.Context, tx *sql.Tx, q string, args ...any) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return scanIDs(rows)
}

// undoCreateEnumOption drops an option created offline whose creation is discarded: it
// goes from the field's options, and from every value that uses it (the changes that only
// set it are dropped with it; a task created offline is created without it).
func undoCreateEnumOption(ctx context.Context, tx *sql.Tx, op *Op, p outbox.CreateEnumOption) error {
	local := op.TaskGID
	field := p.Field
	d, err := getFieldDef(ctx, tx, field)
	known := err == nil
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	if known && d.OptionsKnown {
		opts := slices.DeleteFunc(slices.Clone(d.Options), func(o EnumOptionView) bool { return o.GID == local })
		if err := writeFieldOptions(ctx, tx, field, opts); err != nil {
			return err
		}
		d.Options = opts
	}
	drop := func(v any) any {
		v = mapOption(v, func(g string) string {
			if g == local {
				return ""
			}
			return g
		})
		if v == "" {
			return nil
		}
		return v
	}
	ids, err := rewriteFieldValues(ctx, tx, local, drop)
	if err != nil {
		return err
	}
	// The changes that used it: dropped if they set only it or nothing is left to send,
	// else sent without it.
	for _, id := range ids {
		o, err := getOpTx(ctx, tx, id)
		if err != nil {
			return err
		}
		// Nothing left (it set just that option), or back to Asana's value: nothing to send.
		if v := normalizeCFValue(o.Payload["value"]); v == nil || sameValue(normalizeCFValue(o.Base["value"]), v) {
			if err := applyCustomFieldOp(ctx, tx, *o, true); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM outbox WHERE id = ?", id); err != nil {
				return err
			}
			continue
		}
		if known {
			if err := outbox.EditPayload(o, func(p *outbox.SetCustomField) {
				p.Display = valueDisplay(ctx, tx, d, normalizeCFValue(p.Value))
			}); err != nil {
				return err
			}
			if err := saveOp(ctx, tx, o); err != nil {
				return err
			}
		}
		if err := applyCustomFieldOp(ctx, tx, *o, false); err != nil {
			return err
		}
	}
	// A task created offline with the value: created without it.
	ids, err = scanIDsQuery(ctx, tx, `SELECT id FROM outbox WHERE kind = ? AND instr(payload, ?) > 0`, OpCreateTask, `"`+local+`"`)
	if err != nil {
		return err
	}
	for _, id := range ids {
		o, err := getOpTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := outbox.EditPayload(o, func(p *outbox.CreateTask) {
			for k, v := range p.CustomFields {
				if nv := drop(normalizeCFValue(v)); nv == nil {
					delete(p.CustomFields, k)
				} else {
					p.CustomFields[k] = nv
				}
			}
		}); err != nil {
			return err
		}
		if err := saveOp(ctx, tx, o); err != nil {
			return err
		}
	}
	return nil
}

// --- project settings ---

// SetProjectFieldLocal adds the custom field fieldGID to the project projectGID (add), or
// removes it, and queues the change. Undoing a change not sent yet drops it from the queue.
func (s *Store) SetProjectFieldLocal(ctx context.Context, projectGID, fieldGID string, add bool, now time.Time) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		var ws string
		if err := tx.QueryRowContext(ctx, "SELECT workspace_gid FROM projects WHERE gid = ?", projectGID).Scan(&ws); err != nil {
			return notFound(err, "project "+projectGID)
		}
		d, err := getFieldDef(ctx, tx, fieldGID)
		if err != nil {
			return err
		}
		var position int
		var important bool
		err = tx.QueryRowContext(ctx, "SELECT position, is_important FROM project_custom_fields WHERE project_gid = ? AND field_gid = ?",
			projectGID, fieldGID).Scan(&position, &important)
		attached := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if attached == add {
			if add {
				return fmt.Errorf("カスタムフィールド「%s」はすでにこのプロジェクトにあります", d.Name)
			}
			return fmt.Errorf("カスタムフィールド「%s」はこのプロジェクトにありません", d.Name)
		}
		// The opposite change, not sent yet: drop it instead (back to what Asana has).
		undo, other := OpAddFieldSetting, OpRemoveFieldSetting
		if add {
			undo, other = OpRemoveFieldSetting, OpAddFieldSetting
		}
		var id int64
		err = tx.QueryRowContext(ctx, `SELECT id FROM outbox WHERE kind = ? AND task_gid = ? AND json_extract(payload, '$.field') = ?
			AND state IN ('pending', 'failed') ORDER BY id DESC LIMIT 1`, undo, projectGID, fieldGID).Scan(&id)
		if err == nil {
			op, err := getOpTx(ctx, tx, id)
			if err != nil {
				return err
			}
			return discardOpTx(ctx, tx, op)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		op := &Op{Kind: other, TaskGID: projectGID, State: OpPending, CreatedAt: formatTime(now)}
		if add {
			err = attachField(ctx, tx, projectGID, fieldGID, -1, false)
		} else {
			op.Base = map[string]any{"position": position, "is_important": important}
			_, err = tx.ExecContext(ctx, "DELETE FROM project_custom_fields WHERE project_gid = ? AND field_gid = ?", projectGID, fieldGID)
		}
		if err != nil {
			return err
		}
		return savePayload(ctx, tx, op, outbox.FieldSetting{Field: fieldGID, FieldName: d.Name, Workspace: ws})
	})
}

// attachField adds a field to a project at position (-1: last).
func attachField(ctx context.Context, tx *sql.Tx, projectGID, fieldGID string, position int, important bool) error {
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO project_custom_fields(project_gid, field_gid, position, is_important)
		VALUES(?1, ?2, CASE WHEN ?3 >= 0 THEN ?3 ELSE COALESCE((SELECT max(position) + 1 FROM project_custom_fields WHERE project_gid = ?1), 0) END, ?4)`,
		projectGID, fieldGID, position, important)
	return err
}

// undoFieldSettingOp puts a project's fields back as they were before op.
func undoFieldSettingOp(ctx context.Context, tx *sql.Tx, op *Op, p outbox.FieldSetting) error {
	field := p.Field
	if op.Kind == OpAddFieldSetting {
		_, err := tx.ExecContext(ctx, "DELETE FROM project_custom_fields WHERE project_gid = ? AND field_gid = ?", op.TaskGID, field)
		return err
	}
	position := -1
	if p, ok := op.Base["position"].(float64); ok {
		position = int(p)
	}
	important, _ := op.Base["is_important"].(bool)
	return attachField(ctx, tx, op.TaskGID, field, position, important)
}

// reapplyFieldSettingOps keeps the unsent changes to a project's fields after a sync
// replaced them.
func reapplyFieldSettingOps(ctx context.Context, tx *sql.Tx, projectGID string) error {
	ids, err := scanIDsQuery(ctx, tx, `SELECT id FROM outbox WHERE kind IN (?, ?) AND task_gid = ? AND state <> 'failed' ORDER BY id`,
		OpAddFieldSetting, OpRemoveFieldSetting, projectGID)
	if err != nil {
		return err
	}
	for _, id := range ids {
		op, err := getOpTx(ctx, tx, id)
		if err != nil {
			return err
		}
		field, _ := op.Payload["field"].(string)
		if op.Kind == OpAddFieldSetting {
			err = attachField(ctx, tx, projectGID, field, -1, false)
		} else {
			_, err = tx.ExecContext(ctx, "DELETE FROM project_custom_fields WHERE project_gid = ? AND field_gid = ?", projectGID, field)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// FieldSettingQueued reports whether adding the field fieldGID to a project of task
// taskGID is still in the outbox (in any state): setting its value waits for it.
func (s *Store) FieldSettingQueued(ctx context.Context, taskGID, fieldGID string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM outbox WHERE kind = ? AND json_extract(payload, '$.field') = ?
		AND task_gid IN (SELECT project_gid FROM task_memberships WHERE task_gid = ?)`, OpAddFieldSetting, fieldGID, taskGID).Scan(&n)
	return n > 0, err
}

// fieldWorkspace returns the workspace a field belongs to ("" if not known): from the
// workspace's list, else from a project that has it.
func fieldWorkspace(ctx context.Context, q dbQuerier, fieldGID string) (string, error) {
	var ws string
	err := q.QueryRowContext(ctx, `SELECT COALESCE((SELECT workspace_gid FROM workspace_custom_fields WHERE field_gid = ?1 LIMIT 1),
		(SELECT p.workspace_gid FROM project_custom_fields pf JOIN projects p ON p.gid = pf.project_gid WHERE pf.field_gid = ?1 LIMIT 1), '')`,
		fieldGID).Scan(&ws)
	return ws, err
}

// --- the workspace's fields ---

// ReplaceWorkspaceCustomFields stores the custom fields of a workspace (with their options).
func (s *Store) ReplaceWorkspaceCustomFields(ctx context.Context, workspaceGID string, fields []model.CustomField) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM workspace_custom_fields WHERE workspace_gid = ?", workspaceGID); err != nil {
			return err
		}
		for i, f := range fields {
			if f.GID == "" {
				continue
			}
			if f.EnumOptions == nil && (f.Subtype() == CFEnum || f.Subtype() == CFMultiEnum) {
				f.EnumOptions = []model.EnumOption{} // the list has every option: none is none
			}
			if err := upsertFieldDef(ctx, tx, f); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO workspace_custom_fields(workspace_gid, field_gid, position)
				VALUES(?, ?, ?)`, workspaceGID, f.GID, i); err != nil {
				return err
			}
		}
		return nil
	})
}

// ProjectFields returns the custom fields of a project (attached) and then the other
// fields of its workspace that can be added to it, by name.
func (s *Store) ProjectFields(ctx context.Context, projectGID string) ([]ProjectFieldView, error) {
	var workspace string
	if err := s.db.QueryRowContext(ctx, "SELECT workspace_gid FROM projects WHERE gid = ?", projectGID).Scan(&workspace); err != nil {
		return nil, notFound(err, "project "+projectGID)
	}
	defs, err := loadFieldDefs(ctx, s.db)
	if err != nil {
		return nil, err
	}
	pending := map[string]bool{}
	rows, err := s.db.QueryContext(ctx, `SELECT COALESCE(json_extract(payload, '$.field'), '') FROM outbox WHERE kind IN (?, ?) AND task_gid = ?`,
		OpAddFieldSetting, OpRemoveFieldSetting, projectGID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			rows.Close()
			return nil, err
		}
		pending[g] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []ProjectFieldView
	seen := map[string]bool{}
	rows, err = s.db.QueryContext(ctx, `SELECT field_gid, is_important, 1 AS attached, position FROM project_custom_fields WHERE project_gid = ?1
		UNION ALL SELECT field_gid, 0, 0, position FROM workspace_custom_fields WHERE workspace_gid = ?2
		ORDER BY attached DESC, position, field_gid`, projectGID, workspace)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var g string
		var important, attached bool
		var position int
		if err := rows.Scan(&g, &important, &attached, &position); err != nil {
			return nil, err
		}
		d, ok := defs[g]
		if !ok || seen[g] {
			continue
		}
		seen[g] = true
		out = append(out, ProjectFieldView{GID: g, Name: d.Name, Type: d.Type, Attached: attached, Important: important, Pending: pending[g]})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Attached ones in the project's order, then the others by name.
	slices.SortStableFunc(out, func(a, b ProjectFieldView) int {
		if a.Attached != b.Attached {
			if a.Attached {
				return -1
			}
			return 1
		}
		if a.Attached {
			return 0
		}
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	return out, nil
}

// ResolveProjectField finds a custom field of a project, or of its workspace, by gid or
// name (exact, ignoring case, or a unique part of it).
func (s *Store) ResolveProjectField(ctx context.Context, projectGID, arg string) (ProjectFieldView, error) {
	fields, err := s.ProjectFields(ctx, projectGID)
	if err != nil {
		return ProjectFieldView{}, err
	}
	arg = strings.TrimSpace(arg)
	var partial []ProjectFieldView
	for _, f := range fields {
		if f.GID == arg || strings.EqualFold(f.Name, arg) {
			return f, nil
		}
		if arg != "" && strings.Contains(strings.ToLower(f.Name), strings.ToLower(arg)) {
			partial = append(partial, f)
		}
	}
	switch len(partial) {
	case 1:
		return partial[0], nil
	case 0:
		if len(fields) == 0 {
			return ProjectFieldView{}, fmt.Errorf("カスタムフィールド %q: このワークスペースのカスタムフィールドがまだ取得されていません（オンラインで同期してください）: %w", arg, ErrNotFound)
		}
		return ProjectFieldView{}, fmt.Errorf("カスタムフィールド %q: %w", arg, ErrNotFound)
	}
	var names []string
	for _, f := range partial {
		names = append(names, f.Name)
	}
	return ProjectFieldView{}, fmt.Errorf("カスタムフィールド %q matches %s: %w", arg, strings.Join(names, ", "), ErrAmbiguous)
}
