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
	"custom_fields.name", "custom_fields.resource_subtype", "custom_fields.representation_type", "custom_fields.id_prefix",
	"custom_fields.precision", "custom_fields.enabled",
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
	"is_important", "custom_field.name", "custom_field.resource_subtype",
	"custom_field.representation_type", "custom_field.id_prefix", "custom_field.precision",
	"custom_field.is_formula_field", "custom_field.is_value_read_only",
	"custom_field.enum_options.name", "custom_field.enum_options.color", "custom_field.enum_options.enabled",
}

// CustomFieldSettings lists the custom fields of a project. Without custom fields (e.g. a
// free plan) Asana may answer 402 (IsPaymentRequired).
func (c *Client) CustomFieldSettings(ctx context.Context, projectGID string) ([]CustomFieldSetting, error) {
	return list[CustomFieldSetting](ctx, c, "/projects/"+url.PathEscape(projectGID)+"/custom_field_settings", fields(customFieldSettingFields))
}

// optionalTaskFields are task fields that some plans or workspaces may refuse (custom
// fields, dependencies, approvals, custom task types). If Asana refuses a task read with them (400 / 402), the
// read is repeated without them, and so are later ones of this client: tasks still sync,
// only without those fields (Task.Partial).
func isOptionalTaskField(f string) bool {
	for _, p := range []string{"custom_fields", "approval_status", "dependencies", "dependents", "assignee_section",
		"custom_type", "custom_type_status_option"} {
		if f == p || strings.HasPrefix(f, p+".") {
			return true
		}
	}
	return false
}

// isCustomTypeField reports whether f is one of the custom task type fields (#59), which a
// plan without custom types may refuse alone: they are left out first (fieldTier).
func isCustomTypeField(f string) bool {
	for _, p := range []string{"custom_type", "custom_type_status_option"} {
		if f == p || strings.HasPrefix(f, p+".") {
			return true
		}
	}
	return false
}

// fieldTier is which optional task fields a request leaves out: none, the custom type
// fields, or all of them (plain).
type fieldTier struct{ plain, noCustomTypes bool }

func (c *Client) tier() fieldTier {
	return fieldTier{plain: c.plainTasks.Load(), noCustomTypes: c.noCustomTypes.Load()}
}

// fields returns f without the fields the tier leaves out.
func (t fieldTier) fields(f []string) []string {
	switch {
	case t.plain:
		return slices.DeleteFunc(slices.Clone(f), isOptionalTaskField)
	case t.noCustomTypes:
		return slices.DeleteFunc(slices.Clone(f), isCustomTypeField)
	}
	return f
}

// mark records on a task what was left out of its request.
func (t fieldTier) mark(task *Task) {
	task.Partial = t.plain
	task.NoCustomType = t.plain || t.noCustomTypes
}

// taskFields returns the fields to request for tasks (detail: with enum options).
func (c *Client) taskFields(detail bool) []string {
	f := TaskFields
	if detail {
		f = taskDetailFields
	}
	return c.tier().fields(f)
}

// readTasks runs a task read with the fields to request, without the optional ones if
// Asana refused them: first without the custom type fields only (a plan without custom
// types, #59, keeps its custom fields and dependencies), then without all of them. Only
// reads are repeated (a write might be applied twice).
func readTasks(c *Client, detail bool, read func(fields []string) ([]Task, error)) ([]Task, error) {
	f := TaskFields
	if detail {
		f = taskDetailFields
	}
	tier := c.tier()
	tasks, err := read(tier.fields(f))
	refused := func(err error) bool {
		var apiErr *Error
		return err != nil && errors.As(err, &apiErr) && (apiErr.Status == http.StatusBadRequest || apiErr.Status == http.StatusPaymentRequired)
	}
	if refused(err) && !tier.plain && !tier.noCustomTypes {
		next := fieldTier{noCustomTypes: true}
		if again, err2 := read(next.fields(f)); err2 == nil { // they were the problem: leave them out from now on
			c.noCustomTypes.Store(true)
			tasks, err, tier = again, nil, next
		}
	}
	if refused(err) && !tier.plain {
		next := fieldTier{plain: true}
		if again, err2 := read(next.fields(f)); err2 == nil {
			c.plainTasks.Store(true)
			tasks, err, tier = again, nil, next
		}
	}
	for i := range tasks {
		tier.mark(&tasks[i])
	}
	return tasks, err
}

// customFieldFields are the fields of a custom field definition (GET /custom_fields/{gid},
// a workspace's list), with its enum options.
var customFieldFields = []string{
	"name", "resource_subtype", "representation_type", "id_prefix", "precision",
	"is_formula_field", "is_value_read_only",
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
