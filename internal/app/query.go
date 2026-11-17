package app

import (
	"context"
	"time"

	"github.com/amasok/asagao/internal/domain/outbox"
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
}

// Query is what the entry points read from the local cache, as the store returns it. It
// reads nothing from Asana and writes nothing. Each method is the store's of the same
// name (docs/design/adr/0009-read-ports.md).
type Query struct {
	Store QueryStore
}

// ListTasks returns the tasks f selects.
func (q Query) ListTasks(ctx context.Context, f readmodel.TaskFilter) ([]readmodel.TaskSummary, error) {
	return q.Store.ListTasks(ctx, f)
}

// BuildFilter resolves the names and relative dates of a task list query, at now.
func (q Query) BuildFilter(ctx context.Context, spec readmodel.FilterSpec, now time.Time) (readmodel.TaskFilter, error) {
	return q.Store.BuildFilter(ctx, spec, now)
}

// GetTask returns a task's detail.
func (q Query) GetTask(ctx context.Context, gid string) (*readmodel.TaskDetail, error) {
	return q.Store.GetTask(ctx, gid)
}

// TaskSummaries returns the cached tasks among gids, by gid.
func (q Query) TaskSummaries(ctx context.Context, gids []string) (map[string]readmodel.TaskSummary, error) {
	return q.Store.TaskSummaries(ctx, gids)
}

// TaskFollowers returns a task's followers.
func (q Query) TaskFollowers(ctx context.Context, taskGID string) ([]readmodel.UserRef, error) {
	return q.Store.TaskFollowers(ctx, taskGID)
}

// GetAttachment returns one cached attachment.
func (q Query) GetAttachment(ctx context.Context, gid string) (*readmodel.AttachmentView, error) {
	return q.Store.GetAttachment(ctx, gid)
}

// RealGID returns the Asana gid of a task created offline, or gid itself if it is not a
// local gid or has not been sent yet.
func (q Query) RealGID(ctx context.Context, gid string) (string, error) {
	return q.Store.RealGID(ctx, gid)
}

// ListTags returns the workspace's tags, with how many tasks have them.
func (q Query) ListTags(ctx context.Context) ([]readmodel.TagCount, error) {
	return q.Store.ListTags(ctx)
}

// ListFieldOptions returns the enum options that incomplete tasks use, by field.
func (q Query) ListFieldOptions(ctx context.Context) ([]readmodel.FieldOption, error) {
	return q.Store.ListFieldOptions(ctx)
}

// ResolveCustomField finds a custom field of a task by gid or name.
func (q Query) ResolveCustomField(ctx context.Context, taskGID, arg string) (readmodel.CustomFieldView, error) {
	return q.Store.ResolveCustomField(ctx, taskGID, arg)
}

// ListUsers returns the cached users.
func (q Query) ListUsers(ctx context.Context) ([]readmodel.UserRef, error) {
	return q.Store.ListUsers(ctx)
}

// MeGID returns the logged-in user's gid.
func (q Query) MeGID(ctx context.Context) (string, error) {
	return q.Store.MeGID(ctx)
}

// ResolveUser turns "me", "none"/"" (unassigned), a gid, or a unique (partial) name or
// email into a user gid ("" for unassigned).
func (q Query) ResolveUser(ctx context.Context, arg string) (string, error) {
	return q.Store.ResolveUser(ctx, arg)
}

// ResolveUsers resolves several users as ResolveUser does.
func (q Query) ResolveUsers(ctx context.Context, args []string) ([]string, error) {
	return q.Store.ResolveUsers(ctx, args)
}

// ListProjects returns the cached projects that are not archived (only those the user is
// a member of, if memberOnly).
func (q Query) ListProjects(ctx context.Context, memberOnly bool) ([]readmodel.ProjectView, error) {
	return q.Store.ListProjects(ctx, memberOnly)
}

// ResolveProject finds a project by gid, exact name, or unique partial name.
func (q Query) ResolveProject(ctx context.Context, arg string) (string, error) {
	return q.Store.ResolveProject(ctx, arg)
}

// ProjectMembers returns a project's members.
func (q Query) ProjectMembers(ctx context.Context, projectGID string) ([]readmodel.UserRef, error) {
	return q.Store.ProjectMembers(ctx, projectGID)
}

// StatusUpdates returns a project's status updates.
func (q Query) StatusUpdates(ctx context.Context, projectGID string) ([]readmodel.StatusUpdateView, error) {
	return q.Store.StatusUpdates(ctx, projectGID)
}

// InboxState says whether the notifications can be shown.
func (q Query) InboxState(ctx context.Context) (readmodel.InboxState, error) {
	return q.Store.InboxState(ctx)
}

// ListNotifications returns the cached notifications f selects.
func (q Query) ListNotifications(ctx context.Context, f readmodel.NotificationFilter) ([]readmodel.Notification, error) {
	return q.Store.ListNotifications(ctx, f)
}

// UnreadNotifications counts the unread notifications.
func (q Query) UnreadNotifications(ctx context.Context) (int, error) {
	return q.Store.UnreadNotifications(ctx)
}

// Stats summarises what is in the cache.
func (q Query) Stats(ctx context.Context) (readmodel.Stats, error) {
	return q.Store.Stats(ctx)
}

// OutboxCounts counts the outbox entries by state.
func (q Query) OutboxCounts(ctx context.Context) (pending, conflict, failed int, err error) {
	return q.Store.OutboxCounts(ctx)
}

// ListOps returns the outbox entries.
func (q Query) ListOps(ctx context.Context) ([]outbox.Op, error) {
	return q.Store.ListOps(ctx)
}

// UnsentCount counts the outbox entries not in Asana (any state).
func (q Query) UnsentCount(ctx context.Context) (int, error) {
	return q.Store.UnsentCount(ctx)
}

// UnsentState returns how many changes are unsent and the id of the newest one (0 if none).
func (q Query) UnsentState(ctx context.Context) (n int, newest int64, err error) {
	return q.Store.UnsentState(ctx)
}

// PendingDiscards returns the dropped changes not shown to the user yet, oldest first.
func (q Query) PendingDiscards(ctx context.Context) ([]readmodel.Discard, error) {
	return q.Store.PendingDiscards(ctx)
}

// RecentDiscards returns the last limit dropped changes, notified or not, newest first.
func (q Query) RecentDiscards(ctx context.Context, limit int) ([]readmodel.Discard, error) {
	return q.Store.RecentDiscards(ctx, limit)
}
