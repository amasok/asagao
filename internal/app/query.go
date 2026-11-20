package app

import (
	"context"
	"time"

	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/domain/plan"
	"github.com/amasok/asagao/internal/readmodel"
)

// QueryStore is the part of the local cache that the entry points read to show things:
// lists, a task's detail, the inbox, the outbox. *store.Store satisfies it. Reads that
// only a use case needs (resolving names before a write) stay in the write ports.
type QueryStore interface {
	// tasks
	ListTasks(ctx context.Context, f readmodel.TaskFilter) ([]readmodel.TaskSummary, error)
	BuildFilter(ctx context.Context, spec readmodel.FilterSpec, now time.Time) (readmodel.TaskFilter, error)
	GetTask(ctx context.Context, gid string) (*readmodel.TaskDetail, error)
	TaskSummaries(ctx context.Context, gids []string) (map[string]readmodel.TaskSummary, error)
	TaskFollowers(ctx context.Context, taskGID string) ([]readmodel.UserRef, error)
	TaskTags(ctx context.Context, taskGID string) ([]model.Ref, error)
	GetAttachment(ctx context.Context, gid string) (*readmodel.AttachmentView, error)
	RealGID(ctx context.Context, gid string) (string, error)
	ListTags(ctx context.Context) ([]readmodel.TagCount, error)
	ListFieldOptions(ctx context.Context) ([]readmodel.FieldOption, error)
	ResolveCustomField(ctx context.Context, taskGID, arg string) (readmodel.CustomFieldView, error)

	// users
	ListUsers(ctx context.Context) ([]readmodel.UserRef, error)
	MeGID(ctx context.Context) (string, error)
	ResolveUser(ctx context.Context, arg string) (string, error)
	ResolveUsers(ctx context.Context, args []string) ([]string, error)

	// projects
	ListProjects(ctx context.Context, memberOnly bool) ([]readmodel.ProjectView, error)
	ResolveProject(ctx context.Context, arg string) (string, error)
	ProjectMembers(ctx context.Context, projectGID string) ([]readmodel.UserRef, error)
	StatusUpdates(ctx context.Context, projectGID string) ([]readmodel.StatusUpdateView, error)
	ProjectOverview(ctx context.Context, gid string) (*readmodel.ProjectOverview, error)
	ListProjectsInTeam(ctx context.Context, memberOnly bool, team string) ([]readmodel.ProjectView, error)
	ListTeams(ctx context.Context) ([]readmodel.TeamView, error)
	ResolveTeam(ctx context.Context, arg string) (string, error)
	TeamForNewProject(ctx context.Context, team string) (string, error)

	// inbox
	InboxState(ctx context.Context) (readmodel.InboxState, error)
	ListNotifications(ctx context.Context, f readmodel.NotificationFilter) ([]readmodel.Notification, error)
	UnreadNotifications(ctx context.Context) (int, error)

	// the cache and the outbox
	Stats(ctx context.Context) (readmodel.Stats, error)
	OutboxCounts(ctx context.Context) (pending, conflict, failed int, err error)
	ListOps(ctx context.Context) ([]outbox.Op, error)
	UnsentCount(ctx context.Context) (int, error)
	UnsentState(ctx context.Context) (n int, newest int64, err error)
	PendingDiscards(ctx context.Context) ([]readmodel.Discard, error)
	RecentDiscards(ctx context.Context, limit int) ([]readmodel.Discard, error)
	CountUnfetchedProjects(ctx context.Context, memberOnly bool) (int, error)
	ProjectFields(ctx context.Context, projectGID string) ([]readmodel.ProjectFieldView, error)

	// Workspaces and the 402s recorded for them (the plan reads below).
	PlanRefusedAt(ctx context.Context, feature, workspaceGID string) (time.Time, bool, error)
	WorkspaceGIDs(ctx context.Context) ([]string, error)
	DefaultWorkspace(ctx context.Context) (string, error)
	ProjectWorkspace(ctx context.Context, projectGID string) (string, error)
	TaskWorkspace(ctx context.Context, taskGID string) (string, error)
}

