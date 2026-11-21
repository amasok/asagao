package sync

import (
	"context"
	"time"

	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/domain/rich"
	"github.com/amasok/asagao/internal/readmodel"
)

// AsanaAPI is the part of the Asana REST API that syncing uses, made of what each part
// of a sync uses (see syncer.go). *asana.Client satisfies it; the types are asana/model's.
type AsanaAPI interface {
	PushAPI
	PullAPI
	DetailsAPI
	GoalsAPI
	TemplatesAPI
	SearchAPI
	ProjectsAPI
}

// LocalStore is the part of the local cache that syncing uses, made of what each part of
// a sync uses. *store.Store satisfies it; its types are domain/outbox's, readmodel's and
// asana/model's.
type LocalStore interface {
	PushStore
	PullStore
	DetailsStore
	GoalsStore
	TemplatesStore
	SearchStore
	ProjectsStore
}

// ClockAPI tells how far Asana's clock is ahead of ours (see asana.Client).
type ClockAPI interface {
	ClockOffset() time.Duration
}

// PushAPI is the part of the Asana API Pusher uses: sending the outbox and checking what it sent.
type PushAPI interface {
	ClockAPI
	BriefAPI

	AddComment(ctx context.Context, taskGID string, text string, htmlText string) (*model.Story, error)
	AddCustomFieldSetting(ctx context.Context, projectGID string, fieldGID string) error
	AddDependencies(ctx context.Context, taskGID string, deps []string) error
	AddFollowers(ctx context.Context, taskGID string, users []string) error
	AddProject(ctx context.Context, taskGID string, projectGID string, sectionGID string) error
	AddTag(ctx context.Context, taskGID string, tagGID string) error
	AddTaskToSection(ctx context.Context, sectionGID string, taskGID string) error
	Attachments(ctx context.Context, parentGID string) ([]model.Attachment, error)
	Batch(ctx context.Context, writes []model.BatchWrite) ([]model.BatchWriteResult, error)
	ClockKnown() bool
	CreateEnumOption(ctx context.Context, fieldGID string, name string) (*model.EnumOption, error)
	CreateProjectBrief(ctx context.Context, projectGID string, title string, htmlText string) (*model.ProjectBrief, error)
	CreateSection(ctx context.Context, projectGID string, name string, before string, after string) (*model.Section, error)
	CreateStatusUpdate(ctx context.Context, projectGID string, statusType string, title string, text string, htmlText string) (*model.StatusUpdate, error)
	CreateSubtask(ctx context.Context, parentGID string, fields map[string]any) (*model.Task, error)
	CreateTag(ctx context.Context, workspaceGID string, name string) (*model.Ref, error)
	CreateTask(ctx context.Context, fields map[string]any) (*model.Task, error)
	CreateTimeEntry(ctx context.Context, taskGID string, minutes int, enteredOn string) (*model.TimeEntry, error)
	CustomField(ctx context.Context, gid string) (*model.CustomField, error)
	CustomFieldSettings(ctx context.Context, projectGID string) ([]model.CustomFieldSetting, error)
	DeleteAttachment(ctx context.Context, gid string) error
	DeleteProjectBrief(ctx context.Context, briefGID string) error
	DeleteSection(ctx context.Context, gid string) error
	DeleteStory(ctx context.Context, gid string) error
	DeleteTag(ctx context.Context, gid string) error
	DeleteTask(ctx context.Context, taskGID string) error
	DeleteTimeEntry(ctx context.Context, gid string) error
	DuplicateTask(ctx context.Context, taskGID string, name string) (*model.Ref, error)
	InsertSection(ctx context.Context, projectGID string, sectionGID string, before string, after string) error
	InsertTaskInSection(ctx context.Context, sectionGID string, taskGID string, pos model.Position) error
	MarkNotifications(ctx context.Context, workspaceGID string, gids []string, read bool) (failed []string, err error)
	Me(ctx context.Context) (*model.User, error)
	RemoveCustomFieldSetting(ctx context.Context, projectGID string, fieldGID string) error
	RemoveDependencies(ctx context.Context, taskGID string, deps []string) error
	RemoveFollowers(ctx context.Context, taskGID string, users []string) error
	RemoveProject(ctx context.Context, taskGID string, projectGID string) error
	RemoveTag(ctx context.Context, taskGID string, tagGID string) error
	Section(ctx context.Context, gid string) (*model.Section, error)
	SectionTaskOrder(ctx context.Context, sectionGID string, completedSince time.Time) ([]string, error)
	Sections(ctx context.Context, projectGID string) ([]model.Section, error)
	SetParent(ctx context.Context, taskGID string, parentGID string, pos model.Position) error
	StatusUpdates(ctx context.Context, projectGID string) ([]model.StatusUpdate, error)
	Stories(ctx context.Context, taskGID string) ([]model.Story, error)
	Subtasks(ctx context.Context, taskGID string) ([]model.Task, error)
	Tag(ctx context.Context, gid string) (*model.Tag, error)
	Tags(ctx context.Context, workspaceGID string) ([]model.Tag, error)
	Task(ctx context.Context, gid string) (*model.Task, error)
	Tasks(ctx context.Context, tq model.TaskQuery) ([]model.Task, error)
	TasksByGID(ctx context.Context, gids []string) (tasks []model.Task, gone []string, err error)
	TimeEntries(ctx context.Context, taskGID string) ([]model.TimeEntry, error)
	TimeEntry(ctx context.Context, gid string) (*model.TimeEntry, error)
	UpdateProject(ctx context.Context, gid string, changes map[string]any) (*model.Project, error)
	UpdateProjectBrief(ctx context.Context, briefGID string, changes map[string]any) (*model.ProjectBrief, error)
	UpdateSection(ctx context.Context, gid string, name string) (*model.Section, error)
	UpdateStory(ctx context.Context, gid string, text string, htmlText string) (*model.Story, error)
	UpdateTag(ctx context.Context, gid string, changes map[string]any) (*model.Tag, error)
	UpdateTask(ctx context.Context, gid string, fields map[string]any) (*model.Task, error)
	UpdateTimeEntry(ctx context.Context, gid string, minutes int, enteredOn string) (*model.TimeEntry, error)
	UploadAttachment(ctx context.Context, parentGID string, fileName string, data []byte) (*model.Attachment, error)
}

