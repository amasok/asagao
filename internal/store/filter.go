package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/amasok/asana-tui/internal/dates"
)

// FilterSpec is a task list query as the CLI flags and the MCP tool arguments give it.
// BuildFilter turns it into a TaskFilter.
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
	ModifiedWithinDays int
	Limit              int
	Query              string
}

// FilterError is an invalid FilterSpec field. Field is the spec's input name in
// snake_case (assignee, section, due_before, due_after, completion).
type FilterError struct {
	Field string
	Err   error
}

func (e *FilterError) Error() string { return e.Field + ": " + e.Err.Error() }
func (e *FilterError) Unwrap() error { return e.Err }

// ErrSectionWithoutProject is returned (in a FilterError) for a section without a project.
var ErrSectionWithoutProject = errors.New("section requires project")

// BuildFilter resolves names and relative dates in spec.
func (s *Store) BuildFilter(ctx context.Context, spec FilterSpec, now time.Time) (TaskFilter, error) {
	f := TaskFilter{Tag: spec.Tag, Limit: spec.Limit, Query: spec.Query}

	assignee := spec.Assignee
	if assignee == "" {
		assignee = spec.DefaultAssignee
		if spec.Project != "" {
			assignee = "any"
		}
	}
	switch assignee {
	case "", "any", "all":
	case "none":
		return f, &FilterError{"assignee", errors.New(`filtering unassigned tasks is not supported; use "any"`)}
	default:
		gid, err := s.ResolveUser(ctx, assignee)
		if errors.Is(err, ErrNotFound) && isGID(assignee) {
			gid, err = assignee, nil // a user not seen yet
		}
		if err != nil {
			return f, &FilterError{"assignee", err}
		}
		f.AssigneeGID = gid
	}

	if spec.Project != "" {
		gid, err := s.ResolveProject(ctx, spec.Project)
		if err != nil {
			return f, err
		}
		f.ProjectGID = gid
		if spec.Section != "" {
			if f.SectionGID, err = s.ResolveSection(ctx, gid, spec.Section); err != nil {
				return f, err
			}
		}
	} else if spec.Section != "" {
		return f, &FilterError{"section", ErrSectionWithoutProject}
	}

	var err error
	if f.DueBefore, err = dates.Parse(spec.DueBefore, now); err != nil {
		return f, &FilterError{"due_before", err}
	}
	if f.DueAfter, err = dates.Parse(spec.DueAfter, now); err != nil {
		return f, &FilterError{"due_after", err}
	}

	f.Completed = spec.DefaultCompleted
	switch spec.Completion {
	case "":
		if spec.ModifiedWithinDays > 0 {
			f.Completed = nil
		}
	case "open":
		f.Completed = ptrTo(false)
	case "completed":
		f.Completed = ptrTo(true)
	case "all":
		f.Completed = nil
	default:
		return f, &FilterError{"completion", fmt.Errorf("must be open, completed or all (got %q)", spec.Completion)}
	}
	if spec.Overdue {
		f.DueBefore = now.AddDate(0, 0, -1).Format(time.DateOnly)
		f.Completed = ptrTo(false)
	}
	if spec.ModifiedWithinDays > 0 {
		f.ModifiedAfter = now.AddDate(0, 0, -spec.ModifiedWithinDays).UTC().Format(time.RFC3339)
		f.SortByModified = true
	}
	return f, nil
}

func isGID(s string) bool {
	return s != "" && strings.Trim(s, "0123456789") == ""
}

func ptrTo[T any](v T) *T { return &v }