// Query is what the entry points read from the local cache. It reads nothing from Asana
// and writes nothing. The store's reads are QueryStore's, as they are (embedded, without
// a method that only hands them on); Query's own methods are the reads the entry points
// share, which take more than one of them (docs/design/adr/0009-read-ports.md).
type Query struct {
	QueryStore
}

// RecentDays is how far back the recent updates shown in place of the inbox look.
const RecentDays = 7

// RecentSince is the oldest modified_at of the recent updates at now (RecentDays back), as
// TaskFilter.ModifiedAfter takes it.
func RecentSince(now time.Time) string {
	return now.AddDate(0, 0, -RecentDays).UTC().Format(time.RFC3339)
}

// InboxView is the inbox as the entry points show it: its notifications, or, when the
// inbox cannot be read (State.Available is false), the tasks changed in the last
// RecentDays in its place. The entry points word why (sync.InboxUnavailableMessage).
type InboxView struct {
	State         readmodel.InboxState
	Notifications []readmodel.Notification
	Unread        int
	Recent        []readmodel.TaskSummary
}

// Inbox reads the inbox at now: at most limit notifications (0: all), only the unread
// ones if unreadOnly, and how many are unread; or, when the inbox cannot be read, at most
// limit recently changed tasks, newest first. On an error, what was read so far is returned.
func (q Query) Inbox(ctx context.Context, unreadOnly bool, limit int, now time.Time) (InboxView, error) {
	var v InboxView
	var err error
	if v.State, err = q.InboxState(ctx); err != nil {
		return v, err
	}
	if !v.State.Available() {
		v.Recent, err = q.ListTasks(ctx, readmodel.TaskFilter{ModifiedAfter: RecentSince(now), SortByModified: true, Limit: limit})
		return v, err
	}
	if v.Notifications, err = q.ListNotifications(ctx, readmodel.NotificationFilter{UnreadOnly: unreadOnly, Limit: limit}); err != nil {
		return v, err
	}
	v.Unread, err = q.UnreadNotifications(ctx)
	return v, err
}

// SavedSearchFilter is the task filter of the saved search name (spec: its conditions, as
// the caller may have changed them) at now. A condition that cannot be read is a SearchError.
func (q Query) SavedSearchFilter(ctx context.Context, name string, spec readmodel.FilterSpec, now time.Time) (readmodel.TaskFilter, error) {
	f, err := q.BuildFilter(ctx, spec, now)
	if err != nil {
		return f, &SearchError{Name: name, Err: err}
	}
	return f, nil
}

// PlanOffIn reports whether a recent 402 hides feature in workspaceGID at now, and when it
// does, the window [from, until) during which it does. A refusal that cannot be read
// hides nothing: what is shown is offered, and the write checks again.
func (q Query) PlanOffIn(ctx context.Context, feature, workspaceGID string, now time.Time) (from, until time.Time, off bool) {
	at, ok, err := q.PlanRefusedAt(ctx, feature, workspaceGID)
	if err != nil || !ok {
		return time.Time{}, time.Time{}, false
	}
	return plan.Window(at, now)
}

// PlanOffEverywhere reports whether a recent 402 hides feature in every workspace at now
// (not when at least one offers it, or none are known), and the window during which it
// hides it in all of them.
func (q Query) PlanOffEverywhere(ctx context.Context, feature string, now time.Time) (from, until time.Time, off bool) {
	wss, err := q.WorkspaceGIDs(ctx)
	if err != nil || len(wss) == 0 {
		return time.Time{}, time.Time{}, false
	}
	for i, ws := range wss {
		f, u, wsOff := q.PlanOffIn(ctx, feature, ws, now)
		if !wsOff {
			return time.Time{}, time.Time{}, false
		}
		if i == 0 || f.After(from) {
			from = f
		}
		if i == 0 || u.Before(until) {
			until = u
		}
	}
	return from, until, !until.IsZero() && !now.Before(from) && now.Before(until)
}

// StartDateOffered: see the function StartDateOffered.
func (q Query) StartDateOffered(ctx context.Context, gid string, now time.Time) bool {
	return StartDateOffered(ctx, q, gid, now)
}

// TemplatesOffered: see the function TemplatesOffered.
func (q Query) TemplatesOffered(ctx context.Context, feature, workspaceGID string, now time.Time) bool {
	return TemplatesOffered(ctx, q, feature, workspaceGID, now)
}
