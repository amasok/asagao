package readmodel

import (
	"reflect"
	"slices"
	"testing"

	"github.com/amasok/asagao/internal/asana/model"
)

// refGIDList / refNormalize / refAPIValue are copies of the store's implementation on
// main (before e1); the moved functions must give the same results.
func refGIDList(gids []string) any {
	if len(gids) == 0 {
		return nil
	}
	gids = slices.Clone(gids)
	slices.Sort(gids)
	return slices.Compact(gids)
}

func refNormalize(v any) any {
	switch x := v.(type) {
	case string:
		if x == "" {
			return nil
		}
	case []string:
		return refGIDList(x)
	case []any:
		var gids []string
		for _, e := range x {
			if s, ok := e.(string); ok {
				gids = append(gids, s)
			}
		}
		return refGIDList(gids)
	case int:
		return float64(x)
	}
	return v
}

func refAPIValue(fieldType string, v any) any {
	v = refNormalize(v)
	if s, ok := v.(string); ok && fieldType == "date" {
		return map[string]any{"date": s}
	}
	// Changed since (#1, checked on Asana): an empty people / multi_enum field goes as [].
	if v == nil && (fieldType == "people" || fieldType == "multi_enum") {
		return []string{}
	}
	return v
}

func TestMovedValuesMatchOldStore(t *testing.T) {
	vals := []any{nil, "", "x", "2024-01-02", 3, 2.5, int64(7), true,
		[]string{}, []string{"b", "a", "b"}, []string(nil),
		[]any{}, []any{"b", 1, "a", nil, "a"}, []any{1, 2}, map[string]any{"a": 1}}
	for _, ft := range []string{"text", "number", "enum", "multi_enum", "date", "people", ""} {
		for _, v := range vals {
			if got, want := NormalizeCustomFieldValue(v), refNormalize(v); !reflect.DeepEqual(got, want) {
				t.Errorf("Normalize(%#v) = %#v, want %#v", v, got, want)
			}
			if got, want := CustomFieldAPIValue(ft, v), refAPIValue(ft, v); !reflect.DeepEqual(got, want) {
				t.Errorf("APIValue(%q,%#v) = %#v, want %#v", ft, v, got, want)
			}
		}
	}
	if got := CustomFieldAPIValue("date", "2024-01-02"); !reflect.DeepEqual(got, map[string]any{"date": "2024-01-02"}) {
		t.Errorf("date = %#v", got)
	}
	if got := CustomFieldAPIValue("text", "2024-01-02"); got != "2024-01-02" {
		t.Errorf("text = %#v", got)
	}
	if got := CustomFieldAPIValue("date", ""); got != nil {
		t.Errorf("empty date = %#v", got)
	}
	// Asana refuses null for people ("people_value: Not an array"): [] clears it.
	for _, ft := range []string{"people", "multi_enum"} {
		if got := CustomFieldAPIValue(ft, nil); !reflect.DeepEqual(got, []string{}) {
			t.Errorf("empty %s = %#v", ft, got)
		}
	}
	if got := CustomFieldAPIValue("enum", nil); got != nil {
		t.Errorf("empty enum = %#v", got)
	}
	in := []string{"b", "a"}
	GIDList(in)
	if in[0] != "b" {
		t.Error("GIDList sorted its input")
	}
}

func TestTaskFieldValuesAttack(t *testing.T) {
	tk := &model.Task{Name: "n", Notes: "o", Completed: true, Liked: true, DueOn: "2024-01-01", ApprovalStatus: "approved", ResourceSubtype: "approval", Assignee: &model.Ref{GID: "9"}}
	v := TaskFieldValues(tk)
	if v["approval_status"] != "approved" || v["assignee"] != "9" || v["completed"] != true || v["due_on"] != "2024-01-01" || len(v) != 11 || v["custom_type"] != "" {
		t.Errorf("%v", v)
	}
	tk.Partial, tk.Assignee = true, nil
	v = TaskFieldValues(tk)
	if _, ok := v["approval_status"]; ok || v["assignee"] != "" || len(v) != 8 {
		t.Errorf("partial: %v", v)
	}
}

func TestCustomFieldValueAndServer(t *testing.T) {
	s := func(x string) *string { return &x }
	f := func(x float64) *float64 { return &x }
	for _, tc := range []struct {
		f    model.CustomField
		want any
	}{
		{model.CustomField{Type: "text", TextValue: s("")}, nil},
		{model.CustomField{Type: "text", TextValue: s("a")}, "a"},
		{model.CustomField{Type: "number", NumberValue: f(0)}, float64(0)},
		{model.CustomField{Type: "number"}, nil},
		{model.CustomField{Type: "enum", EnumValue: &model.EnumOption{GID: ""}}, nil},
		{model.CustomField{Type: "enum", EnumValue: &model.EnumOption{GID: "e"}}, "e"},
		{model.CustomField{Type: "multi_enum", MultiEnumValues: []model.EnumOption{{GID: "b"}, {GID: "a"}, {GID: "b"}}}, []string{"a", "b"}},
		{model.CustomField{Type: "multi_enum"}, nil},
		{model.CustomField{Type: "people", PeopleValue: []model.Ref{{GID: "2"}, {GID: "1"}}}, []string{"1", "2"}},
		{model.CustomField{Type: "date", DateValue: &model.DateValue{Date: "2024-02-03"}}, "2024-02-03"},
		{model.CustomField{Type: "date", DateValue: &model.DateValue{}}, nil},
		{model.CustomField{Type: "unknown"}, nil},
	} {
		if got := CustomFieldValue(tc.f); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: %#v, want %#v", tc.f.Type, got, tc.want)
		}
	}
	tk := &model.Task{CustomFields: []model.CustomField{{GID: "1", Type: "text", TextValue: s("a"), DisplayValue: s("disp")}, {GID: "2", Type: "number"}}}
	if v, d, ok := ServerCustomField(tk, "1"); v != "a" || d != "disp" || !ok {
		t.Errorf("%v %v %v", v, d, ok)
	}
	if v, d, ok := ServerCustomField(tk, "2"); v != nil || d != "" || !ok {
		t.Errorf("%v %v %v", v, d, ok)
	}
	if _, _, ok := ServerCustomField(tk, "3"); ok {
		t.Error("found")
	}
}

func TestAmbiguousMessageText(t *testing.T) {
	const want = "Asana に届いたか自動で確かめられませんでした（同じ内容のものが複数あります）。Asana で確認して、届いていなければ再送、届いていれば破棄してください"
	if AmbiguousMessage != want {
		t.Errorf("%q", AmbiguousMessage)
	}
}
