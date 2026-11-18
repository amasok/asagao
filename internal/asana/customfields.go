package asana

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

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
	for _, p := range []string{"custom_fields", "approval_status", "dependencies", "dependents", "assignee_section"} {
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

// customFieldFields are the fields of a custom field definition (GET /custom_fields/{gid},
// a workspace's list), with its enum options.
var customFieldFields = []string{
	"name", "resource_subtype", "precision", "is_formula_field", "is_value_read_only",
	"enum_options.name", "enum_options.color", "enum_options.enabled",
}

// CustomField reads one custom field definition with its enum options.
func (c *Client) CustomField(ctx context.Context, gid string) (*CustomField, error) {
	var f CustomField
	_, err := c.do(ctx, http.MethodGet, "/custom_fields/"+url.PathEscape(gid), fields(customFieldFields), nil, &f)
	return &f, err
}

// WorkspaceCustomFields lists the custom fields of a workspace (to add one to a project).
// Without custom fields (e.g. a free plan) Asana may answer 402.
func (c *Client) WorkspaceCustomFields(ctx context.Context, workspaceGID string) ([]CustomField, error) {
	return list[CustomField](ctx, c, "/workspaces/"+url.PathEscape(workspaceGID)+"/custom_fields", fields(customFieldFields))
}

// CreateEnumOption adds an option named name to an enum or multi_enum field (at the end).
func (c *Client) CreateEnumOption(ctx context.Context, fieldGID, name string) (*EnumOption, error) {
	var o EnumOption
	_, err := c.do(ctx, http.MethodPost, "/custom_fields/"+url.PathEscape(fieldGID)+"/enum_options",
		fields([]string{"name", "color", "enabled"}), map[string]any{"name": name}, &o)
	return &o, err
}

// AddCustomFieldSetting adds the custom field fieldGID to a project.
func (c *Client) AddCustomFieldSetting(ctx context.Context, projectGID, fieldGID string) error {
	_, err := c.do(ctx, http.MethodPost, "/projects/"+url.PathEscape(projectGID)+"/addCustomFieldSetting", nil,
		map[string]any{"custom_field": fieldGID}, nil)
	return err
}

// RemoveCustomFieldSetting removes the custom field fieldGID from a project.
func (c *Client) RemoveCustomFieldSetting(ctx context.Context, projectGID, fieldGID string) error {
	_, err := c.do(ctx, http.MethodPost, "/projects/"+url.PathEscape(projectGID)+"/removeCustomFieldSetting", nil,
		map[string]any{"custom_field": fieldGID}, nil)
	return err
}
