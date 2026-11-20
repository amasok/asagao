// Package readmodel holds the DTO types that the store returns to be shown, and that the
// application layer also passes back to the store. It only imports the standard library
// and internal/asana/model, so the ports in internal/app can use these types without
// importing the store (docs/design/adr/0006-application-use-cases.md, decision 4).
//
// The store keeps type aliases for them (store.UserRef), so its methods and their callers
// do not change.
package readmodel

import (
	"errors"

	"github.com/amasok/asagao/internal/asana/model"
)

// Types of custom fields (CustomFieldView.Type) that asagao shows and edits.
const (
	CFText      = model.CFText
	CFNumber    = model.CFNumber
	CFEnum      = model.CFEnum
	CFMultiEnum = model.CFMultiEnum
	CFDate      = model.CFDate
	CFPeople    = model.CFPeople
)

// ResultKind identifies what kind of locally created entity a LocalResult or
// RecordedResults lookup asks about without exposing SQLite sync_state key prefixes.
type ResultKind string

const (
	ResultTask       ResultKind = "task"
	ResultStory      ResultKind = "story"
	ResultAttachment ResultKind = "attachment"
	ResultStatus     ResultKind = "status"
	ResultTag        ResultKind = "tag"
	ResultSection    ResultKind = "section"
	ResultTimeEntry  ResultKind = "time_entry"
	ResultEnumOption ResultKind = "enum_option"
)

// ResetResult says what a reset with a backup did.
type ResetResult struct {
	Unsent     int    `json:"unsent"`                // unsent changes deleted
	BackupPath string `json:"backup_path,omitempty"` // "" when there was nothing to keep
}

// AnyUnsent lets a reset delete every unsent change, however many there are.
const AnyUnsent int64 = -1

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