// PushStore is the part of the cache Pusher uses: the outbox, and what sending changes.
type PushStore interface {
	ProjectSaver
	BriefStore

	ApplySectionName(ctx context.Context, gid string, name string) error
	ApplyStory(ctx context.Context, gid string, text string, htmlText string) error
	ApplyTag(ctx context.Context, t model.Tag) error
	ApplyTimeEntry(ctx context.Context, e model.TimeEntry) error
	BeginSend(ctx context.Context, id int64, now time.Time) (bool, error)
	ClearVerify(ctx context.Context, id int64) error
	CompleteBriefOp(ctx context.Context, id int64, projectGID string, brief *model.ProjectBrief) error
	CompleteComment(ctx context.Context, id int64, localGID string, taskGID string, st model.Story) error
	CompleteCreate(ctx context.Context, id int64, local string, real string, move *outbox.MoveTarget) error
	CompleteCreateEnumOption(ctx context.Context, id int64, local string, real model.EnumOption) error
	CompleteCreateSection(ctx context.Context, id int64, local string, real string) error
	CompleteDelete(ctx context.Context, id int64, gid string) error
	CompleteOp(ctx context.Context, id int64) error
	CompleteStatus(ctx context.Context, id int64, local string, u model.StatusUpdate) error
	CompleteTimeEntry(ctx context.Context, id int64, localGID string, taskGID string, e model.TimeEntry) error
	CompleteUpload(ctx context.Context, id int64, local string, real string, viewURL string) error
	CountAttempt(ctx context.Context, id int64, now time.Time) error
	CreateQueued(ctx context.Context, gid string) (bool, error)
	DefaultWorkspace(ctx context.Context) (string, error)
	DiscardDeletedTask(ctx context.Context, taskGID string, reason string, now time.Time) ([]readmodel.Discard, error)
	DiscardGoneOp(ctx context.Context, id int64, reason string, now time.Time) (readmodel.Discard, error)
	DropNotificationMarks(ctx context.Context, also int64) (int, error)
	EnumOptionCreateQueued(ctx context.Context, gid string) (bool, error)
	FieldSettingQueued(ctx context.Context, taskGID string, fieldGID string) (bool, error)
	FitQueuedRichText(ctx context.Context, fit func(target rich.Target, html string) (fitted string, plain string, changed bool)) (int, error)
	GetOp(ctx context.Context, id int64) (*outbox.Op, error)
	// HoldOp leaves a pending entry this version cannot send in the queue with note.
	HoldOp(ctx context.Context, id int64, note string) error
	InboxState(ctx context.Context) (readmodel.InboxState, error)
	LocalResult(ctx context.Context, kind readmodel.ResultKind, gid string) (real string, waiting bool, err error)
	MarkOpConflict(ctx context.Context, id int64, server map[string]any) error
	MarkOpFailed(ctx context.Context, id int64, cause error) error
	MarkOpVerify(ctx context.Context, id int64, note string) error
	OutboxCounts(ctx context.Context) (pending int, conflict int, failed int, err error)
	PendingNotificationMarks(ctx context.Context, op outbox.Op, limit int) ([]outbox.Op, error)
	PendingOps(ctx context.Context) ([]outbox.Op, error)
	PendingSectionOrders(ctx context.Context, projectGID string) (string, error)
	RealGID(ctx context.Context, gid string) (string, error)
	RealSectionGID(ctx context.Context, gid string) (string, error)
	RealTagGID(ctx context.Context, gid string) (string, error)
	RecordedResults(ctx context.Context, kind readmodel.ResultKind) (map[string]bool, error)
	RecoverInterruptedOps(ctx context.Context) (requeued int, verify int, err error)
	ReleaseNotSent(ctx context.Context, id int64) error
	ReleaseOp(ctx context.Context, id int64) error
	// RemoveAttachmentFile deletes a local attachment file, only inside the store's
	// attachments folder.
	RemoveAttachmentFile(path string) error
	ReplaceLocalTagGID(ctx context.Context, local string, real string) error
	ReplaceProjectCustomFields(ctx context.Context, projectGID string, settings []model.CustomFieldSetting) error
	SafeToResend(ctx context.Context, op outbox.Op) (bool, error)
	SectionCreateQueued(ctx context.Context, gid string) (bool, error)
	SetFieldOptionsLocked(ctx context.Context, fieldGID string, at time.Time) error
	SetInboxWriteForbidden(ctx context.Context, forbidden bool) error
	SetPendingSectionOrders(ctx context.Context, projectGID string, value string) error
	SetProjectCustomFieldsFetchedAt(ctx context.Context, projectGID string, at time.Time) error
	SetProjectFieldsLocked(ctx context.Context, projectGID string, at time.Time) error
	SetSectionTaskOrder(ctx context.Context, sectionGID string, gids []string) error
	SetSectionsExisting(ctx context.Context, id int64, existing []string) error
	SyncedMeGID(ctx context.Context) (string, error)
	TagCreateQueued(ctx context.Context, tag string) (bool, error)
	TagWorkspace(ctx context.Context, tagGID string) (string, error)
	TaskDeletesQueuedIn(ctx context.Context, sectionGID string) (bool, error)
	TaskWorkspace(ctx context.Context, taskGID string) (string, error)
	TryLockPush() (unlock func(), err error)
	UpsertTasks(ctx context.Context, tasks []model.Task) error
	// WithdrawCustomType takes a custom type Asana did not take out of an entry (#59).
	WithdrawCustomType(ctx context.Context, id int64, reason string, applied bool, now time.Time) (*outbox.Op, readmodel.Discard, error)
	// WithdrawStartOn takes a start date the plan lacks out of an entry (#57).
	WithdrawStartOn(ctx context.Context, id int64, reason string, now time.Time) (*outbox.Op, readmodel.Discard, error)
	// RecordStartOnIgnored records a start date Asana answered without keeping (#62).
	RecordStartOnIgnored(ctx context.Context, taskGID, start, reason string, now time.Time) (readmodel.Discard, error)
}

