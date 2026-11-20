package app

import (
	"context"
	"errors"
	"time"

	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/domain/plan"
	"github.com/amasok/asagao/internal/readmodel"
)

// SectionStore is the part of the local cache that moving a task between sections and
// renaming a section need. *store.Store satisfies it.
type SectionStore interface {
	ResolveProject(ctx context.Context, arg string) (string, error)
	ResolveSection(ctx context.Context, projectGID, arg string) (string, error)
	ResolveMove(ctx context.Context, taskGID, project, section string) (projectGID, sectionGID string, err error)
	ResolveMoveNear(ctx context.Context, taskGID, project, anchor string) (projectGID, sectionGID string, err error)
	MoveTaskLocal(ctx context.Context, gid, projectGID, sectionGID string, now time.Time) error
	MoveTaskAtLocal(ctx context.Context, gid, projectGID, sectionGID string, pos model.Position, now time.Time) error
	RenameSectionLocal(ctx context.Context, sectionGID, name string, now time.Time) error
	CreateSectionLocal(ctx context.Context, projectGID, name, before, after string, now time.Time) (string, error)
	DeleteSectionLocal(ctx context.Context, sectionGID string, now time.Time) error
	MoveSectionLocal(ctx context.Context, sectionGID, before, after string, now time.Time) error
	PlanRefusedAt(ctx context.Context, feature, workspaceGID string) (time.Time, bool, error)
	SectionName(ctx context.Context, gid string) (string, error)
	ListMyTaskSections(ctx context.Context, workspaceGID string) ([]readmodel.SectionView, error)
	ResolveMySection(ctx context.Context, taskGID, arg string) (string, error)
	MoveMySectionLocal(ctx context.Context, gid, sectionGID string, now time.Time) error
	// The workspaces a 402 is recorded for.
	ProjectWorkspace(ctx context.Context, projectGID string) (string, error)
	SectionWorkspace(ctx context.Context, sectionGID string) (string, error)
	TaskWorkspace(ctx context.Context, taskGID string) (string, error)
	DefaultWorkspace(ctx context.Context) (string, error)
	WorkspaceGIDs(ctx context.Context) ([]string, error)
}

// Sections is the use cases on sections: moving a task to another section, and renaming
// a section. Each is one local write, queued in the outbox.
type Sections struct {
	Store SectionStore
	Clock Clock
}

// Move moves task taskGID to the section named (or with the gid) section. project picks
// the project when the task is in more than one ("" for the only one). The errors are
// the store's, as they are, and nothing is written after one.
func (s *Sections) Move(ctx context.Context, taskGID, project, section string) error {
	projectGID, sectionGID, err := s.ResolveMove(ctx, taskGID, project, section)
	if err != nil {
		return err
	}
	return s.MoveHere(ctx, taskGID, projectGID, sectionGID)
}

// ResolveMove resolves the project and section to move task taskGID to, as Move does,
// without writing anything.
func (s *Sections) ResolveMove(ctx context.Context, taskGID, project, section string) (projectGID, sectionGID string, err error) {
	return s.Store.ResolveMove(ctx, taskGID, project, section)
}

// MoveHere moves task taskGID to the section sectionGID of the project projectGID, both
// gids the user already chose (the TUI's board and section chooser).
func (s *Sections) MoveHere(ctx context.Context, taskGID, projectGID, sectionGID string) error {
	return s.Store.MoveTaskLocal(ctx, taskGID, projectGID, sectionGID, s.Clock())
}

// Rename renames the section named (or with the gid) section in the project named (or
// with the gid) project. The errors are the store's, as they are (a project or section
// that does not resolve, an empty name), and nothing is written after one.
func (s *Sections) Rename(ctx context.Context, project, section, name string) error {
	projectGID, err := s.Store.ResolveProject(ctx, project)
	if err != nil {
		return err
	}
	gid, err := s.Store.ResolveSection(ctx, projectGID, section)
	if err != nil {
		return err
	}
	return s.RenameHere(ctx, gid, name)
}

// RenameHere renames the section sectionGID, which the user already is on (the TUI).
func (s *Sections) RenameHere(ctx context.Context, sectionGID, name string) error {
	return s.Store.RenameSectionLocal(ctx, sectionGID, name, s.Clock())
}

// ErrPlanUnavailable is returned for a feature that Asana refused because the workspace's
// plan lacks it. The message says which.
type ErrPlanUnavailable = plan.UnavailableError

// IsPlanUnavailable reports whether err is an ErrPlanUnavailable.
func IsPlanUnavailable(err error) bool {
	var e *ErrPlanUnavailable
	return errors.As(err, &e)
}

// Available reports whether creating, deleting and reordering sections can be offered:
// unless a recent 402 hides them in every workspace. Each change checks its project's
// workspace (inProject).
func (s *Sections) Available(ctx context.Context) bool {
	return planAnywhere(ctx, s.Store, plan.Sections, planNow(s.Clock))
}

// inProject returns ErrPlanUnavailable while a recent 402 hides creating, deleting and
// reordering sections in the workspace of project projectGID.
func (s *Sections) inProject(ctx context.Context, projectGID string) error {
	ws, err := s.Store.ProjectWorkspace(ctx, projectGID)
	return planIn(ctx, s.Store, plan.Sections, ws, err, planNow(s.Clock))
}

