package asana

import (
	"context"
	"net/url"
	"strings"
	"time"
)

type Ref struct {
	GID  string `json:"gid"`
	Name string `json:"name,omitempty"`
}

type User struct {
	GID        string `json:"gid"`
	Name       string `json:"name"`
	Email      string `json:"email"`
	Workspaces []Ref  `json:"workspaces,omitempty"`
}

type Workspace struct {
	GID            string `json:"gid"`
	Name           string `json:"name"`
	IsOrganization bool   `json:"is_organization"`
}

type Project struct {
	GID          string    `json:"gid"`
	Name         string    `json:"name"`
	Archived     bool      `json:"archived"`
	Color        string    `json:"color"`
	Notes        string    `json:"notes"`
	Workspace    *Ref      `json:"workspace"`
	Team         *Ref      `json:"team"`
	Members      []Ref     `json:"members"`
	PermalinkURL string    `json:"permalink_url"`
	ModifiedAt   time.Time `json:"modified_at"`
}

type Section struct {
	GID     string `json:"gid"`
	Name    string `json:"name"`
	Project *Ref   `json:"project"`
}

type Membership struct {
	Project *Ref `json:"project"`
	Section *Ref `json:"section"`
}

type Task struct {
	GID             string       `json:"gid"`
	Name            string       `json:"name"`
	Notes           string       `json:"notes"`
	ResourceSubtype string       `json:"resource_subtype"`
	Completed       bool         `json:"completed"`
	CompletedAt     *time.Time   `json:"completed_at"`
	Assignee        *Ref         `json:"assignee"`
	DueOn           string       `json:"due_on"`
	DueAt           *time.Time   `json:"due_at"`
	StartOn         string       `json:"start_on"`
	Parent          *Ref         `json:"parent"`
	Memberships     []Membership `json:"memberships"`
	Tags            []Ref        `json:"tags"`
	NumSubtasks     int          `json:"num_subtasks"`
	Workspace       *Ref         `json:"workspace"`
	PermalinkURL    string       `json:"permalink_url"`
	CreatedAt       time.Time    `json:"created_at"`
	ModifiedAt      time.Time    `json:"modified_at"`
}

// Fields requested for each resource (keep responses small; see requirements 8 章).
var (
	userFields    = []string{"name", "email", "workspaces.name"}
	projectFields = []string{"name", "archived", "color", "notes", "workspace", "team.name", "members", "permalink_url", "modified_at"}
	sectionFields = []string{"name", "project"}
	TaskFields    = []string{
		"name", "notes", "resource_subtype", "completed", "completed_at",
		"assignee.name", "due_on", "due_at", "start_on", "parent.name",
		"memberships.project.name", "memberships.section.name", "tags.name",
		"num_subtasks", "workspace", "permalink_url", "created_at", "modified_at",
	}
)

func fields(f []string) url.Values {
	return url.Values{"opt_fields": {strings.Join(f, ",")}}
}

func (c *Client) Me(ctx context.Context) (*User, error) {
	var u User
	return &u, c.get(ctx, "/users/me", fields(userFields), &u)
}

func (c *Client) Workspaces(ctx context.Context) ([]Workspace, error) {
	return list[Workspace](ctx, c, "/workspaces", fields([]string{"name", "is_organization"}))
}

func (c *Client) Projects(ctx context.Context, workspaceGID string, archived bool) ([]Project, error) {
	q := fields(projectFields)
	q.Set("workspace", workspaceGID)
	if !archived {
		q.Set("archived", "false")
	}
	return list[Project](ctx, c, "/projects", q)
}

func (c *Client) Sections(ctx context.Context, projectGID string) ([]Section, error) {
	return list[Section](ctx, c, "/projects/"+url.PathEscape(projectGID)+"/sections", fields(sectionFields))
}

// TaskQuery selects tasks for GET /tasks. Exactly one of Project or (Assignee + Workspace) must be set.
type TaskQuery struct {
	Project   string
	Assignee  string // "me" or a user gid
	Workspace string
	// CompletedSince includes only incomplete tasks and tasks completed after this time.
	CompletedSince time.Time
	// ModifiedSince includes only tasks modified after this time (incremental sync).
	ModifiedSince time.Time
}

func (c *Client) Tasks(ctx context.Context, tq TaskQuery) ([]Task, error) {
	q := fields(TaskFields)
	if tq.Project != "" {
		q.Set("project", tq.Project)
	} else {
		q.Set("assignee", tq.Assignee)
		q.Set("workspace", tq.Workspace)
	}
	if !tq.CompletedSince.IsZero() {
		q.Set("completed_since", tq.CompletedSince.UTC().Format(time.RFC3339))
	}
	if !tq.ModifiedSince.IsZero() {
		q.Set("modified_since", tq.ModifiedSince.UTC().Format(time.RFC3339))
	}
	return list[Task](ctx, c, "/tasks", q)
}

func (c *Client) Subtasks(ctx context.Context, taskGID string) ([]Task, error) {
	return list[Task](ctx, c, "/tasks/"+url.PathEscape(taskGID)+"/subtasks", fields(TaskFields))
}

type Story struct {
	GID             string    `json:"gid"`
	Type            string    `json:"type"`             // "comment" or "system"
	ResourceSubtype string    `json:"resource_subtype"` // e.g. "comment_added", "assigned"
	Text            string    `json:"text"`
	CreatedBy       *Ref      `json:"created_by"`
	CreatedAt       time.Time `json:"created_at"`
}

var storyFields = []string{"type", "resource_subtype", "text", "created_by.name", "created_at"}

func (c *Client) Task(ctx context.Context, gid string) (*Task, error) {
	var t Task
	return &t, c.get(ctx, "/tasks/"+url.PathEscape(gid), fields(TaskFields), &t)
}

func (c *Client) Stories(ctx context.Context, taskGID string) ([]Story, error) {
	return list[Story](ctx, c, "/tasks/"+url.PathEscape(taskGID)+"/stories", fields(storyFields))
}