// PullAPI is the part of the Asana API Puller uses to bring the cache up to date.
type PullAPI interface {
	ClockAPI

	CustomFieldSettings(ctx context.Context, projectGID string) ([]model.CustomFieldSetting, error)
	CustomTypes(ctx context.Context, projectGID string) ([]model.CustomType, error)
	Events(ctx context.Context, resource string, token string) (model.EventPage, error)
	FavoriteProjects(ctx context.Context, workspaceGID string) ([]model.Ref, error)
	Me(ctx context.Context) (*model.User, error)
	MyTeams(ctx context.Context, workspaceGID string) ([]model.Team, error)
	Notifications(ctx context.Context, workspaceGID string, max int) ([]model.Notification, error)
	Projects(ctx context.Context, workspaceGID string, archived bool) ([]model.Project, error)
	SectionTaskOrder(ctx context.Context, sectionGID string, completedSince time.Time) ([]string, error)
	Sections(ctx context.Context, projectGID string) ([]model.Section, error)
	StatusUpdates(ctx context.Context, projectGID string) ([]model.StatusUpdate, error)
	SubtaskOrder(ctx context.Context, taskGID string) ([]string, error)
	Subtasks(ctx context.Context, taskGID string) ([]model.Task, error)
	Tags(ctx context.Context, workspaceGID string) ([]model.Tag, error)
	Task(ctx context.Context, gid string) (*model.Task, error)
	Tasks(ctx context.Context, tq model.TaskQuery) ([]model.Task, error)
	TasksByGID(ctx context.Context, gids []string) (tasks []model.Task, gone []string, err error)
	UserTaskList(ctx context.Context, userGID string, workspaceGID string) (*model.Ref, error)
	Users(ctx context.Context, workspaceGID string) ([]model.User, error)
	WorkspaceCustomFields(ctx context.Context, workspaceGID string) ([]model.CustomField, error)
	Workspaces(ctx context.Context) ([]model.Workspace, error)
}

