package model

import (
	"encoding/json"
	"testing"
)

// Adversarial boundary cases for CustomField.IsCustomID, CustomIDValue, and Task.HasCustomID (#65).
func TestTaskHasCustomIDAttackEdgeCases(t *testing.T) {
	str := func(s string) *string { return &s }

	cases := []struct {
		name string
		task Task
		id   string
		want bool
	}{
		{
			name: "nil custom fields never matches",
			task: Task{GID: "t1"},
			id:   "RC-1",
			want: false,
		},
		{
			name: "empty query never matches even with empty field values",
			task: Task{GID: "t1", CustomFields: []CustomField{{
				GID: "cf1", RepresentationType: "custom_id", IDPrefix: "RC", DisplayValue: str(""), TextValue: str(""),
			}}},
			id:   "",
			want: false,
		},
		{
			name: "display_value wins over text_value when both non-empty",
			task: Task{GID: "t1", CustomFields: []CustomField{{
				GID: "cf1", RepresentationType: "custom_id", IDPrefix: "RC", DisplayValue: str("RC-2"), TextValue: str("RC-1"),
			}}},
			id:   "RC-1",
			want: false,
		},
		{
			name: "nil display_value falls back to text_value",
			task: Task{GID: "t1", CustomFields: []CustomField{{
				GID: "cf1", RepresentationType: "custom_id", TextValue: str("RC-1"),
			}}},
			id:   "RC-1",
			want: true,
		},
		{
			name: "representation_type custom_id without id_prefix still matches",
			task: Task{GID: "t1", CustomFields: []CustomField{{
				GID: "cf1", RepresentationType: "custom_id", DisplayValue: str("RC-1"),
			}}},
			id:   "RC-1",
			want: true,
		},
		{
			name: "regular enum/number/text fields with matching display_value do not match",
			task: Task{GID: "t1", CustomFields: []CustomField{
				{GID: "cf1", ResourceSubtype: "text", RepresentationType: "text", DisplayValue: str("RC-1"), TextValue: str("RC-1")},
				{GID: "cf2", ResourceSubtype: "enum", RepresentationType: "enum", DisplayValue: str("RC-1")},
				{GID: "cf3", ResourceSubtype: "text", DisplayValue: str("RC-1"), TextValue: str("RC-1")},
			}},
			id:   "RC-1",
			want: false,
		},
		{
			name: "whitespace around value does not match",
			task: Task{GID: "t1", CustomFields: []CustomField{
				{GID: "cf1", RepresentationType: "custom_id", IDPrefix: "RC", DisplayValue: str(" RC-1")},
				{GID: "cf2", RepresentationType: "custom_id", IDPrefix: "RC", DisplayValue: str("RC-1 ")},
				{GID: "cf3", RepresentationType: "custom_id", IDPrefix: "RC", DisplayValue: str("RC-1\n")},
			}},
			id:   "RC-1",
			want: false,
		},
		{
			name: "prefix and suffix lookalikes do not match",
			task: Task{GID: "t1", CustomFields: []CustomField{
				{GID: "cf1", RepresentationType: "custom_id", IDPrefix: "RC", DisplayValue: str("RC-10")},
				{GID: "cf2", RepresentationType: "custom_id", IDPrefix: "RC", DisplayValue: str("RC-100")},
				{GID: "cf3", RepresentationType: "custom_id", IDPrefix: "XRC", DisplayValue: str("XRC-1")},
				{GID: "cf4", RepresentationType: "custom_id", IDPrefix: "RC", DisplayValue: str("RC-")},
			}},
			id:   "RC-1",
			want: false,
		},
		{
			name: "case mismatch does not match",
			task: Task{GID: "t1", CustomFields: []CustomField{
				{GID: "cf1", RepresentationType: "custom_id", IDPrefix: "rc", DisplayValue: str("rc-1")},
				{GID: "cf2", RepresentationType: "custom_id", IDPrefix: "Rc", DisplayValue: str("Rc-1")},
			}},
			id:   "RC-1",
			want: false,
		},
		{
			name: "control characters in display_value do not spuriously match clean id",
			task: Task{GID: "t1", CustomFields: []CustomField{
				{GID: "cf1", RepresentationType: "custom_id", IDPrefix: "RC", DisplayValue: str("\x1b[31mRC-1\x1b[0m")},
				{GID: "cf2", RepresentationType: "custom_id", IDPrefix: "RC", DisplayValue: str("RC-1\x1b]52;c;ZXZpbA==\x07")},
			}},
			id:   "RC-1",
			want: false,
		},
		{
			name: "non-matching custom_id before matching custom_id still matches",
			task: Task{GID: "t1", CustomFields: []CustomField{
				{GID: "cf1", RepresentationType: "text", DisplayValue: str("RC-1")},
				{GID: "cf2", RepresentationType: "custom_id", IDPrefix: "RC", DisplayValue: str("RC-10")},
				{GID: "cf3", RepresentationType: "custom_id", IDPrefix: "RC", DisplayValue: str("RC-1")},
			}},
			id:   "RC-1",
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.task.HasCustomID(tc.id); got != tc.want {
				t.Fatalf("HasCustomID(%q) = %v, want %v", tc.id, got, tc.want)
			}
		})
	}

	// JSON with explicit null id_prefix and null display_value decodes cleanly without panic.
	var decoded Task
	raw := `{"gid":"t9","custom_fields":[{"gid":"cf","representation_type":"custom_id","id_prefix":null,"display_value":null,"text_value":"RC-1"}]}`
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatal(err)
	}
	if !decoded.HasCustomID("RC-1") {
		t.Fatalf("decoded task should match RC-1: %+v", decoded.CustomFields)
	}
}
