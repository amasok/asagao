package readmodel

import "strconv"

// SavedSearch is a task list query kept under a name in searches.toml (Issue #10). The
// fields are the list and search conditions of the CLI flags and the MCP tool arguments,
// kept as the user gave them: names are resolved and relative dates (today, +7d) read when
// the search is run, not when it is saved.
//
// A search with Query runs like `asagao search` (everyone's tasks, completed ones too); one
// without runs like `asagao task list` (my incomplete tasks, or everyone's in a project).
// Assignee and Completion override those defaults.
type SavedSearch struct {
	Name               string `toml:"name" json:"name"`
	Query              string `toml:"query,omitempty" json:"query,omitempty"`
	Assignee           string `toml:"assignee,omitempty" json:"assignee,omitempty"`
	Project            string `toml:"project,omitempty" json:"project,omitempty"`
	Section            string `toml:"section,omitempty" json:"section,omitempty"`
	Tag                string `toml:"tag,omitempty" json:"tag,omitempty"`
	Field              string `toml:"field,omitempty" json:"field,omitempty"`
	DueBefore          string `toml:"due_before,omitempty" json:"due_before,omitempty"`
	DueAfter           string `toml:"due_after,omitempty" json:"due_after,omitempty"`
	Completion         string `toml:"completion,omitempty" json:"completion,omitempty"`
	Overdue            bool   `toml:"overdue,omitempty" json:"overdue,omitempty"`
	ModifiedWithinDays int    `toml:"modified_within_days,omitempty" json:"modified_within_days,omitempty"`
	Limit              int    `toml:"limit,omitempty" json:"limit,omitempty"`
}

// Spec is the list query the search runs, with the defaults of `asagao search` when it
// has Query and of `asagao task list` otherwise.
func (s SavedSearch) Spec() FilterSpec {
	sp := FilterSpec{
		Assignee: s.Assignee, Project: s.Project, Section: s.Section, Tag: s.Tag, Field: s.Field,
		DueBefore: s.DueBefore, DueAfter: s.DueAfter, Completion: s.Completion, Overdue: s.Overdue,
		ModifiedWithinDays: s.ModifiedWithinDays, Limit: s.Limit, Query: s.Query,
	}
	if s.Query != "" {
		sp.DefaultAssignee = "any"
	} else {
		open := false
		sp.DefaultAssignee, sp.DefaultCompleted = "me", &open
	}
	return sp
}

// Conditions are the search's conditions as `key=value` with the keys of searches.toml,
// in a fixed order (for a list of the saved searches).
func (s SavedSearch) Conditions() []string {
	var out []string
	add := func(k, v string) {
		if v != "" {
			out = append(out, k+"="+v)
		}
	}
	add("query", s.Query)
	add("assignee", s.Assignee)
	add("project", s.Project)
	add("section", s.Section)
	add("tag", s.Tag)
	add("field", s.Field)
	add("due_after", s.DueAfter)
	add("due_before", s.DueBefore)
	add("completion", s.Completion)
	if s.Overdue {
		out = append(out, "overdue")
	}
	if s.ModifiedWithinDays > 0 {
		add("modified_within_days", strconv.Itoa(s.ModifiedWithinDays))
	}
	if s.Limit > 0 {
		add("limit", strconv.Itoa(s.Limit))
	}
	return out
}