// PullStore is the part of the cache Puller writes what it fetches to.
type PullStore interface {
	PlanStore

	DeleteGoneTasks(ctx context.Context, gids []string) (int64, error)
	EventsToken(ctx context.Context, projectGID string) (string, error)
	GetTask(ctx context.Context, gid string) (*readmodel.TaskDetail, error)
	InboxState(ctx context.Context) (readmodel.InboxState, error)
	LastFullSyncAt(ctx context.Context) (time.Time, error)
	LastPollAt(ctx context.Context) (time.Time, error)
	LastSyncAt(ctx context.Context) (time.Time, error)
	MyTaskState(ctx context.Context, workspaceGID string) (string, error)
	MyTaskStates(ctx context.Context) (map[string]string, error)
	NotificationTargets(ctx context.Context, since string) (gids []string, recent map[string]bool, err error)
	OutboxCounts(ctx context.Context) (pending int, conflict int, failed int, err error)
	PendingSectionOrders(ctx context.Context, projectGID string) (string, error)
	PendingSubtaskOrders(ctx context.Context) (string, error)
	ProjectCustomFieldsFetchedAt(ctx context.Context, projectGID string) (string, error)
	ProjectCustomTypesFetchedAt(ctx context.Context, projectGID string) (string, error)
	ProjectRef(ctx context.Context, gid string) (readmodel.ProjectRef, error)
	ProjectTaskState(ctx context.Context, projectGID string) (string, error)
	ProjectTaskStates(ctx context.Context) (map[string]string, error)
	PruneProjects(ctx context.Context, workspaceGID string, keep []model.Project) error
	PruneResults(ctx context.Context, now time.Time) (int, error)
	PruneTasks(ctx context.Context, keep map[string]bool) (int64, error)
	PruneWorkspaces(ctx context.Context, keep []model.Workspace) error
	ReplaceFavoriteProjects(ctx context.Context, workspaceGID string, favorites []model.Ref) error
	ReplaceMyTaskSections(ctx context.Context, workspaceGID string, secs []model.Section) error
	ReplaceNotifications(ctx context.Context, workspaceGID string, ns []model.Notification) error
	ReplaceProjectCustomFields(ctx context.Context, projectGID string, settings []model.CustomFieldSetting) error
	ReplaceProjectCustomTypes(ctx context.Context, projectGID string, types []model.CustomType) error
	ReplaceSections(ctx context.Context, projectGID string, secs []model.Section) error
	ReplaceStatusUpdates(ctx context.Context, projectGID string, updates []model.StatusUpdate) error
	ReplaceTeams(ctx context.Context, workspaceGID string, teams []model.Team) error
	ReplaceWorkspaceCustomFields(ctx context.Context, workspaceGID string, fields []model.CustomField) error
	SetEventsToken(ctx context.Context, projectGID string, token string) error
	SetInboxState(ctx context.Context, st readmodel.InboxState) error
	SetLastFullSyncAt(ctx context.Context, at time.Time) error
	SetLastPollAt(ctx context.Context, at time.Time) error
	SetLastSyncAt(ctx context.Context, at time.Time) error
	SetMeGID(ctx context.Context, gid string) error
	SetMyTaskState(ctx context.Context, workspaceGID string, state string) error
	SetPendingSectionOrders(ctx context.Context, projectGID string, value string) error
	SetPendingSubtaskOrders(ctx context.Context, value string) error
	SetProjectCustomFieldsFetchedAt(ctx context.Context, projectGID string, at time.Time) error
	SetProjectCustomTypesFetchedAt(ctx context.Context, projectGID string, at time.Time) error
	SetProjectTaskOrder(ctx context.Context, projectGID string, gids []string) error
	SetProjectTaskState(ctx context.Context, projectGID string, state string) error
	SetSectionTaskOrder(ctx context.Context, sectionGID string, gids []string) error
	SetSubtaskOrder(ctx context.Context, parentGID string, gids []string) error
	SetUserTaskListGID(ctx context.Context, workspaceGID string, listGID string) error
	SetWorkspaceCustomFieldsFetchedAt(ctx context.Context, workspaceGID string, at time.Time) error
	SetWorkspaceTeamsFetchedAt(ctx context.Context, workspaceGID string, at time.Time) error
	StoredProjectTaskStates(ctx context.Context) (map[string]string, error)
	SyncedMeGID(ctx context.Context) (string, error)
	UpsertProjects(ctx context.Context, workspaceGID string, ps []model.Project, meGID string) error
	UpsertTags(ctx context.Context, tags []model.Tag) error
	UpsertTasks(ctx context.Context, tasks []model.Task) error
	UpsertUser(ctx context.Context, u model.User) error
	UpsertUsers(ctx context.Context, users []model.User) error
	UpsertWorkspaces(ctx context.Context, ws []model.Workspace) error
	WantProject(ctx context.Context, projectGID string) (string, error)
	WorkspaceCustomFieldsFetchedAt(ctx context.Context, workspaceGID string) (string, error)
	WorkspaceTeamsFetchedAt(ctx context.Context, workspaceGID string) (string, error)
}

