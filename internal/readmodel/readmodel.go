// Package readmodel holds the types that the store returns to be shown, and that the
// application layer also passes back to the store. It has no dependencies, so the ports in
// internal/app can use these types without importing the store
// (docs/design/adr/0006-application-use-cases.md, decision 4).
//
// The store keeps type aliases for them (store.UserRef), so its methods and their callers
// do not change. Stage c2 (migration-plan.md) moves the read types here: the views of
// tasks (tasks.go) and of projects (projects.go), the inbox (inbox.go), the task list
// query (filter.go), the errors the entry points check (errors.go) and the wording of
// outbox entries, dropped changes and status updates (describe.go).
package readmodel

import (
	"errors"

	"github.com/amasok/asagao/internal/domain/outbox"
)

// UserRef is a user as the CLI and MCP show it (keep the JSON stable), and the author of
// a comment or a status update written locally.
type UserRef struct {
	GID  string `json:"gid"`
	Name string `json:"name"`
}

// UserContact is a known user with the name and email Asana gave (either may be ""), for
// matching what a user types (the quick-add "@name") against both.
type UserContact struct {
	GID, Name, Email string
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
	// CanAddOption: an option can be added to this enum / multi_enum field (task detail
	// only). Not when Asana refused it before (402: the plan; 403: e.g. locked options).
	CanAddOption bool `json:"can_add_option,omitempty"`
}

// ProjectFieldView is a custom field of a project, or of its workspace that can be added
// to it (#37).
type ProjectFieldView struct {
	GID  string `json:"gid"`
	Name string `json:"name"`
	Type string `json:"type"`
	// Attached: the project has the field (as changed locally: an add or a removal not sent
	// yet counts). Pending: such a change is queued.
	Attached  bool `json:"attached"`
	Important bool `json:"important,omitempty"`
	Pending   bool `json:"pending,omitempty"`
}

// ErrOpSending is returned when an outbox entry that is being sent is retried, resolved
// or discarded.
var ErrOpSending = errors.New("この変更は送信中です。送信が終わってからやり直してください")

// OpStateLabel is the Japanese name of an outbox entry's state.
func OpStateLabel(state string) string {
	switch state {
	case outbox.StatePending:
		return "未送信"
	case outbox.StateConflict:
		return "競合"
	case outbox.StateFailed:
		return "失敗"
	case outbox.StateSending:
		return "送信中"
	}
	return state
}
