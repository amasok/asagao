package sync

import (
	"context"
	"time"

	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/domain/rich"
	"github.com/amasok/asagao/internal/readmodel"
)

// AsanaAPI is the part of the Asana REST API that syncing uses. *asana.Client satisfies
// it; the types are asana/model's.
type AsanaAPI interface {
	// ClockOffset is how far Asana's clock is ahead of ours, and ClockKnown whether it
	// has been measured (see asana.Client).
	ClockOffset() time.Duration
	ClockKnown() bool

	AddComment(ctx context.Context, taskGID, text, htmlText string) (*model.Story, error)
	AddDependencies(ctx context.Context, taskGID string, deps []string) error
	AddFollowers(ctx context.Context, taskGID string, users []string) error
	AddMembers(ctx context.Context, projectGID string, users []string) (*model.Project, error)
	AddProject(ctx context.Context, taskGID, projectGID, sectionGID string) error
	AddTag(ctx context.Context, taskGID, tagGID string) error
	AddTaskToSection(ctx context.Context, sectionGID, taskGID string) error
	Batch(ctx context.Context, writes []model.BatchWrite) ([]model.BatchWriteResult, error)
	Attachment(ctx context.Context, gid string) (*model.Attachment, error)
	Attachments(ctx context.Context, parentGID string) ([]model.Attachment, error)
	CreateProject(ctx context.Context, workspaceGID, teamGID, name string) (*model.Project, error)
	CreateStatusUpdate(ctx context.Context, projectGID, statusType, title, text, htmlText string) (*model.StatusUpdate, error)
	CreateSubtask(ctx context.Context, parentGID string, fields map[string]any) (*model.Task, error)
	CreateSection(ctx context.Context, projectGID, name, before, after string) (*model.Section, error)
	CreateTag(ctx context.Context, workspaceGID, name string) (*model.Ref, error)
	DeleteSection(ctx context.Context, gid string) error
	DeleteTag(ctx context.Context, gid string) error
	InsertSection(ctx context.Context, projectGID, sectionGID, before, after string) error
	Tag(ctx context.Context, gid string) (*model.Tag, error)
	UpdateTag(ctx context.Context, gid string, changes map[string]any) (*model.Tag, error)
	UserTaskList(ctx context.Context, userGID, workspaceGID string) (*model.Ref, error)
	CreateTask(ctx context.Context, fields map[string]any) (*model.Task, error)
	CustomFieldSettings(ctx context.Context, projectGID string) ([]model.CustomFieldSetting, error)
	DeleteAttachment(ctx context.Context, gid string) error
	DeleteStory(ctx context.Context, gid string) error
	DeleteTask(ctx context.Context, taskGID string) error
	Dependencies(ctx context.Context, taskGID string) ([]model.TaskRef, error)
	Dependents(ctx context.Context, taskGID string) ([]model.TaskRef, error)
	DuplicateTask(ctx context.Context, taskGID, name string) (*model.Ref, error)
	Events(ctx context.Context, resource, token string) (model.EventPage, error)
	InsertTaskInSection(ctx context.Context, sectionGID, taskGID string, pos model.Position) error
	FavoriteProjects(ctx context.Context, workspaceGID string) ([]model.Ref, error)
	MarkNotifications(ctx context.Context, workspaceGID string, gids []string, read bool) (failed []string, err error)
	Me(ctx context.Context) (*model.User, error)
	MyTeams(ctx context.Context, workspaceGID string) ([]model.Team, error)
	Notifications(ctx context.Context, workspaceGID string, max int) ([]model.Notification, error)
	Project(ctx context.Context, gid string) (*model.Project, error)
	ProjectBrief(ctx context.Context, briefGID string) (*model.ProjectBrief, error)
	Projects(ctx context.Context, workspaceGID string, archived bool) ([]model.Project, error)
	RemoveDependencies(ctx context.Context, taskGID string, deps []string) error
	RemoveFollowers(ctx context.Context, taskGID string, users []string) error
	RemoveMembers(ctx context.Context, projectGID string, users []string) (*model.Project, error)
	RemoveProject(ctx context.Context, taskGID, projectGID string) error
	RemoveTag(ctx context.Context, taskGID, tagGID string) error
	Section(ctx context.Context, gid string) (*model.Section, error)
	Sections(ctx context.Context, projectGID string) ([]model.Section, error)
	SetParent(ctx context.Context, taskGID, parentGID string, pos model.Position) error
	SetProjectArchived(ctx context.Context, projectGID string, archived bool) (*model.Project, error)
	StatusUpdates(ctx context.Context, projectGID string) ([]model.StatusUpdate, error)
	Stories(ctx context.Context, taskGID string) ([]model.Story, error)
	Subtasks(ctx context.Context, taskGID string) ([]model.Task, error)
	Tags(ctx context.Context, workspaceGID string) ([]model.Tag, error)
	Task(ctx context.Context, gid string) (*model.Task, error)
	TaskCounts(ctx context.Context, projectGID string) (*model.TaskCounts, error)
	TaskDetail(ctx context.Context, gid string) (*model.Task, error)
	Tasks(ctx context.Context, tq model.TaskQuery) ([]model.Task, error)
	TasksByGID(ctx context.Context, gids []string) (tasks []model.Task, gone []string, err error)
	UpdateProject(ctx context.Context, gid string, changes map[string]any) (*model.Project, error)
	UpdateSection(ctx context.Context, gid, name string) (*model.Section, error)
	UpdateStory(ctx context.Context, gid, text, htmlText string) (*model.Story, error)
	UpdateTask(ctx context.Context, gid string, fields map[string]any) (*model.Task, error)
	UploadAttachment(ctx context.Context, parentGID, fileName string, data []byte) (*model.Attachment, error)
	Users(ctx context.Context, workspaceGID string) ([]model.User, error)
	Workspaces(ctx context.Context) ([]model.Workspace, error)
}