// DetailsAPI is the part of the Asana API Details uses: a task's details, a project's overview.
type DetailsAPI interface {
	ClockAPI
	BriefAPI

	Attachment(ctx context.Context, gid string) (*model.Attachment, error)
	Attachments(ctx context.Context, parentGID string) ([]model.Attachment, error)
	Dependencies(ctx context.Context, taskGID string) ([]model.TaskRef, error)
	Dependents(ctx context.Context, taskGID string) ([]model.TaskRef, error)
	StatusUpdates(ctx context.Context, projectGID string) ([]model.StatusUpdate, error)
	Stories(ctx context.Context, taskGID string) ([]model.Story, error)
	Subtasks(ctx context.Context, taskGID string) ([]model.Task, error)
	TaskCounts(ctx context.Context, projectGID string) (*model.TaskCounts, error)
	TaskDetail(ctx context.Context, gid string) (*model.Task, error)
	TimeEntries(ctx context.Context, taskGID string) ([]model.TimeEntry, error)
}

// DetailsStore is the part of the cache Details uses.
type DetailsStore interface {
	ProjectSaver
	BriefStore

	GetAttachment(ctx context.Context, gid string) (*readmodel.AttachmentView, error)
	GetTask(ctx context.Context, gid string) (*readmodel.TaskDetail, error)
	ReplaceAttachments(ctx context.Context, taskGID string, atts []model.Attachment) error
	ReplaceDependencies(ctx context.Context, taskGID string, deps []model.TaskRef, dependents []model.TaskRef) error
	ReplaceStatusUpdates(ctx context.Context, projectGID string, updates []model.StatusUpdate) error
	ReplaceStories(ctx context.Context, taskGID string, stories []model.Story, fetchedAt time.Time) error
	ReplaceTimeEntries(ctx context.Context, taskGID string, entries []model.TimeEntry, fetchedAt time.Time) error
	SetAttachmentPath(ctx context.Context, gid string, path string) error
	SetSubtaskOrder(ctx context.Context, parentGID string, gids []string) error
	TaskWorkspace(ctx context.Context, taskGID string) (string, error)
	UpsertAttachment(ctx context.Context, taskGID string, a model.Attachment) error
	UpsertTasks(ctx context.Context, tasks []model.Task) error
}

