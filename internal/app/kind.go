package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/amasok/asagao/internal/domain/task"
	"github.com/amasok/asagao/internal/readmodel"
)

// Custom task types (#59): a task's type is one of task.Subtypes, or a custom type that
// its projects offer, with a status of that type.

// resolveKind reads the type fields of in into u: Subtype (one of task.Subtypes, or with a
// task, a custom type's name or gid), CustomType and CustomTypeStatus (a task is needed).
func (t *Tasks) resolveKind(ctx context.Context, gid string, in UpdateTaskInput, u *TaskUpdate) error {
	var kind *readmodel.TaskKind
	load := func(field string) error {
		if kind != nil {
			return nil
		}
		if gid == "" {
			return &FieldError{Field: field, Err: errors.New("カスタムタイプは、タスクを指定して設定してください")}
		}
		k, err := t.Store.TaskKind(ctx, gid)
		if err != nil {
			return err
		}
		kind = &k
		return nil
	}
	setType := func(c readmodel.CustomTypeView) {
		custom := task.SubtypeCustom
		u.Subtype, u.CustomType, u.CustomTypeName = &custom, &c.GID, c.Name
	}
	if in.Subtype != nil {
		v, err := task.ParseSubtype(*in.Subtype)
		if err != nil && gid != "" {
			if lerr := load("resource_subtype"); lerr != nil {
				return lerr
			}
			if c, ok := findCustomType(kind.Choices, *in.Subtype); ok {
				setType(c)
				err = nil
			} else if len(kind.Choices) > 0 {
				err = fmt.Errorf("%w（カスタムタイプ: %s）", err, customTypeNames(kind.Choices))
			}
		} else if err == nil {
			u.Subtype = &v
		}
		if err != nil {
			return &FieldError{Field: "resource_subtype", Err: err}
		}
	}
	if in.CustomType != nil {
		if err := load("custom_type"); err != nil {
			return err
		}
		c, ok := findCustomType(kind.Choices, *in.CustomType)
		if !ok {
			return &FieldError{Field: "custom_type", Err: noCustomType(*in.CustomType, kind.Choices)}
		}
		if u.Subtype != nil && *u.Subtype != task.SubtypeCustom {
			return &FieldError{Field: "custom_type", Err: fmt.Errorf("種類「%s」とカスタムタイプは同時に指定できません", task.SubtypeLabel(*u.Subtype))}
		}
		if u.CustomType != nil && *u.CustomType != c.GID {
			return &FieldError{Field: "custom_type", Err: fmt.Errorf("種類に「%s」、カスタムタイプに「%s」と、違う種類が指定されています", u.CustomTypeName, c.Name)}
		}
		setType(c)
	}
	if in.CustomTypeStatus != nil {
		if err := load("custom_type_status_option"); err != nil {
			return err
		}
		typ := ""
		if u.CustomType != nil {
			typ = *u.CustomType
		} else if (u.Subtype == nil || *u.Subtype == task.SubtypeCustom) && kind.Subtype == task.SubtypeCustom && kind.Type != nil {
			typ = kind.Type.GID
		}
		if typ == "" {
			return &FieldError{Field: "custom_type_status_option", Err: errors.New("カスタムタイプのタスクではないため、ステータスは設定できません（先に種類をカスタムタイプにしてください）")}
		}
		var statuses []readmodel.CustomTypeStatusView
		for _, c := range kind.Choices {
			if c.GID == typ {
				statuses = c.Statuses
			}
		}
		st, ok := findCustomStatus(statuses, *in.CustomTypeStatus)
		if !ok {
			return &FieldError{Field: "custom_type_status_option", Err: noCustomStatus(*in.CustomTypeStatus, statuses)}
		}
		u.CustomTypeStatus, u.CustomTypeStatusName = &st.GID, st.Name
	}
	return nil
}

// findCustomType finds a type by gid, or by name (exactly, then ignoring case).
func findCustomType(types []readmodel.CustomTypeView, arg string) (readmodel.CustomTypeView, bool) {
	arg = strings.TrimSpace(arg)
	for _, exact := range []bool{true, false} {
		for _, c := range types {
			if c.GID == arg || (exact && c.Name == arg) || (!exact && strings.EqualFold(c.Name, arg)) {
				return c, arg != ""
			}
		}
	}
	return readmodel.CustomTypeView{}, false
}

func findCustomStatus(statuses []readmodel.CustomTypeStatusView, arg string) (readmodel.CustomTypeStatusView, bool) {
	arg = strings.TrimSpace(arg)
	for _, exact := range []bool{true, false} {
		for _, s := range statuses {
			if s.GID == arg || (exact && s.Name == arg) || (!exact && strings.EqualFold(s.Name, arg)) {
				return s, arg != ""
			}
		}
	}
	return readmodel.CustomTypeStatusView{}, false
}

func customTypeNames(types []readmodel.CustomTypeView) string {
	var names []string
	for _, c := range types {
		names = append(names, c.Name)
	}
	return strings.Join(names, " / ")
}

func noCustomType(arg string, types []readmodel.CustomTypeView) error {
	if len(types) == 0 {
		return fmt.Errorf("カスタムタイプ %q が見つかりません（このタスクのプロジェクトにはカスタムタイプがありません。同期すると取得します）", arg)
	}
	return fmt.Errorf("カスタムタイプ %q が見つかりません。%s のどれかを指定してください", arg, customTypeNames(types))
}

func noCustomStatus(arg string, statuses []readmodel.CustomTypeStatusView) error {
	if len(statuses) == 0 {
		return fmt.Errorf("ステータス %q が見つかりません（この種類のステータスがまだ取得されていません。同期すると取得します）", arg)
	}
	var names []string
	for _, s := range statuses {
		names = append(names, s.Name)
	}
	return fmt.Errorf("ステータス %q はこの種類にありません。%s のどれかを指定してください", arg, strings.Join(names, " / "))
}