// ofSection is inProject for the project of section sectionGID.
func (s *Sections) ofSection(ctx context.Context, sectionGID string) error {
	ws, err := s.Store.SectionWorkspace(ctx, sectionGID)
	return planIn(ctx, s.Store, plan.Sections, ws, err, planNow(s.Clock))
}

// SectionPlace says where a new or moved section goes: just before or just after another
// section of the project (a name or gid), or (both empty) last.
type SectionPlace struct {
	Before string
	After  string
}

func (s *Sections) resolvePlace(ctx context.Context, projectGID string, at SectionPlace) (before, after string, err error) {
	if at.Before != "" && at.After != "" {
		return "", "", errors.New("before と after は片方だけ指定してください")
	}
	if at.Before != "" {
		before, err = s.Store.ResolveSection(ctx, projectGID, at.Before)
	}
	if err == nil && at.After != "" {
		after, err = s.Store.ResolveSection(ctx, projectGID, at.After)
	}
	return before, after, err
}

// Create creates a section named name in the project named (or with the gid) project, at
// at. It returns the new section's gid (temporary until it is sent; tasks can be moved
// to it right away).
func (s *Sections) Create(ctx context.Context, project, name string, at SectionPlace) (string, error) {
	projectGID, err := s.Store.ResolveProject(ctx, project)
	if err != nil {
		return "", err
	}
	before, after, err := s.resolvePlace(ctx, projectGID, at)
	if err != nil {
		return "", err
	}
	return s.CreateHere(ctx, projectGID, name, before, after)
}

// CreateHere creates a section in projectGID next to the section gids before / after (at
// most one), as the TUI does on the section the user is on.
func (s *Sections) CreateHere(ctx context.Context, projectGID, name, before, after string) (string, error) {
	if err := s.inProject(ctx, projectGID); err != nil {
		return "", err
	}
	return s.Store.CreateSectionLocal(ctx, projectGID, name, before, after, s.Clock())
}

// ResolveIn resolves the section named (or with the gid) section of the project named (or
// with the gid) project, without changing anything.
func (s *Sections) ResolveIn(ctx context.Context, project, section string) (projectGID, sectionGID string, err error) {
	if projectGID, err = s.Store.ResolveProject(ctx, project); err != nil {
		return "", "", err
	}
	sectionGID, err = s.Store.ResolveSection(ctx, projectGID, section)
	return projectGID, sectionGID, err
}

// Delete deletes the (empty) section named (or with the gid) section of project.
func (s *Sections) Delete(ctx context.Context, project, section string) error {
	_, gid, err := s.ResolveIn(ctx, project, section)
	if err != nil {
		return err
	}
	return s.DeleteHere(ctx, gid)
}

// DeleteHere deletes the (empty) section sectionGID.
func (s *Sections) DeleteHere(ctx context.Context, sectionGID string) error {
	if err := s.ofSection(ctx, sectionGID); err != nil {
		return err
	}
	return s.Store.DeleteSectionLocal(ctx, sectionGID, s.Clock())
}

// Reorder moves the section named (or with the gid) section of project just before or
// just after another section (exactly one of at.Before / at.After).
func (s *Sections) Reorder(ctx context.Context, project, section string, at SectionPlace) error {
	projectGID, gid, err := s.ResolveIn(ctx, project, section)
	if err != nil {
		return err
	}
	before, after, err := s.resolvePlace(ctx, projectGID, at)
	if err != nil {
		return err
	}
	return s.ReorderHere(ctx, gid, before, after)
}

// ReorderHere moves the section sectionGID just before or just after another section gid.
func (s *Sections) ReorderHere(ctx context.Context, sectionGID, before, after string) error {
	if err := s.ofSection(ctx, sectionGID); err != nil {
		return err
	}
	return s.Store.MoveSectionLocal(ctx, sectionGID, before, after, s.Clock())
}

// MySections returns the sections of my My Tasks list in a workspace ("" for the default
// one), in order: none when they are not known (not synced yet, or a recent 402 says the
// workspace's plan lacks them).
func (s *Sections) MySections(ctx context.Context, workspaceGID string) ([]readmodel.SectionView, error) {
	if planIn(ctx, s.Store, plan.MySection, workspaceGID, nil, planNow(s.Clock)) != nil {
		return []readmodel.SectionView{}, nil
	}
	return s.Store.ListMyTaskSections(ctx, workspaceGID)
}

// MoveMine moves task taskGID (assigned to me) to the section of My Tasks named (or with
// the gid) section.
func (s *Sections) MoveMine(ctx context.Context, taskGID, section string) error {
	if err := s.myIn(ctx, taskGID); err != nil { // before the name: none may be cached then
		return err
	}
	gid, err := s.Store.ResolveMySection(ctx, taskGID, section)
	if err != nil {
		return err
	}
	return s.MoveMineHere(ctx, taskGID, gid)
}

// MoveMineHere moves task taskGID to the My Tasks section sectionGID the user chose.
func (s *Sections) MoveMineHere(ctx context.Context, taskGID, sectionGID string) error {
	if err := s.myIn(ctx, taskGID); err != nil {
		return err
	}
	return s.Store.MoveMySectionLocal(ctx, taskGID, sectionGID, s.Clock())
}

// myIn returns ErrPlanUnavailable while a recent 402 hides the sections of My Tasks in the
// workspace of task taskGID.
func (s *Sections) myIn(ctx context.Context, taskGID string) error {
	ws, err := s.Store.TaskWorkspace(ctx, taskGID)
	return planIn(ctx, s.Store, plan.MySection, ws, err, planNow(s.Clock))
}
