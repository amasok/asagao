package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Name resolution shared by the CLI and the MCP server. Resolving is separate from
// writing so that a command can check all of its input before changing anything.

// TagRef is a tag to put on a task: an existing tag (GID) or a new one to create (Name).
type TagRef struct {
	GID  string
	Name string
}

// TagEdit is a resolved set of tag changes for one task.
type TagEdit struct {
	Add    []TagRef
	Remove []string // tag gids
}

// Empty reports whether the edit changes nothing.
func (e TagEdit) Empty() bool { return len(e.Add) == 0 && len(e.Remove) == 0 }

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
