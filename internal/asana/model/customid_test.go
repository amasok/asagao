package model

import (
	"encoding/json"
	"testing"
)

func TestTaskHasCustomID(t *testing.T) {
	str := func(s string) *string { return &s }

	t.Run("matches ID custom field decoded from Asana JSON", func(t *testing.T) {
		raw := `{
			"gid": "1001",
			"name": "Fix bug",
			"custom_fields": [
				{
					"gid": "cf-text",
					"name": "Ref",
					"resource_subtype": "text",
					"representation_type": "text",
					"id_prefix": null,
					"display_value": "RC-1",
					"text_value": "RC-1"
				},
				{
					"gid": "cf-id",
					"name": "ID",
					"resource_subtype": "text",
					"representation_type": "custom_id",
					"id_prefix": "RC",
					"is_value_read_only": true,
					"display_value": "RC-1",
					"text_value": "RC-1"
				}
			]
		}`
		var task Task
		if err := json.Unmarshal([]byte(raw), &task); err != nil {
			t.Fatal(err)
		}
		if !task.HasCustomID("RC-1") {
			t.Fatalf("expected RC-1 to match: %+v", task.CustomFields)
		}
		if task.HasCustomID("rc-1") || task.HasCustomID("Rc-1") {
			t.Fatal("case mismatch should not match")
		}
		if task.HasCustomID("RC-10") || task.HasCustomID("RC-") || task.HasCustomID("") {
			t.Fatal("partial or empty ID should not match")
		}
	})

	t.Run("does not match regular text custom field with same value", func(t *testing.T) {
		task := Task{
			GID: "1002",
			CustomFields: []CustomField{{
				GID:                "cf-text",
				ResourceSubtype:    "text",
				RepresentationType: "text",
				DisplayValue:       str("RC-1"),
				TextValue:          str("RC-1"),
			}},
		}
		if task.HasCustomID("RC-1") {
			t.Fatal("regular text field matched as custom ID")
		}
	})

	t.Run("matches via id_prefix alone and falls back to text_value", func(t *testing.T) {
		task := Task{
			GID: "1003",
			CustomFields: []CustomField{{
				GID:          "cf-id",
				IDPrefix:     "ENG",
				DisplayValue: str(""),
				TextValue:    str("ENG-42"),
			}},
		}
		if !task.HasCustomID("ENG-42") {
			t.Fatal("expected ENG-42 to match via id_prefix and text_value")
		}
	})

	t.Run("multi-homed task with multiple ID custom fields", func(t *testing.T) {
		task := Task{
			GID: "1004",
			CustomFields: []CustomField{
				{GID: "cf1", RepresentationType: "custom_id", IDPrefix: "ENG", DisplayValue: str("ENG-7")},
				{GID: "cf2", RepresentationType: "custom_id", IDPrefix: "RC", DisplayValue: str("RC-9")},
			},
		}
		if !task.HasCustomID("ENG-7") || !task.HasCustomID("RC-9") {
			t.Fatal("expected both IDs on multi-homed task to match")
		}
	})

	t.Run("unset ID custom field does not match", func(t *testing.T) {
		task := Task{
			GID:          "1005",
			CustomFields: []CustomField{{GID: "cf1", RepresentationType: "custom_id", IDPrefix: "RC"}},
		}
		if task.HasCustomID("RC-1") || task.HasCustomID("") {
			t.Fatal("unset field matched")
		}
	})
}
