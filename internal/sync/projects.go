package sync

import (
	"context"
	"errors"
	"strings"

	"github.com/amasok/asana-tui/internal/asana"
	"github.com/amasok/asana-tui/internal/store"
)

// Project operations that need Asana right away (creating, archiving, members). They are
// not queued: tasks may be created in a new project immediately, which an offline
// placeholder project could not support. The store is updated with Asana's response.

// ErrOffline is returned by online-only operations when there is no API client.
var ErrOffline = errors.New("オンラインのときだけできる操作です")

func (s *Syncer) saveProject(ctx context.Context, p *asana.Project) error {
	me, _ := s.Store.MeGID(ctx)
	ws := ""
	if p.Workspace != nil {
		ws = p.Workspace.GID
	}
	return s.Store.UpsertProjects(ctx, ws, []asana.Project{*p}, me)
}

// CreateProject creates a project (in the default workspace) and stores it with its sections.
func (s *Syncer) CreateProject(ctx context.Context, name string) (*asana.Project, error) {
	if s.API == nil {
		return nil, ErrOffline
	}
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("プロジェクト名が空です")
	}
	ws, err := s.Store.DefaultWorkspace(ctx)
	if err != nil {
		return nil, err
	}
	p, err := s.API.CreateProject(ctx, ws, "", name)
	if err != nil {
		return nil, err
	}
	if p.Workspace == nil {
		p.Workspace = &asana.Ref{GID: ws}
	}
	if err := s.saveProject(ctx, p); err != nil {
		return nil, err
	}
	if secs, err := s.API.Sections(ctx, p.GID); err == nil {
		_ = s.Store.ReplaceSections(ctx, p.GID, secs)
	}
	return p, nil
}

// SetProjectArchived archives (or unarchives) a project.
func (s *Syncer) SetProjectArchived(ctx context.Context, gid string, archived bool) error {
	if s.API == nil {
		return ErrOffline
	}
	if _, err := s.API.SetProjectArchived(ctx, gid, archived); err != nil {
		return err
	}
	return s.Store.SetProjectArchivedLocal(ctx, gid, archived)
}

// SetProjectMembers adds and removes project members.
func (s *Syncer) SetProjectMembers(ctx context.Context, gid string, add, remove []string) error {
	if s.API == nil {
		return ErrOffline
	}
	var p *asana.Project
	var err error
	if len(add) > 0 {
		if p, err = s.API.AddMembers(ctx, gid, add); err != nil {
			return err
		}
	}
	if len(remove) > 0 {
		if p, err = s.API.RemoveMembers(ctx, gid, remove); err != nil {
			return err
		}
	}
	if p == nil {
		return nil
	}
	if p.Members == nil { // some responses omit members: fetch the project
		if p, err = s.API.Project(ctx, gid); err != nil {
			return err
		}
	}
	return s.saveProject(ctx, p)
}

// FetchStatusUpdates refreshes a project's status updates.
func (s *Syncer) FetchStatusUpdates(ctx context.Context, projectGID string) error {
	if s.API == nil {
		return ErrOffline
	}
	updates, err := s.API.StatusUpdates(ctx, projectGID)
	if err != nil {
		return err
	}
	return s.Store.ReplaceStatusUpdates(ctx, projectGID, updates)
}

func (s *Syncer) pushStatus(ctx context.Context, op store.Op) error {
	str := func(k string) string { v, _ := op.Payload[k].(string); return v }
	u, err := s.API.CreateStatusUpdate(ctx, op.TaskGID, str("status_type"), str("title"), str("text"), str("html_text"))
	if err != nil {
		return err
	}
	if err := s.Store.ReplaceLocalStatus(ctx, str("status"), *u); err != nil {
		return err
	}
	return s.Store.CompleteOp(ctx, op.ID)
}
