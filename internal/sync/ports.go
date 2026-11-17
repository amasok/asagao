package sync

import (
	"context"
	"time"

	"github.com/amasok/asagao/internal/asana/model"
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
	Attachment(ctx context.Context, gid string) (*model.Attachment, error)
	Attachments(ctx context.Context, parentGID string) ([]model.Attachment, error)
	CreateProject(ctx context.Context, workspaceGID, teamGID, name string) (*model.Project, error)
	CreateStatusUpdate(ctx context.Context, projectGID, statusType, title, text, htmlText string) (*model.StatusUpdate, error)
	CreateSubtask(ctx context.Context, parentGID string, fields map[string]any) (*model.Task, error)
	CreateTag(ctx context.Context, workspaceGID, name string) (*model.Ref, error)
	CreateTask(ctx context.Context, fields map[string]any) (*model.Task, error)
	CustomFieldSettings(ctx context.Context, projectGID string) ([]model.CustomFieldSetting, error)
	Dependencies(ctx context.Context, taskGID string) ([]model.TaskRef, error)
	Dependents(ctx context.Context, taskGID string) ([]model.TaskRef, error)
	DuplicateTask(ctx context.Context, taskGID, name string) (*model.Ref, error)
	Events(ctx context.Context, resource, token string) (model.EventPage, error)
	MarkNotifications(ctx context.Context, workspaceGID string, gids []string, read bool) (failed []string, err error)
	Me(ctx context.Context) (*model.User, error)
	Notifications(ctx context.Context, workspaceGID string, max int) ([]model.Notification, error)
	Project(ctx context.Context, gid string) (*model.Project, error)
	Projects(ctx context.Context, workspaceGID string, archived bool) ([]model.Project, error)
	RemoveDependencies(ctx context.Context, taskGID string, deps []string) error
	RemoveFollowers(ctx context.Context, taskGID string, users []string) error
	RemoveMembers(ctx context.Context, projectGID string, users []string) (*model.Project, error)
	RemoveProject(ctx context.Context, taskGID, projectGID string) error
	RemoveTag(ctx context.Context, taskGID, tagGID string) error
	Section(ctx context.Context, gid string) (*model.Section, error)
	Sections(ctx context.Context, projectGID string) ([]model.Section, error)
	SetProjectArchived(ctx context.Context, projectGID string, archived bool) (*model.Project, error)
	StatusUpdates(ctx context.Context, projectGID string) ([]model.StatusUpdate, error)
	Stories(ctx context.Context, taskGID string) ([]model.Story, error)
	Subtasks(ctx context.Context, taskGID string) ([]model.Task, error)
	Tags(ctx context.Context, workspaceGID string) ([]model.Ref, error)
	Task(ctx context.Context, gid string) (*model.Task, error)
	TaskDetail(ctx context.Context, gid string) (*model.Task, error)
	Tasks(ctx context.Context, tq model.TaskQuery) ([]model.Task, error)
	TasksByGID(ctx context.Context, gids []string) (tasks []model.Task, gone []string, err error)
	UpdateSection(ctx context.Context, gid, name string) (*model.Section, error)
	UpdateTask(ctx context.Context, gid string, fields map[string]any) (*model.Task, error)
	UploadAttachment(ctx context.Context, parentGID, fileName string, data []byte) (*model.Attachment, error)
	Users(ctx context.Context, workspaceGID string) ([]model.User, error)
	Workspaces(ctx context.Context) ([]model.Workspace, error)
}
