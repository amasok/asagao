package tui

import (
	"context"
	"time"

	"github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/sync"
)

// Store is the local cache as the TUI uses it: the ports of the use cases and of the sync
// engine, and what the screen writes itself (its collapsed groups, a reset, the dropped
// changes it has shown). Store satisfies it; the TUI does not import the store.
type Store interface {
	app.TaskStore
	app.CommentStore
	app.StatusStore
	app.ProjectStore
	app.BriefStore
	app.RelationStore
	app.ProjectFieldStore
	app.SectionStore
	app.TaskOpsStore
	app.TagStore
	app.CopyStore
	app.NotificationStore
	app.OutboxStore
	app.QueryStore
	app.TimeTrackingStore
	app.GoalStore
	app.QuickAddStore
	sync.LocalStore

	CollapsedGroups(ctx context.Context, scope string) (string, error)
	SetCollapsedGroups(ctx context.Context, scope, raw string) error
	LayoutState(ctx context.Context) (string, error)
	SetLayoutState(ctx context.Context, raw string) error
	ResetWithBackup(ctx context.Context, backupDir string, upTo int64, now time.Time) (readmodel.ResetResult, error)
	MarkDiscardsNotified(ctx context.Context, ids []int64) error
}