// GoalsAPI is the part of the Asana API Goals uses: goals and portfolios.
type GoalsAPI interface {
	Goal(ctx context.Context, gid string) (*model.Goal, error)
	GoalRelationships(ctx context.Context, goalGID string) ([]model.GoalRelationship, error)
	Goals(ctx context.Context, workspaceGID string) ([]model.Goal, error)
	Portfolio(ctx context.Context, gid string) (*model.Portfolio, error)
	PortfolioItems(ctx context.Context, portfolioGID string) ([]model.PortfolioItem, error)
	Portfolios(ctx context.Context, workspaceGID string) ([]model.Portfolio, error)
	TimePeriods(ctx context.Context, workspaceGID string) ([]model.TimePeriod, error)
}

// GoalsStore is the part of the cache Goals uses.
type GoalsStore interface {
	PlanStore

	DefaultWorkspace(ctx context.Context) (string, error)
	DeleteGoal(ctx context.Context, gid string) error
	DeletePortfolio(ctx context.Context, gid string) error
	ReplaceGoals(ctx context.Context, workspaceGID string, goals []model.Goal) error
	ReplacePortfolios(ctx context.Context, workspaceGID string, ps []model.Portfolio) error
	ReplaceTimePeriods(ctx context.Context, workspaceGID string, periods []model.TimePeriod) error
	SaveGoal(ctx context.Context, g model.Goal, supports []model.GoalRelationship, now time.Time) error
	SavePortfolio(ctx context.Context, p model.Portfolio, items []model.PortfolioItem, now time.Time) error
	SetGoalsFetchedAt(ctx context.Context, workspaceGID string, at time.Time) error
	SetPortfoliosFetchedAt(ctx context.Context, workspaceGID string, at time.Time) error
}

// TemplatesAPI is the part of the Asana API Templates uses: templates and the jobs they start.
type TemplatesAPI interface {
	ClockAPI
	DuplicateProject(ctx context.Context, projectGID string, name string, teamGID string) (*model.Job, error)
	InstantiateProject(ctx context.Context, templateGID string, in model.ProjectFromTemplate) (*model.Job, error)
	InstantiateTask(ctx context.Context, templateGID string, name string) (*model.Job, error)
	Job(ctx context.Context, gid string) (*model.Job, error)
	MyTeams(ctx context.Context, workspaceGID string) ([]model.Team, error)
	Project(ctx context.Context, gid string) (*model.Project, error)
	ProjectTemplates(ctx context.Context, workspaceGID string, teamGID string) ([]model.ProjectTemplate, error)
	Sections(ctx context.Context, projectGID string) ([]model.Section, error)
	TaskTemplates(ctx context.Context, projectGID string) ([]model.TaskTemplate, error)
	Tasks(ctx context.Context, tq model.TaskQuery) ([]model.Task, error)
}

