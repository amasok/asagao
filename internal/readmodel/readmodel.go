// Package readmodel holds the types that the store returns to be shown, and that the
// application layer also passes back to the store. It has no dependencies, so the ports in
// internal/app can use these types without importing the store
// (docs/design/adr/0006-application-use-cases.md, decision 4).
//
// The store keeps type aliases for them (store.UserRef), so its methods and their callers
// do not change. The rest of the read types move here in stage c2 (migration-plan.md).
package readmodel

// UserRef is a user as the CLI and MCP show it (keep the JSON stable), and the author of
// a comment or a status update written locally.
type UserRef struct {
	GID  string `json:"gid"`
	Name string `json:"name"`
}

// EnumOptionView is a choice of an enum / multi_enum field.
type EnumOptionView struct {
	GID     string `json:"gid"`
	Name    string `json:"name"`
	Color   string `json:"color,omitempty"`
	Enabled bool   `json:"enabled"`
}

// CustomFieldView is a custom field of a task with its value.
type CustomFieldView struct {
	GID  string `json:"gid"`
	Name string `json:"name"`
	Type string `json:"type"`
	// Value is in the form Asana accepts when setting it (nil when empty): text: string,
	// number: float64, enum: option gid, multi_enum / people: sorted []string of gids,
	// date: "YYYY-MM-DD". Display is it as text.
	Value   any    `json:"value"`
	Display string `json:"display"`
	// Color is the color of the chosen enum option.
	Color string `json:"color,omitempty"`
	// Important: a project shows this field prominently (Asana's is_important).
	Important bool `json:"important,omitempty"`
	// Editable: asagao can change the value (not a formula, read-only or unknown type).
	Editable bool `json:"editable"`
	// Options are the enabled choices of an enum / multi_enum field (task detail only).
	Options []EnumOptionView `json:"options,omitempty"`
}
