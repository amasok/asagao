package app

import (
	"context"
	"time"

	"github.com/amasok/asagao/internal/asana/model"
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
