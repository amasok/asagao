package asana

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
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

// Subtype is the field's type (resource_subtype, or the deprecated type).
func (f CustomField) Subtype() string {
	if f.ResourceSubtype != "" {
		return f.ResourceSubtype
	}
	return f.Type
}

// CustomFieldSetting is a custom field added to a project.
type CustomFieldSetting struct {
	GID         string      `json:"gid"`
	IsImportant bool        `json:"is_important"`
	CustomField CustomField `json:"custom_field"`
}

// Fields of custom field values on tasks. Enum options are requested only for single
// tasks (taskDetailFields): projects' options come from their settings.
var customFieldValueFields = []string{
	"custom_fields.name", "custom_fields.resource_subtype", "custom_fields.precision", "custom_fields.enabled",
	"custom_fields.is_formula_field", "custom_fields.is_value_read_only", "custom_fields.display_value",
	"custom_fields.enum_value.name", "custom_fields.enum_value.color",
	"custom_fields.multi_enum_values.name", "custom_fields.multi_enum_values.color",
	"custom_fields.number_value", "custom_fields.text_value", "custom_fields.date_value",
	"custom_fields.people_value.name",
}

var enumOptionFields = []string{"custom_fields.enum_options.name", "custom_fields.enum_options.color", "custom_fields.enum_options.enabled"}

// taskDetailFields is TaskFields plus the enum options of the task's custom fields (for one task).
var taskDetailFields = slices.Concat(TaskFields, enumOptionFields)

var customFieldSettingFields = []string{
	"is_important", "custom_field.name", "custom_field.resource_subtype", "custom_field.precision",
	"custom_field.is_formula_field", "custom_field.is_value_read_only",
	"custom_field.enum_options.name", "custom_field.enum_options.color", "custom_field.enum_options.enabled",
}

// CustomFieldSettings lists the custom fields of a project. Without custom fields (e.g. a
// free plan) Asana may answer 402 (IsPaymentRequired).
func (c *Client) CustomFieldSettings(ctx context.Context, projectGID string) ([]CustomFieldSetting, error) {
	return list[CustomFieldSetting](ctx, c, "/projects/"+url.PathEscape(projectGID)+"/custom_field_settings", fields(customFieldSettingFields))
}

// optionalTaskFields are task fields that some plans or workspaces may refuse (custom
// fields, dependencies, approvals). If Asana refuses a task read with them (400 / 402), the
// read is repeated without them, and so are later ones of this client: tasks still sync,
// only without those fields (Task.Partial).
func isOptionalTaskField(f string) bool {
	for _, p := range []string{"custom_fields", "approval_status", "dependencies", "dependents"} {
		if f == p || strings.HasPrefix(f, p+".") {
			return true
		}
	}
	return false
}

// taskFields returns the fields to request for tasks (detail: with enum options).
func (c *Client) taskFields(detail bool) []string {
	f := TaskFields
	if detail {
		f = taskDetailFields
	}
	if !c.plainTasks.Load() {
		return f
	}
	return slices.DeleteFunc(slices.Clone(f), isOptionalTaskField)
}

// readTasks runs a task read with the fields to request, without the optional ones if
// Asana refused them. Only reads are repeated (a write might be applied twice).
func readTasks(c *Client, detail bool, read func(fields []string) ([]Task, error)) ([]Task, error) {
	plain := c.plainTasks.Load()
	tasks, err := read(c.taskFields(detail))
	var apiErr *Error
	if err != nil && !plain && errors.As(err, &apiErr) && (apiErr.Status == http.StatusBadRequest || apiErr.Status == http.StatusPaymentRequired) {
		f := slices.DeleteFunc(slices.Clone(c.taskFields(detail)), isOptionalTaskField)
		if again, err2 := read(f); err2 == nil { // they were the problem: leave them out from now on
			c.plainTasks.Store(true)
			tasks, err, plain = again, nil, true
		}
	}
	if plain {
		for i := range tasks {
			tasks[i].Partial = true
		}
	}
	return tasks, err
}
