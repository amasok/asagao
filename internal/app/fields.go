package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/amasok/asagao/internal/readmodel"
)

// Custom fields themselves (#37): adding an option to an enum / multi_enum field while
// setting a task's value, and adding an existing field of the workspace to a project (or
// removing it). Both are queued in the outbox; Asana may refuse them (402: the plan; 403:
// e.g. the field's options are locked), and they are then not offered again.

// FieldOptionStore is the part of the local cache that adding options needs.
// *store.Store satisfies it.
type FieldOptionStore interface {
	AddEnumOptionLocal(ctx context.Context, fieldGID, name string, now time.Time) (gid string, created bool, err error)
	SetCustomFieldLocal(ctx context.Context, taskGID, fieldGID string, value any, now time.Time) (string, error)
	TaskWorkspace(ctx context.Context, taskGID string) (string, error)
	DefaultWorkspace(ctx context.Context) (string, error)
	SyncState(ctx context.Context, key string) (string, error)
}

// ProjectFieldStore is the part of the local cache that adding a field to a project
// needs. *store.Store satisfies it.
type ProjectFieldStore interface {
	ResolveProject(ctx context.Context, arg string) (string, error)
	ProjectFields(ctx context.Context, projectGID string) ([]readmodel.ProjectFieldView, error)
	ResolveProjectField(ctx context.Context, projectGID, arg string) (readmodel.ProjectFieldView, error)
	SetProjectFieldLocal(ctx context.Context, projectGID, fieldGID string, add bool, now time.Time) error
	ProjectWorkspace(ctx context.Context, projectGID string) (string, error)
	SyncState(ctx context.Context, key string) (string, error)
}

// ErrFieldLocked is returned for a change to a custom field itself that Asana refused
// before (403).
type ErrFieldLocked struct{ Project bool }

func (e *ErrFieldLocked) Error() string {
	if e.Project {
		return "このプロジェクトのカスタムフィールドは変更できません（Asana が 403 を返しました）"
	}
	return "このカスタムフィールドには選択肢を足せません（選択肢の編集が制限されているか、権限がありません。Asana が 403 を返しました）"
}

// refused says, as an error, whether Asana refused custom field changes in workspace
// workspaceGID (402, less than PlanRecheck ago) or under lockedKey (403).
func refused(ctx context.Context, st interface {
	SyncState(ctx context.Context, key string) (string, error)
}, workspaceGID, lockedKey string, project bool, now time.Time) error {
	if err := planOffered(ctx, st, readmodel.PlanCustomFields, workspaceGID, now); err != nil {
		return err
	}
	v, err := st.SyncState(ctx, lockedKey)
	if err != nil {
		return err
	}
	if v != "" {
		return &ErrFieldLocked{Project: project}
	}
	return nil
}

// --- options ---

// FieldOptions is the use case of adding options to a field while setting a value.
type FieldOptions struct {
	Store FieldOptionStore
	Clock Clock
}

// Addable says (nil) whether options can be added to field f of task taskGID, else why not.
func (u *FieldOptions) Addable(ctx context.Context, taskGID string, f readmodel.CustomFieldView) error {
	if f.Type != readmodel.CFEnum && f.Type != readmodel.CFMultiEnum {
		return fmt.Errorf("カスタムフィールド「%s」は選択式ではありません（%s）", f.Name, f.Type)
	}
	if !f.Editable {
		return fmt.Errorf("カスタムフィールド「%s」は変更できません", f.Name)
	}
	ws, err := u.Store.TaskWorkspace(ctx, taskGID)
	if err == nil && ws == "" { // neither the task's nor its project's is known
		ws, err = u.Store.DefaultWorkspace(ctx)
	}
	if err != nil {
		return err
	}
	return refused(ctx, u.Store, ws, readmodel.KeyFieldOptionsLocked+f.GID, false, u.Clock())
}

// ParseAdding reads input as options of the enum / multi_enum field f of task taskGID (a name or gid;
// several separated by commas, or a list, for multi_enum), like ParseFieldValue, except
// that a name only matches an option exactly (ignoring case): any other name is an option
// to add (NewOptions). Nothing is written. Other types are an error.
func (u *FieldOptions) ParseAdding(ctx context.Context, taskGID string, f readmodel.CustomFieldView, input any) (FieldValue, error) {
	if err := u.Addable(ctx, taskGID, f); err != nil {
		return FieldValue{}, err
	}
	var parts []string
	switch x := input.(type) {
	case nil:
	case string:
		parts = []string{x}
		if f.Type == readmodel.CFMultiEnum {
			parts = strings.Split(x, ",")
		}
	case []string:
		parts = x
	case []any:
		for _, e := range x {
			parts = append(parts, fmt.Sprint(e))
		}
	default:
		return FieldValue{}, fmt.Errorf("カスタムフィールド「%s」に設定できない値です: %v", f.Name, input)
	}
	fv := FieldValue{Field: f}
	var gids []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if i := slices.IndexFunc(f.Options, func(o readmodel.EnumOptionView) bool {
			return o.GID == p || strings.EqualFold(o.Name, p)
		}); i >= 0 {
			if !slices.Contains(gids, f.Options[i].GID) {
				gids = append(gids, f.Options[i].GID)
			}
		} else if !slices.ContainsFunc(fv.NewOptions, func(n string) bool { return strings.EqualFold(n, p) }) {
			fv.NewOptions = append(fv.NewOptions, p)
		}
	}
	if f.Type == readmodel.CFEnum && len(gids)+len(fv.NewOptions) > 1 {
		return FieldValue{}, fmt.Errorf("カスタムフィールド「%s」には値を 1 つだけ指定してください", f.Name)
	}
	switch {
	case f.Type == readmodel.CFEnum && len(gids) == 1:
		fv.Value = gids[0]
	case f.Type == readmodel.CFMultiEnum && len(gids) > 0:
		fv.Value = gids
	}
	return fv, nil
}

