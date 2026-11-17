package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/amasok/asagao/internal/dates"
	"github.com/amasok/asagao/internal/readmodel"
)

// FilterSpec is readmodel.FilterSpec.
type FilterSpec = readmodel.FilterSpec

// FilterError is readmodel.FilterError.
type FilterError = readmodel.FilterError

// MaxModifiedWithinDays is readmodel.MaxModifiedWithinDays.
const MaxModifiedWithinDays = readmodel.MaxModifiedWithinDays

// ErrSectionWithoutProject is readmodel.ErrSectionWithoutProject.
var ErrSectionWithoutProject = readmodel.ErrSectionWithoutProject

// BuildFilter resolves names and relative dates in spec.
func (s *Store) BuildFilter(ctx context.Context, spec FilterSpec, now time.Time) (TaskFilter, error) {
	f := TaskFilter{Tag: spec.Tag, Limit: spec.Limit, Query: spec.Query}
	f.Field, f.FieldValue = ParseFieldFilter(spec.Field)

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
		return f, &FilterError{Field: "assignee", Err: errors.New(`filtering unassigned tasks is not supported; use "any"`)}
	default:
		gid, err := s.ResolveUser(ctx, assignee)
		if err != nil {
			return f, &FilterError{Field: "assignee", Err: err}
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
		return f, &FilterError{Field: "section", Err: ErrSectionWithoutProject}
	}

	var err error
	if f.DueBefore, err = dates.Parse(spec.DueBefore, now); err != nil {
		return f, &FilterError{Field: "due_before", Err: err}
	}
	if f.DueAfter, err = dates.Parse(spec.DueAfter, now); err != nil {
		return f, &FilterError{Field: "due_after", Err: err}
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
		return f, &FilterError{Field: "completion", Err: fmt.Errorf("must be open, completed or all (got %q)", spec.Completion)}
	}
	if spec.Overdue {
		f.DueBefore = now.AddDate(0, 0, -1).Format(time.DateOnly)
		f.Completed = ptrTo(false)
	}
	if spec.ModifiedWithinDays > 0 {
		// more days than this mean "all": a huge N would overflow the date
		days := min(spec.ModifiedWithinDays, MaxModifiedWithinDays)
		f.ModifiedAfter = now.AddDate(0, 0, -days).UTC().Format(time.RFC3339)
		f.SortByModified = true
	}
	return f, nil
}

func ptrTo[T any](v T) *T { return &v }
