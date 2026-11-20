package model

// Types of custom fields that Asana uses and asagao shows and edits.
const (
	CFText      = "text"
	CFNumber    = "number"
	CFEnum      = "enum"
	CFMultiEnum = "multi_enum"
	CFDate      = "date"
	CFPeople    = "people"
)

// EnumOption is a choice of an enum / multi_enum custom field.
type EnumOption struct {
	GID     string `json:"gid"`
	Name    string `json:"name"`
	Color   string `json:"color,omitempty"`
	Enabled *bool  `json:"enabled,omitempty"`
}

// DateValue is the value of a date custom field.
type DateValue struct {
	Date     string  `json:"date"`
	DateTime *string `json:"date_time"`
}

// CustomField is a custom field as it appears on a task (with its value) or in a
// project's settings (with its enum options).
type CustomField struct {
	GID                string       `json:"gid"`
	Name               string       `json:"name"`
	ResourceSubtype    string       `json:"resource_subtype"`    // text, number, enum, multi_enum, date, people, ...
	RepresentationType string       `json:"representation_type"` // text, number, enum, ..., custom_id
	IDPrefix           string       `json:"id_prefix"`           // prefix of an ID custom field (e.g. "RC")
	Type               string       `json:"type"`                // deprecated alias of resource_subtype
	Precision          int          `json:"precision"`
	Enabled            *bool        `json:"enabled"`
	IsFormulaField     bool         `json:"is_formula_field"`
	IsValueReadOnly    bool         `json:"is_value_read_only"`
	EnumOptions        []EnumOption `json:"enum_options"`
	EnumValue          *EnumOption  `json:"enum_value"`
	MultiEnumValues    []EnumOption `json:"multi_enum_values"`
	NumberValue        *float64     `json:"number_value"`
	TextValue          *string      `json:"text_value"`
	DateValue          *DateValue   `json:"date_value"`
	PeopleValue        []Ref        `json:"people_value"`
	DisplayValue       *string      `json:"display_value"`
}

// CustomFieldSetting is a custom field added to a project.
type CustomFieldSetting struct {
	GID         string      `json:"gid"`
	IsImportant bool        `json:"is_important"`
	CustomField CustomField `json:"custom_field"`
}

// Subtype is the field's type (resource_subtype, or the deprecated type).
func (f CustomField) Subtype() string {
	if f.ResourceSubtype != "" {
		return f.ResourceSubtype
	}
	return f.Type
}

// IsCustomID reports whether f is an ID custom field (Asana's task IDs, e.g. "RC-1").
// Asana marks ID custom fields with representation_type "custom_id" and a non-empty id_prefix.
func (f CustomField) IsCustomID() bool {
	return f.RepresentationType == "custom_id" || f.IDPrefix != ""
}

// RawCustomIDValue returns the raw string value (DisplayValue, else TextValue) of f,
// without checking IsCustomID. Callers that verify the field's definition separately
// (because task payloads omit representation_type and id_prefix on real Asana) use this.
func (f CustomField) RawCustomIDValue() string {
	if f.DisplayValue != nil && *f.DisplayValue != "" {
		return *f.DisplayValue
	}
	if f.TextValue != nil {
		return *f.TextValue
	}
	return ""
}

// CustomIDValue returns the task ID value of an ID custom field, or "" when f is not an ID
// field or has no value.
func (f CustomField) CustomIDValue() string {
	if !f.IsCustomID() {
		return ""
	}
	return f.RawCustomIDValue()
}