// SetWithNewOption sets field f of task taskGID to an option named name, added to the
// field first unless it has one by that name (ignoring case). A multi_enum field keeps its
// other options. It returns the value as text.
func (u *FieldOptions) SetWithNewOption(ctx context.Context, taskGID string, f readmodel.CustomFieldView, name string) (string, error) {
	fv, err := u.ParseAdding(ctx, taskGID, f, name)
	if err != nil {
		return "", err
	}
	if fv.Value == nil && len(fv.NewOptions) == 0 {
		return "", errors.New("選択肢の名前が空です")
	}
	if f.Type == readmodel.CFMultiEnum {
		cur, _ := f.Value.([]string)
		add, _ := fv.Value.([]string)
		fv.Value = append(slices.Clone(cur), add...)
	}
	return u.Set(ctx, taskGID, fv)
}

// Set adds fv's new options to its field (in order) and sets field of task taskGID to its
// value with them. It returns the value as text.
func (u *FieldOptions) Set(ctx context.Context, taskGID string, fv FieldValue) (string, error) {
	return setFieldValue(ctx, u.Store, u.Clock(), taskGID, fv)
}

// setFieldValue is FieldOptions.Set with a store and a time.
func setFieldValue(ctx context.Context, st FieldOptionStore, now time.Time, taskGID string, fv FieldValue) (string, error) {
	value := fv.Value
	for _, name := range fv.NewOptions {
		gid, _, err := st.AddEnumOptionLocal(ctx, fv.Field.GID, name, now)
		if err != nil {
			return "", err
		}
		if fv.Field.Type == readmodel.CFEnum {
			value = gid
			continue
		}
		gids, _ := value.([]string)
		if !slices.Contains(gids, gid) {
			value = append(slices.Clone(gids), gid)
		}
	}
	return st.SetCustomFieldLocal(ctx, taskGID, fv.Field.GID, value, now)
}

// --- project settings ---

// ProjectFields is the use case of adding a workspace's custom field to a project, or
// removing one from it.
type ProjectFields struct {
	Store ProjectFieldStore
	Clock Clock
}

// Available reports whether adding fields to projects and options to fields can be
// offered in a workspace (the plan has it, unless Asana refused it there with a 402 less
// than readmodel.PlanRecheckAfter ago).
func (u *ProjectFields) Available(ctx context.Context, workspaceGID string) bool {
	return planOffered(ctx, u.Store, readmodel.PlanCustomFields, workspaceGID, u.Clock()) == nil
}

// Changeable says (nil) whether the fields of project projectGID can be changed, else why not.
func (u *ProjectFields) Changeable(ctx context.Context, projectGID string) error {
	ws, err := u.Store.ProjectWorkspace(ctx, projectGID)
	if err != nil {
		return err
	}
	return refused(ctx, u.Store, ws, readmodel.KeyProjectFieldsLocked+projectGID, true, u.Clock())
}

// List returns the fields of the project named (or with the gid) project, then (all) the
// other fields of its workspace, which can be added to it.
func (u *ProjectFields) List(ctx context.Context, project string, all bool) (string, []readmodel.ProjectFieldView, error) {
	gid, err := u.Store.ResolveProject(ctx, project)
	if err != nil {
		return "", nil, err
	}
	fields, err := u.Store.ProjectFields(ctx, gid)
	if err != nil {
		return "", nil, err
	}
	if !all {
		fields = slices.DeleteFunc(fields, func(f readmodel.ProjectFieldView) bool { return !f.Attached })
	}
	return gid, fields, nil
}

// Set adds (add) the custom field named (or with the gid) field to the project named (or
// with the gid) project, or removes it. It returns the field.
func (u *ProjectFields) Set(ctx context.Context, project, field string, add bool) (readmodel.ProjectFieldView, error) {
	gid, err := u.Store.ResolveProject(ctx, project)
	if err != nil {
		return readmodel.ProjectFieldView{}, err
	}
	f, err := u.Store.ResolveProjectField(ctx, gid, field)
	if err != nil {
		return readmodel.ProjectFieldView{}, err
	}
	return f, u.SetHere(ctx, gid, f.GID, add)
}

// SetHere adds (add) the field fieldGID to the project projectGID, or removes it, both
// gids the user already chose (the TUI).
func (u *ProjectFields) SetHere(ctx context.Context, projectGID, fieldGID string, add bool) error {
	if err := u.Changeable(ctx, projectGID); err != nil {
		return err
	}
	return u.Store.SetProjectFieldLocal(ctx, projectGID, fieldGID, add, u.Clock())
}
