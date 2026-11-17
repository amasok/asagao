package model

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
	GID             string       `json:"gid"`
	Name            string       `json:"name"`
	ResourceSubtype string       `json:"resource_subtype"` // text, number, enum, multi_enum, date, people, ...
	Type            string       `json:"type"`             // deprecated alias of resource_subtype
	Precision       int          `json:"precision"`
	Enabled         *bool        `json:"enabled"`
	IsFormulaField  bool         `json:"is_formula_field"`
	IsValueReadOnly bool         `json:"is_value_read_only"`
	EnumOptions     []EnumOption `json:"enum_options"`
	EnumValue       *EnumOption  `json:"enum_value"`
	MultiEnumValues []EnumOption `json:"multi_enum_values"`
	NumberValue     *float64     `json:"number_value"`
	TextValue       *string      `json:"text_value"`
	DateValue       *DateValue   `json:"date_value"`
	PeopleValue     []Ref        `json:"people_value"`
	DisplayValue    *string      `json:"display_value"`
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
