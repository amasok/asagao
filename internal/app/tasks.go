package app

import (
	"context"

	"github.com/amasok/asagao/internal/domain/task"
)

// Tasks is the task use cases.
type Tasks struct {
	Store TaskStore
	Text  RichText
	Clock Clock
}

// CreateTaskInput is what a user gives to create a task. Project, Section and Assignee
// are names or gids; Due is a date as ParseDue reads it; Notes is Markdown.
type CreateTaskInput struct {
	Name, Parent, Project, Section, Due, Assignee, Notes string
	Tags                                                 []string // names or gids to add
}

// Create resolves all of its input and then creates the task locally, queued in the
// outbox. Nothing is written when the input is invalid: a bad due date or assignee is a
// *FieldError, a name that does not resolve is the store's error.
//
//   - A subtask (Parent) gets its workspace from the parent and is in no project.
//   - In a project, the task goes to Section, or to the first section.
//   - Otherwise it goes to the default workspace and is assigned to me unless Assignee says.
//
// Tags are added after the task is created, in their own transaction(s). If that fails,
// the task is still created and its gid is returned with the error.
func (t *Tasks) Create(ctx context.Context, in CreateTaskInput) (string, error) {
	now := t.Clock()
	nt := task.NewTask{Name: in.Name, ParentGID: in.Parent}
	if in.Notes != "" {
		nt.HTMLNotes, nt.Notes = t.Text.TaskNotes(in.Notes)
	}
	var err error
	if nt.DueOn, err = ParseDue(in.Due, now); err != nil {
		return "", &FieldError{Field: "due", Err: err}
	}
	assignee := in.Assignee
	switch {
	case in.Parent != "":
		// workspace comes from the parent; no project or section
	case in.Project != "":
		if nt.ProjectGID, err = t.Store.ResolveProject(ctx, in.Project); err != nil {
			return "", err
		}
		if in.Section != "" {
			nt.SectionGID, err = t.Store.ResolveSection(ctx, nt.ProjectGID, in.Section)
		} else {
			nt.SectionGID, err = t.Store.FirstSection(ctx, nt.ProjectGID)
		}
		if err != nil {
			return "", err
		}
	default:
		if nt.WorkspaceGID, err = t.Store.DefaultWorkspace(ctx); err != nil {
			return "", err
		}
		if assignee == "" {
			assignee = "me"
		}
	}
	if nt.AssigneeGID, err = t.Store.ResolveUser(ctx, assignee); err != nil {
		return "", &FieldError{Field: "assignee", Err: err}
	}
	tags, err := t.Store.ResolveTagEdit(ctx, in.Tags, nil)
	if err != nil {
		return "", err
	}
	gid, err := t.Store.CreateTaskLocal(ctx, nt, now)
	if err != nil {
		return "", err
	}
	if err := t.Store.ApplyTagEdit(ctx, gid, tags, now); err != nil {
		return gid, err
	}
	return gid, nil
}

// CreateHereInput is a task to create where the user already is, as the TUI does: Parent,
// Project and Section are gids the entry point already has, not names, and Due is already
// a date (YYYY-MM-DD, or ""). Nothing in it is resolved by name.
type CreateHereInput struct {
	Name, Due string
	Parent    string // gid of the parent task: a subtask
	Project   string // gid of the project the user is looking at
	Section   string // gid of a section of Project ("": the first section)

	// Assignee is the gid to assign instead of the default (nil: the default below;
	// "": no one). Tags are existing tags (gids) to add after the task is created.
	Assignee *string
	Tags     []task.TagRef
}

// CreateHere creates a task from gids, without resolving names, locally and queued in the
// outbox:
//
//   - A subtask (Parent) gets its workspace from the parent and is in no project.
//   - In Project, the task goes to Section, or to the first section, assigned to no one.
//   - Otherwise it goes to the default workspace, assigned to me.
//
// Assignee, when given, replaces the default. Tags are added after the task is created,
// as in Create: if that fails, the task is still created and its gid is returned with
// the error.
//
// Errors are the store's, as they are (there is no user input to name in a *FieldError).
// Nothing is written when one comes before the task is created.
func (t *Tasks) CreateHere(ctx context.Context, in CreateHereInput) (string, error) {
	now := t.Clock()
	nt := task.NewTask{Name: in.Name, DueOn: in.Due, ParentGID: in.Parent}
	var err error
	switch {
	case in.Parent != "":
		// workspace comes from the parent; no project or section
	case in.Project != "":
		nt.ProjectGID, nt.SectionGID = in.Project, in.Section
		if nt.SectionGID == "" {
			if nt.SectionGID, err = t.Store.FirstSection(ctx, in.Project); err != nil {
				return "", err
			}
		}
	default:
		if nt.WorkspaceGID, err = t.Store.DefaultWorkspace(ctx); err != nil {
			return "", err
		}
		// "me" resolves to my gid without a name lookup (the store's MeGID)
		if in.Assignee == nil {
			if nt.AssigneeGID, err = t.Store.ResolveUser(ctx, "me"); err != nil {
				return "", err
			}
		}
	}
	if in.Assignee != nil {
		nt.AssigneeGID = *in.Assignee
	}
	gid, err := t.Store.CreateTaskLocal(ctx, nt, now)
	if err != nil || len(in.Tags) == 0 {
		return gid, err
	}
	if err := t.Store.ApplyTagEdit(ctx, gid, task.TagEdit{Add: in.Tags}, now); err != nil {
		return gid, err
	}
	return gid, nil
}
