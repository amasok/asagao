package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/amasok/asagao/internal/domain/task"
)

// Name resolution shared by the CLI and the MCP server. Resolving is separate from
// writing so that a command can check all of its input before changing anything.

// TagRef and TagEdit moved to internal/domain/task; these aliases keep the store's
// signatures and callers unchanged until every entry point goes through internal/app.
type (
	TagRef  = task.TagRef
	TagEdit = task.TagEdit
)

// ResolveTagEdit resolves tag names or gids ("#" prefix allowed). Unknown names to add
// become new tags; unknown tags to remove are an error.
func (s *Store) ResolveTagEdit(ctx context.Context, add, remove []string) (TagEdit, error) {
	var e TagEdit
	if len(add) == 0 && len(remove) == 0 {
		return e, nil
	}
	tags, err := s.ListTags(ctx)
	if err != nil {
		return e, err
	}
	find := func(arg string) string {
		name := strings.TrimSpace(strings.TrimPrefix(arg, "#"))
		for _, t := range tags {
			if t.GID == arg || t.Name == name {
				return t.GID
			}
		}
		return ""
	}
	for _, a := range add {
		if gid := find(a); gid != "" {
			e.Add = append(e.Add, TagRef{GID: gid})
			continue
		}
		name := strings.TrimSpace(strings.TrimPrefix(a, "#"))
		if name == "" {
			return TagEdit{}, fmt.Errorf("add tag %q: タグ名が空です", a)
		}
		e.Add = append(e.Add, TagRef{Name: name})
	}
	for _, r := range remove {
		gid := find(r)
		if gid == "" {
			return TagEdit{}, fmt.Errorf("remove tag %q: no such tag: %w", r, ErrNotFound)
		}
		e.Remove = append(e.Remove, gid)
	}
	return e, nil
}

// ApplyTagEdit tags and untags a task (creating new tags) and queues the changes.
func (s *Store) ApplyTagEdit(ctx context.Context, taskGID string, e TagEdit, now time.Time) error {
	for _, a := range e.Add {
		if _, err := s.AddTagLocal(ctx, taskGID, a.GID, a.Name, now); err != nil {
			return fmt.Errorf("add tag %q: %w", a.GID+a.Name, err)
		}
	}
	for _, gid := range e.Remove {
		if err := s.RemoveTagLocal(ctx, taskGID, gid, now); err != nil {
			return fmt.Errorf("remove tag %q: %w", gid, err)
		}
	}
	return nil
}

// ResolveUsers resolves each of "me", a gid, or a unique (partial) name or email to a
// user gid. "none" and "" are not users here and are an error.
func (s *Store) ResolveUsers(ctx context.Context, args []string) ([]string, error) {
	gids := make([]string, 0, len(args))
	for _, a := range args {
		if a == "" || a == "none" {
			return nil, fmt.Errorf("user %q: %w", a, ErrNotFound)
		}
		gid, err := s.ResolveUser(ctx, a)
		if err != nil {
			return nil, err
		}
		gids = append(gids, gid)
	}
	return gids, nil
}

// ResolveProjects resolves each project gid or (partial) name.
func (s *Store) ResolveProjects(ctx context.Context, args []string) ([]string, error) {
	gids := make([]string, 0, len(args))
	for _, a := range args {
		gid, err := s.ResolveProject(ctx, a)
		if err != nil {
			return nil, err
		}
		gids = append(gids, gid)
	}
	return gids, nil
}
