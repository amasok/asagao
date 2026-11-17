package readmodel

import "errors"

// FilterSpec is a task list query as the CLI flags and the MCP tool arguments give it.
// The store's BuildFilter turns it into a TaskFilter.
type FilterSpec struct {
	// Assignee: "me", "any"/"all" (everyone), a user gid, or a unique (partial) name.
	// "" means DefaultAssignee, or "any" when Project is set.
	Assignee        string
	DefaultAssignee string
	Project         string // gid or (partial) name
	Section         string // gid or exact name; requires Project
	Tag             string
	DueBefore       string // inclusive; YYYY-MM-DD or relative (today, +7d, ...)
	DueAfter        string // inclusive
	// Completion: "open", "completed", "all", or "" for DefaultCompleted.
	Completion       string
	DefaultCompleted *bool
	// Overdue keeps incomplete tasks due before today (today's are not overdue). It
	// overrides DueBefore and Completion.
	Overdue bool
	// ModifiedWithinDays keeps tasks modified in the last N days, newest first. Without
	// an explicit Completion it includes completed tasks (completing is a change).
	ModifiedWithinDays int // at most MaxModifiedWithinDays
	Limit              int
	Query              string
	// Field is a custom field filter: "name=value" (an option name or the value as text),
	// or "name" for any value. The name may also be the field's gid.
	Field string
}

// FilterError is an invalid FilterSpec field. Field is the spec's input name in
// snake_case (assignee, section, due_before, due_after, completion).
type FilterError struct {
	Field string
	Err   error
}

// MaxModifiedWithinDays caps FilterSpec.ModifiedWithinDays (about 100 years).
const MaxModifiedWithinDays = 36500

func (e *FilterError) Error() string { return e.Field + ": " + e.Err.Error() }

func (e *FilterError) Unwrap() error { return e.Err }

// ErrSectionWithoutProject is returned (in a FilterError) for a section without a project.
var ErrSectionWithoutProject = errors.New("section requires project")
