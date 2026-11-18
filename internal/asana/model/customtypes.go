package model

import "strings"

// CustomType is a custom task type (#59): a kind of task a workspace defines, besides
// milestones and approvals, with its own statuses. A task of one has resource_subtype
// "custom". GET /custom_types?project= lists the types a project offers; a task names its
// type and status in the compact form (gid and name).
type CustomType struct {
	GID           string                   `json:"gid"`
	Name          string                   `json:"name"`
	StatusOptions []CustomTypeStatusOption `json:"status_options,omitempty"`
}

// CustomTypeStatusOption is a status of a custom type. CompletionState is Asana's
// "Incomplete" or "Complete" (each type has at least one of both); a task in a complete
// status is done.
type CustomTypeStatusOption struct {
	GID             string `json:"gid"`
	Name            string `json:"name"`
	Color           string `json:"color,omitempty"`
	Enabled         *bool  `json:"enabled,omitempty"`
	CompletionState string `json:"completion_state,omitempty"`
}

// IsEnabled reports whether the status can be chosen (Asana leaves out "enabled" in the
// compact form: it is then taken as enabled).
func (o CustomTypeStatusOption) IsEnabled() bool { return o.Enabled == nil || *o.Enabled }

// IsComplete reports whether a task in this status is done (case is not relied on).
func (o CustomTypeStatusOption) IsComplete() bool {
	return strings.EqualFold(o.CompletionState, "complete")
}
