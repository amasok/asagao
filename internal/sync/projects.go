package sync

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/domain/outbox"
)

// Project operations that need Asana right away (creating, archiving, members). They are
// not queued: tasks may be created in a new project immediately, which an offline
// placeholder project could not support. The store is updated with Asana's response.

// ErrOffline is returned by online-only operations when there is no API client.
var ErrOffline = errors.New("オンラインのときだけできる操作です")

func (s *Syncer) saveProject(ctx context.Context, p *model.Project) error {
	me, _ := s.Store.MeGID(ctx)
	ws := ""
	if p.Workspace != nil {
		ws = p.Workspace.GID
	}
	return s.Store.UpsertProjects(ctx, ws, []model.Project{*p}, me)
}

// CreateProject creates a project (in the default workspace) and stores it with its sections.
func (s *Syncer) CreateProject(ctx context.Context, name string) (*model.Project, error) {
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
	started := s.clock()
	p, err := s.API.CreateProject(ctx, ws, "", name)
	if err != nil {
		if p, err = s.recoverCreateProject(ctx, ws, name, started, err); err != nil {
			return nil, err
		}
	}
	if p.Workspace == nil {
		p.Workspace = &model.Ref{GID: ws}
	}
	if err := s.saveProject(ctx, p); err != nil {
		return nil, err
	}
	if secs, err := s.API.Sections(ctx, p.GID); err == nil {
		_ = s.Store.ReplaceSections(ctx, p.GID, secs)
	}
	return p, nil
}

// recoverCreateProject handles a failed CreateProject that may have created the project
// anyway (a 5xx, a dropped connection). If Asana has exactly one project of that name
// created since started, that is it. Asana may need a moment to list a new project, so it
// looks twice, projectSettle apart. If it is still not there, the project is not created
// again (that could make a duplicate); the error says to check after the next sync.
func (s *Syncer) recoverCreateProject(ctx context.Context, ws, name string, started time.Time, cause error) (*model.Project, error) {
	var apiErr *model.Error
	if (errors.As(cause, &apiErr) && apiErr.Status < 500) || requestNotSent(cause) || ctx.Err() != nil {
		return nil, cause // not created
	}
	for try := 0; try < 2; try++ {
		if try > 0 {
			if err := s.wait(ctx, projectSettle); err != nil {
				return nil, cause
			}
		}
		projects, err := s.API.Projects(ctx, ws, false)
		if err != nil {
			return nil, fmt.Errorf("プロジェクトが作成されたか確かめられませんでした (%v): %w", err, cause)
		}
		// By Asana's clock, like created_at. Measured only now: a dropped connection may
		// have been this process's first request, and the list's answer has a Date header.
		since := started.Add(s.API.ClockOffset()).Add(-clockSkew)
		p, m := pick(projects, func(p model.Project) bool { return p.Name == name && !p.CreatedAt.Before(since) })
		switch m {
		case matched:
			if p.Workspace == nil {
				p.Workspace = &model.Ref{GID: ws}
			}
			return &p, nil
		case ambiguous:
			return nil, fmt.Errorf("同じ名前のプロジェクトが複数あります。Asana で確認してください: %w", cause)
		}
	}
	return nil, fmt.Errorf("プロジェクトが作成されたか、まだ分かりません。次の同期の後に一覧に無ければ、もう一度作成してください: %w", cause)
}

const projectSettle = 5 * time.Second

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
	var p *model.Project
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

func (s *Syncer) pushStatus(ctx context.Context, op outbox.Op) error {
	str := func(k string) string { v, _ := op.Payload[k].(string); return v }
	u, err := s.API.CreateStatusUpdate(ctx, op.TaskGID, str("status_type"), str("title"), str("text"), str("html_text"))
	if err != nil {
		return err
	}
	return sentNotSaved(s.Store.CompleteStatus(ctx, op.ID, str("status"), *u))
}
