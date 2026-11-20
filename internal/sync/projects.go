package sync

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/domain/tag"
	"github.com/amasok/asagao/internal/readmodel"
)

// Project operations that need Asana right away (creating, archiving, members). They are
// not queued: tasks may be created in a new project immediately, which an offline
// placeholder project could not support. The store is updated with Asana's response.
// Changes to a project's fields (description, owner, dates, color) are queued like task
// edits (update_project, sent by pushUpdateProject); the overview's progress and brief
// are fetched when it is opened (FetchProjectOverview).

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

// CreateProject creates a project in team teamGID ("" for none, in a workspace that is
// not an organization) of the default workspace, and stores it with its sections.
func (s *Syncer) CreateProject(ctx context.Context, name, teamGID string) (*model.Project, error) {
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
	p, err := s.API.CreateProject(ctx, ws, teamGID, name)
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
	// A new project has no tasks yet: it is synced from now on, not shown as unfetched (#54).
	if err := s.Store.SetProjectTaskState(ctx, p.GID, readmodel.TasksAll); err != nil {
		return nil, err
	}
	return p, nil
}

// recoverCreateProject handles a failed CreateProject that may have created the project
// anyway (a 5xx, a dropped connection). If Asana has exactly one project of that name
// created since started, that is it. Asana may need a moment to list a new project, so it
// looks twice, projectSettle apart. If it is still not there, the project is not created
// again (that could make a duplicate); the error says to check after the next sync.
func (s *Syncer) recoverCreateProject(ctx context.Context, ws, name string, started time.Time, cause error) (*model.Project, error) {
	if notMade(ctx, cause) {
		return nil, cause // not created
	}
	return s.findCreatedProject(ctx, ws, name, started, cause,
		"プロジェクトが作成されたか、まだ分かりません。次の同期の後に一覧に無ければ、もう一度作成してください")
}

// notMade reports whether a failed request surely made nothing: Asana refused it (4xx),
// it was not sent, or ctx ended before.
func notMade(ctx context.Context, cause error) bool {
	var apiErr *model.Error
	return (errors.As(cause, &apiErr) && apiErr.Status < 500) || requestNotSent(cause) || ctx.Err() != nil
}

// findCreatedProject looks in Asana for the one project named name created since started
// (twice, projectSettle apart). Not there, the error is notFound wrapping cause (cause
// alone if notFound is "").
func (s *Syncer) findCreatedProject(ctx context.Context, ws, name string, started time.Time, cause error, notFound string) (*model.Project, error) {
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
	if notFound == "" {
		return nil, cause
	}
	return nil, fmt.Errorf("%s: %w", notFound, cause)
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

// FetchProjectOverview refreshes what a project's overview shows that a sync does not
// fetch: the project itself (its notes, owner and dates), how many of its tasks are done,
// and its brief. The brief is a preview API that Asana may refuse (402 on some plans, or
// 403): it is then recorded as unavailable and not shown, and the workspace is marked so
// that it is not asked for there for a while (brief.go). Whatever was fetched is stored
// even if a later request fails; the error is the first failure.
func (s *Syncer) FetchProjectOverview(ctx context.Context, projectGID string) error {
	if s.API == nil {
		return ErrOffline
	}
	p, err := s.API.Project(ctx, projectGID)
	if err != nil {
		return err
	}
	if err := s.saveProject(ctx, p); err != nil {
		return err
	}
	var fail error
	counts, err := s.API.TaskCounts(ctx, projectGID)
	if err != nil {
		counts, fail = nil, err
	}
	state := readmodel.BriefNone
	var brief *model.ProjectBrief
	hidden, err := s.briefHidden(ctx, projectGID)
	if err != nil {
		return err
	}
	switch {
	case hidden: // refused lately in this workspace: not asked again for a while (brief.go)
		state = readmodel.BriefUnavailable
	case p.ProjectBrief != nil && p.ProjectBrief.GID != "":
		brief, err = s.serverBrief(ctx, p)
		switch {
		case err == nil && brief == nil: // deleted meanwhile
		case err == nil:
			state = readmodel.BriefAvailable
			if err := s.clearBriefRefusal(ctx, projectGID); err != nil {
				return err
			}
		case briefRefused(err):
			state, brief = readmodel.BriefUnavailable, nil
			_ = s.briefRefusal(ctx, projectGID, err) // 402: for the workspace; 403: this project
		default:
			state, brief = "", nil // keep the cached brief
			if fail == nil {
				fail = err
			}
		}
	}
	if err := s.Store.SaveProjectOverview(ctx, projectGID, counts, state, brief, s.clock()); err != nil {
		return err
	}
	return fail
}

// pushUpdateProject sends a project edit, after checking that nobody changed the same
// fields in Asana since it was made here (like a task's update).
func (s *Syncer) pushUpdateProject(ctx context.Context, op outbox.Op) (outcome, error) {
	server, err := s.API.Project(ctx, op.TaskGID)
	if err != nil {
		return sent, err
	}
	current := projectFieldValues(server)
	conflicts := map[string]any{}
	for k, local := range op.Payload {
		if k == "html_notes" {
			continue // the server normalizes HTML; "notes" (plain) is compared instead
		}
		sv, known := current[k]
		base := op.Base[k]
		if k == "color" { // "none" ≡ "", and Asana renames the written names (dark-red is red)
			sv, base, local = canonColor(sv), canonColor(base), canonColor(local)
		}
		if known && !same(sv, base) && !same(sv, local) {
			conflicts[k] = sv
		}
	}
	if len(conflicts) > 0 {
		return conflicted, s.Store.MarkOpConflict(ctx, op.ID, conflicts)
	}
	changes := apiFields(op.Payload)
	for _, k := range []string{"owner", "start_on"} {
		if v, ok := changes[k]; ok && v == "" {
			changes[k] = nil
		}
	}
	if _, ok := changes["start_on"]; ok {
		if _, ok := changes["due_on"]; !ok {
			// Asana needs the due date with a start date: send the one it has.
			changes["due_on"] = nullIfEmpty(server.DueOn)
		}
	}
	p, err := s.API.UpdateProject(ctx, op.TaskGID, changes)
	if err != nil {
		return sent, err
	}
	if err := s.Store.CompleteOp(ctx, op.ID); err != nil {
		return sent, sentNotSaved(err)
	}
	if p.Workspace == nil {
		p.Workspace = server.Workspace
	}
	_ = s.saveProject(ctx, p) // the next sync fixes a failure
	return sent, nil
}

// projectFieldValues are a project's values of the fields an update_project changes, as
// the store keeps them.
func projectFieldValues(p *model.Project) map[string]any {
	owner := ""
	if p.Owner != nil {
		owner = p.Owner.GID
	}
	return map[string]any{"notes": p.Notes, "owner": owner, "start_on": p.StartOn, "due_on": p.DueOn, "color": p.Color}
}

// canonColor compares colors as Asana's GET names them (tag.CanonicalColor): no color
// ("" / "none") is "", and a written name is its new name (dark-red is red).
func canonColor(v any) any {
	if s, ok := v.(string); ok {
		return tag.CanonicalColor(s)
	}
	return v
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
