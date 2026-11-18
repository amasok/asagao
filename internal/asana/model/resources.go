// Package model holds the types of Asana's resources as the REST API returns them (JSON
// tags included) and its error, without the client. The store, the sync engine and auth
// use them; internal/asana keeps aliases (migration-plan.md, d1).
package model

import "time"

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
	HTMLNotes    string    `json:"html_notes"`
	Owner        *Ref      `json:"owner"`
	StartOn      string    `json:"start_on"`
	DueOn        string    `json:"due_on"`
	Workspace    *Ref      `json:"workspace"`
	Team         *Ref      `json:"team"`
	Members      []Ref     `json:"members"`
	PermalinkURL string    `json:"permalink_url"`
	CreatedAt    time.Time `json:"created_at"`
	ModifiedAt   time.Time `json:"modified_at"`
	// ProjectBrief is the project's brief, if it has one (only asked for by Client.Project).
	ProjectBrief *Ref `json:"project_brief,omitempty"`
}

// Team is a team of an organization.
type Team struct {
	GID  string `json:"gid"`
	Name string `json:"name"`
}

// TaskCounts is how many tasks a project has (GET /projects/{gid}/task_counts).
type TaskCounts struct {
	NumTasks           int `json:"num_tasks"`
	NumCompletedTasks  int `json:"num_completed_tasks"`
	NumIncompleteTasks int `json:"num_incomplete_tasks"`
	NumMilestones      int `json:"num_milestones"`
	NumCompletedMiles  int `json:"num_completed_milestones"`
}

// ProjectBrief is a project's brief (the "概要" note; a preview API).
type ProjectBrief struct {
	GID      string `json:"gid"`
	Title    string `json:"title"`
	Text     string `json:"text"`
	HTMLText string `json:"html_text"`
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
	HTMLNotes       string       `json:"html_notes"`
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
	Followers       []Ref        `json:"followers"`
	Liked           bool         `json:"liked"`
	NumLikes        int          `json:"num_likes"`
	Workspace       *Ref         `json:"workspace"`
	PermalinkURL    string       `json:"permalink_url"`
	CreatedAt       time.Time    `json:"created_at"`
	CreatedBy       *Ref         `json:"created_by"`
	ModifiedAt      time.Time    `json:"modified_at"`
	// CustomFields is nil when the response did not include them.
	CustomFields []CustomField `json:"custom_fields"`
	// ApprovalStatus is pending / approved / rejected / changes_requested (approval tasks).
	ApprovalStatus string `json:"approval_status"`
	// Dependencies (tasks this one waits for) and Dependents (tasks waiting for this one)
	// are nil when the response did not include them.
	Dependencies []Ref `json:"dependencies"`
	Dependents   []Ref `json:"dependents"`
	// Partial is set when the task was fetched without the optional fields (see
	// optionalTaskFields): its custom fields, dependencies and approval status are unknown.
	Partial bool `json:"-"`
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

type Story struct {
	GID             string    `json:"gid"`
	Type            string    `json:"type"`             // "comment" or "system"
	ResourceSubtype string    `json:"resource_subtype"` // e.g. "comment_added", "assigned"
	Text            string    `json:"text"`
	HTMLText        string    `json:"html_text"`
	CreatedBy       *Ref      `json:"created_by"`
	CreatedAt       time.Time `json:"created_at"`
}

type Attachment struct {
	GID             string    `json:"gid"`
	Name            string    `json:"name"`
	ResourceSubtype string    `json:"resource_subtype"` // "asana" for uploaded files; external hosts otherwise
	Host            string    `json:"host"`
	DownloadURL     string    `json:"download_url"` // short-lived; fetch Attachment(gid) right before downloading
	ViewURL         string    `json:"view_url"`
	PermanentURL    string    `json:"permanent_url"`
	Size            int64     `json:"size"`
	CreatedAt       time.Time `json:"created_at"`
	Parent          *Ref      `json:"parent,omitempty"` // the task (or project) it is attached to
}

type StatusUpdate struct {
	GID        string    `json:"gid"`
	StatusType string    `json:"status_type"` // on_track, at_risk, off_track, on_hold, complete
	Title      string    `json:"title"`
	Text       string    `json:"text"`
	HTMLText   string    `json:"html_text"`
	Author     *Ref      `json:"author"`
	CreatedAt  time.Time `json:"created_at"`
}