// TemplatesStore is the part of the cache Templates uses.
type TemplatesStore interface {
	ProjectSaver
	PlanStore

	DefaultWorkspace(ctx context.Context) (string, error)
	ProjectWorkspace(ctx context.Context, projectGID string) (string, error)
	ReplaceSections(ctx context.Context, projectGID string, secs []model.Section) error
	UpsertTasks(ctx context.Context, tasks []model.Task) error
}

// SearchAPI is the part of the Asana API Search uses: search and typeahead.
type SearchAPI interface {
	CustomField(ctx context.Context, gid string) (*model.CustomField, error)
	SearchTasks(ctx context.Context, workspaceGID string, text string, completed *bool, limit int) ([]model.Ref, error)
	TaskByCustomID(ctx context.Context, workspaceGID string, customID string) (*model.Task, error)
	TasksByGID(ctx context.Context, gids []string) (tasks []model.Task, gone []string, err error)
	TypeaheadTags(ctx context.Context, workspaceGID string, query string, count int) ([]model.Tag, error)
	TypeaheadTasks(ctx context.Context, workspaceGID string, query string, count int) ([]model.Ref, error)
	TypeaheadUsers(ctx context.Context, workspaceGID string, query string, count int) ([]model.User, error)
}

// SearchStore is the part of the cache Search uses.
type SearchStore interface {
	PlanStore

	AddTags(ctx context.Context, tags []model.Tag) (int, error)
	AddUsers(ctx context.Context, users []model.User) (int, error)
	DefaultWorkspace(ctx context.Context) (string, error)
	SetCustomIDTaskGID(ctx context.Context, customID string, taskGID string) error
	UpsertTasks(ctx context.Context, tasks []model.Task) error
}

// ProjectsAPI is the part of the Asana API Projects uses: making and changing projects.
type ProjectsAPI interface {
	AddMembers(ctx context.Context, projectGID string, users []string) (*model.Project, error)
	ClockAPI
	CreateProject(ctx context.Context, workspaceGID string, teamGID string, name string) (*model.Project, error)
	Project(ctx context.Context, gid string) (*model.Project, error)
	Projects(ctx context.Context, workspaceGID string, archived bool) ([]model.Project, error)
	RemoveMembers(ctx context.Context, projectGID string, users []string) (*model.Project, error)
	Sections(ctx context.Context, projectGID string) ([]model.Section, error)
	SetProjectArchived(ctx context.Context, projectGID string, archived bool) (*model.Project, error)
}

// ProjectsStore is the part of the cache Projects uses.
type ProjectsStore interface {
	ProjectSaver
	DefaultWorkspace(ctx context.Context) (string, error)
	ReplaceSections(ctx context.Context, projectGID string, secs []model.Section) error
	SetProjectArchivedLocal(ctx context.Context, projectGID string, archived bool) error
	SetProjectTaskState(ctx context.Context, projectGID string, state string) error
}

// BriefAPI reads a project's brief (Pusher and Details, see briefs).
type BriefAPI interface {
	Project(ctx context.Context, gid string) (*model.Project, error)
	ProjectBrief(ctx context.Context, briefGID string) (*model.ProjectBrief, error)
}

// BriefStore records what Asana answered for a project's brief (see briefs).
type BriefStore interface {
	PlanStore
	ProjectWorkspace(ctx context.Context, projectGID string) (string, error)
	SaveProjectOverview(ctx context.Context, projectGID string, counts *model.TaskCounts, briefState string, brief *model.ProjectBrief, now time.Time) error
}

// RichText rewrites rich text queued for a comment or a status update before #63 as it is
// written for its target now (see store.FitQueuedRichText). richtext.Converter satisfies it.
type RichText interface {
	Fit(target rich.Target, html string) (fitted, plain string, changed bool)
}
