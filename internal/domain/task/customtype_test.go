package task

import (
	"strings"
	"testing"
)

func TestCheckCustomType(t *testing.T) {
	statusOf := func(s string) string { return map[string]string{"a1": "A", "a2": "A", "b1": "B"}[s] }
	for _, tc := range []struct {
		name                 string
		subtype, typ, status string
		changes              map[string]any
		want                 CustomTypeChange
		wantErr              string
	}{
		{"plain task, no type fields", "default_task", "", "", map[string]any{"name": "x"}, CustomTypeChange{Subtype: "default_task"}, ""},
		{"type on a plain task", "default_task", "", "", map[string]any{"custom_type": "A"}, CustomTypeChange{}, "カスタムタイプのタスクではない"},
		{"status on a plain task", "milestone", "", "", map[string]any{"custom_type_status_option": "a1"}, CustomTypeChange{}, "カスタムタイプのタスクではない"},
		{"custom without a type", "default_task", "", "", map[string]any{"resource_subtype": "custom"}, CustomTypeChange{}, "カスタムタイプを指定"},
		{"set a type", "default_task", "", "", map[string]any{"resource_subtype": "custom", "custom_type": "A"}, CustomTypeChange{Subtype: "custom", Type: "A"}, ""},
		{"set a type and its status", "default_task", "", "", map[string]any{"resource_subtype": "custom", "custom_type": "A", "custom_type_status_option": "a2"},
			CustomTypeChange{Subtype: "custom", Type: "A", Status: "a2"}, ""},
		{"a status of another type", "custom", "A", "a1", map[string]any{"custom_type_status_option": "b1"}, CustomTypeChange{}, "カスタムタイプのものではありません"},
		{"an unknown status", "custom", "A", "a1", map[string]any{"custom_type_status_option": "zz"}, CustomTypeChange{}, "カスタムタイプのものではありません"},
		{"a new type drops the old status", "custom", "A", "a1", map[string]any{"custom_type": "B"}, CustomTypeChange{Subtype: "custom", Type: "B"}, ""},
		{"the same type keeps it", "custom", "A", "a1", map[string]any{"custom_type": "A"}, CustomTypeChange{Subtype: "custom", Type: "A", Status: "a1"}, ""},
		{"a status of the new type", "custom", "A", "a1", map[string]any{"custom_type": "B", "custom_type_status_option": "b1"}, CustomTypeChange{Subtype: "custom", Type: "B", Status: "b1"}, ""},
		{"the old type's status with a new type", "custom", "A", "a1", map[string]any{"custom_type": "B", "custom_type_status_option": "a2"}, CustomTypeChange{}, "カスタムタイプのものではありません"},
		{"leaving custom", "custom", "A", "a1", map[string]any{"resource_subtype": "approval"}, CustomTypeChange{Subtype: "approval"}, ""},
		{"leaving custom with a status", "custom", "A", "a1", map[string]any{"resource_subtype": "approval", "custom_type_status_option": "a2"}, CustomTypeChange{}, "カスタムタイプのタスクではない"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CheckCustomType(tc.subtype, tc.typ, tc.status, tc.changes, statusOf)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("= %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}

func TestValidateCustomTypeFields(t *testing.T) {
	if err := ValidateFields(map[string]any{"resource_subtype": "custom", "custom_type": "1", "custom_type_status_option": "2"}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []map[string]any{{"custom_type": ""}, {"custom_type_status_option": " "}, {"custom_type": 1}} {
		if err := ValidateFields(bad); err == nil {
			t.Errorf("%v was accepted", bad)
		}
	}
	if !TouchesCustomType(map[string]any{"resource_subtype": "custom"}) || TouchesCustomType(map[string]any{"resource_subtype": "milestone"}) ||
		!TouchesCustomType(map[string]any{"custom_type_status_option": "x"}) {
		t.Error("TouchesCustomType")
	}
	if SubtypeLabel("custom") != "カスタムタイプ" {
		t.Error(SubtypeLabel("custom"))
	}
	if _, err := ParseSubtype("custom"); err == nil {
		t.Error(`ParseSubtype("custom") is set with a type, not by itself`)
	}
}