// LocalStore is the part of the local cache that syncing uses. *store.Store satisfies it;
// its types are domain/outbox's, readmodel's and asana/model's.
type LocalStore interface {
	ApplySectionName(ctx context.Context, gid, name string) error
	ApplyStory(ctx context.Context, gid, text, htmlText string) error
	ApplyTag(ctx context.Context, t model.Tag) error
	ReplaceMyTaskSections(ctx context.Context, workspaceGID string, secs []model.Section) error
	CompleteCreateSection(ctx context.Context, id int64, local, real string) error
	RealSectionGID(ctx context.Context, gid string) (string, error)
	SectionCreateQueued(ctx context.Context, gid string) (bool, error)
	SetOpPayload(ctx context.Context, id int64, key string, value any) error
	TagCreateQueued(ctx context.Context, tag string) (bool, error)
	BeginSend(ctx context.Context, id int64, now time.Time) (bool, error)
	ClearVerify(ctx context.Context, id int64) error
	CompleteComment(ctx context.Context, id int64, localGID, taskGID string, st model.Story) error
	CompleteDelete(ctx context.Context, id int64, gid string) error
	// RemoveAttachmentFile deletes a local attachment file, only inside the store's
	// attachments folder.
	RemoveAttachmentFile(path string) error
	CompleteCreate(ctx context.Context, id int64, local, real string, move *outbox.MoveTarget) error
	CompleteOp(ctx context.Context, id int64) error
	CompleteStatus(ctx context.Context, id int64, local string, u model.StatusUpdate) error
	CompleteUpload(ctx context.Context, id int64, local, real, viewURL string) error
	CountAttempt(ctx context.Context, id int64, now time.Time) error
	CreateQueued(ctx context.Context, gid string) (bool, error)
	DefaultWorkspace(ctx context.Context) (string, error)
	DeleteGoneTasks(ctx context.Context, gids []string) (int64, error)
	DiscardDeletedTask(ctx context.Context, taskGID, reason string, now time.Time) ([]readmodel.Discard, error)
	DiscardGoneOp(ctx context.Context, id int64, reason string, now time.Time) (readmodel.Discard, error)
	DropNotificationMarks(ctx context.Context, also int64) (int, error)
	FitQueuedRichText(ctx context.Context, fit func(target rich.Target, html string) (fitted, plain string, changed bool)) (int, error)
	GetAttachment(ctx context.Context, gid string) (*readmodel.AttachmentView, error)
	GetOp(ctx context.Context, id int64) (*outbox.Op, error)
	GetTask(ctx context.Context, gid string) (*readmodel.TaskDetail, error)
	InboxState(ctx context.Context) (readmodel.InboxState, error)
	LocalResult(ctx context.Context, prefix, gid string) (real string, waiting bool, err error)
	MarkOpConflict(ctx context.Context, id int64, server map[string]any) error
	MarkOpFailed(ctx context.Context, id int64, cause error) error
	MarkOpVerify(ctx context.Context, id int64, note string) error
	MeGID(ctx context.Context) (string, error)
	NotificationTargets(ctx context.Context, since string) (gids []string, recent map[string]bool, err error)
	OutboxCounts(ctx context.Context) (pending, conflict, failed int, err error)
	PendingNotificationMarks(ctx context.Context, op outbox.Op, limit int) ([]outbox.Op, error)
	PendingOps(ctx context.Context) ([]outbox.Op, error)
	PruneProjects(ctx context.Context, workspaceGID string, keep []model.Project) error
	PruneResults(ctx context.Context, now time.Time) (int, error)
	PruneTasks(ctx context.Context, keep map[string]bool) (int64, error)
	PruneWorkspaces(ctx context.Context, keep []model.Workspace) error
	RealGID(ctx context.Context, gid string) (string, error)
	RealTagGID(ctx context.Context, gid string) (string, error)
	RecordedResults(ctx context.Context, prefix string) (map[string]bool, error)
	RecoverInterruptedOps(ctx context.Context) (requeued, verify int, err error)
	ReleaseNotSent(ctx context.Context, id int64) error
	ReleaseOp(ctx context.Context, id int64) error
	ReplaceAttachments(ctx context.Context, taskGID string, atts []model.Attachment) error
	ReplaceDependencies(ctx context.Context, taskGID string, deps, dependents []model.TaskRef) error
	ReplaceFavoriteProjects(ctx context.Context, workspaceGID string, favorites []model.Ref) error
	ReplaceLocalTagGID(ctx context.Context, local, real string) error
	ReplaceNotifications(ctx context.Context, workspaceGID string, ns []model.Notification) error
	ReplaceProjectCustomFields(ctx context.Context, projectGID string, settings []model.CustomFieldSetting) error
	ReplaceSections(ctx context.Context, projectGID string, secs []model.Section) error
	ReplaceStatusUpdates(ctx context.Context, projectGID string, updates []model.StatusUpdate) error
	ReplaceStories(ctx context.Context, taskGID string, stories []model.Story, fetchedAt time.Time) error
	ReplaceTeams(ctx context.Context, workspaceGID string, teams []model.Team) error
	SafeToResend(ctx context.Context, op outbox.Op) (bool, error)
	SaveProjectOverview(ctx context.Context, projectGID string, counts *model.TaskCounts, briefState string, brief *model.ProjectBrief, now time.Time) error
	SetAttachmentPath(ctx context.Context, gid, path string) error
	SetInboxState(ctx context.Context, st readmodel.InboxState) error
	SetInboxWriteForbidden(ctx context.Context, forbidden bool) error
	SetProjectArchivedLocal(ctx context.Context, projectGID string, archived bool) error
	SetSyncState(ctx context.Context, key, value string) error
	SyncState(ctx context.Context, key string) (string, error)
	TryLockPush() (unlock func(), err error)
	UpsertAttachment(ctx context.Context, taskGID string, a model.Attachment) error
	UpsertProjects(ctx context.Context, workspaceGID string, ps []model.Project, meGID string) error
	UpsertTags(ctx context.Context, tags []model.Tag) error
	UpsertTasks(ctx context.Context, tasks []model.Task) error
	UpsertUser(ctx context.Context, u model.User) error
	UpsertUsers(ctx context.Context, users []model.User) error
	UpsertWorkspaces(ctx context.Context, ws []model.Workspace) error
}

// RichText rewrites rich text queued for a comment or a status update before #63 as it is
// written for its target now (see store.FitQueuedRichText). richtext.Converter satisfies it.
type RichText interface {
	Fit(target rich.Target, html string) (fitted, plain string, changed bool)
}
