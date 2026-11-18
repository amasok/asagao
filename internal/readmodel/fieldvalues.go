package readmodel

import (
	"slices"

	"github.com/amasok/asagao/internal/asana/model"
)

// Values of a task's fields in the form the local cache stores them and the outbox sends
// them, taken from a task Asana returned. The store keeps them for its tasks and
// set_custom_field entries; sync compares them with what Asana has now (conflict checks)
// and sends them (e1: moved from the store, which keeps aliases).
//
// Custom field values: text: string, number: float64, enum: option gid, multi_enum /
// people: sorted []string of gids, date: "YYYY-MM-DD"; nil when empty.

// TaskFieldValues extracts update_task fields from an API task (for conflict checks).
func TaskFieldValues(t *model.Task) map[string]any {
	assignee := ""
	if t.Assignee != nil {
		assignee = t.Assignee.GID
	}
	values := map[string]any{"completed": t.Completed, "name": t.Name, "notes": t.Notes, "html_notes": t.HTMLNotes, "due_on": t.DueOn, "assignee": assignee, "liked": t.Liked,
		"resource_subtype": t.ResourceSubtype, "approval_status": t.ApprovalStatus}
	if t.Partial {
		delete(values, "approval_status") // not fetched
	}
	return values
}

// CustomFieldValue returns the value of a custom field from Asana in the stored form.
func CustomFieldValue(f model.CustomField) any {
	switch f.Subtype() {
	case CFText:
		if f.TextValue != nil && *f.TextValue != "" {
			return *f.TextValue
		}
	case CFNumber:
		if f.NumberValue != nil {
			return *f.NumberValue
		}
	case CFEnum:
		if f.EnumValue != nil && f.EnumValue.GID != "" {
			return f.EnumValue.GID
		}
	case CFMultiEnum:
		var gids []string
		for _, o := range f.MultiEnumValues {
			gids = append(gids, o.GID)
		}
		return GIDList(gids)
	case CFDate:
		if f.DateValue != nil && f.DateValue.Date != "" {
			return f.DateValue.Date
		}
	case CFPeople:
		var gids []string
		for _, u := range f.PeopleValue {
			gids = append(gids, u.GID)
		}
		return GIDList(gids)
	}
	return nil
}

// GIDList sorts gids (the stored form of a multi_enum or people value); nil when there are
// none.
func GIDList(gids []string) any {
	if len(gids) == 0 {
		return nil
	}
	gids = slices.Clone(gids)
	slices.Sort(gids)
	return slices.Compact(gids)
}

// NormalizeCustomFieldValue turns a value decoded from JSON into the stored form.
func NormalizeCustomFieldValue(v any) any {
	switch x := v.(type) {
	case string:
		if x == "" {
			return nil
		}
	case []string:
		return GIDList(x)
	case []any:
		var gids []string
		for _, e := range x {
			if s, ok := e.(string); ok {
				gids = append(gids, s)
			}
		}
		return GIDList(gids)
	case int:
		return float64(x)
	}
	return v
}

// CustomFieldAPIValue is a stored value as PUT /tasks sends it: the same, except that a
// date goes as {"date": "YYYY-MM-DD"} (Asana rejects a plain string for date fields,
// although its reference shows one; see the custom fields guide and forum). nil clears.
func CustomFieldAPIValue(fieldType string, v any) any {
	v = NormalizeCustomFieldValue(v)
	if s, ok := v.(string); ok && fieldType == CFDate {
		return map[string]any{"date": s}
	}
	if v == nil && fieldType == CFPeople {
		// Asana clears people with an empty list; null is "400 people_value: Not an
		// array" (found by scripts/realcheck, #31).
		return []string{}
	}
	return v
}

// ServerCustomField finds a field on a task fetched from Asana: its value (stored form)
// and display text. found is false if the task does not have the field (any more).
func ServerCustomField(t *model.Task, fieldGID string) (value any, display string, found bool) {
	for _, f := range t.CustomFields {
		if f.GID == fieldGID {
			display := ""
			if f.DisplayValue != nil {
				display = *f.DisplayValue
			}
			return CustomFieldValue(f), display, true
		}
	}
	return nil, "", false
}
