// Package model holds the types of Asana's resources as the REST API returns them (JSON
// tags included) and its error, without the client. The store, the sync engine and auth
// use them; internal/asana keeps aliases (migration-plan.md, d1).
package model

import (
	"encoding/json"
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

// Tag is a tag with its color (a color name such as "dark-green", or "" for none).
type Tag struct {
	GID   string `json:"gid"`
	Name  string `json:"name"`
	Color string `json:"color"`
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
	// CustomType and CustomTypeStatusOption are the custom task type and its status of a
	// task whose ResourceSubtype is "custom" (#59); nil for the other tasks.
	CustomType             *Ref `json:"custom_type"`
	CustomTypeStatusOption *Ref `json:"custom_type_status_option"`
	// Dependencies (tasks this one waits for) and Dependents (tasks waiting for this one)
	// are nil when the response did not include them.
	Dependencies []Ref `json:"dependencies"`
	Dependents   []Ref `json:"dependents"`
	// AssigneeSection is the section of the assignee's My Tasks the task is in. Set is
	// false when the response did not include it (it is then left as it is).
	AssigneeSection OptionalRef `json:"assignee_section"`
	// Partial is set when the task was fetched without the optional fields (see
	// optionalTaskFields): its custom fields, dependencies, approval status and custom type
	// are unknown.
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
	// ReactionSummary is the emoji reactions on a comment, one entry per emoji.
	ReactionSummary []Reaction `json:"reaction_summary,omitempty"`
}

// Reaction is one emoji's reactions on a story (an item of its reaction_summary).
type Reaction struct {
	EmojiBase string `json:"emoji_base"`
	Variant   string `json:"variant,omitempty"` // the emoji as shown (e.g. with a skin tone)
	Count     int    `json:"count"`
	Reacted   bool   `json:"reacted,omitempty"` // the logged-in user is one of them
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

// Position is where in a list (a section's tasks, a parent's subtasks) a task goes: just
// before or just after another task of it. Zero: Asana's default (the end, for addTask).
type Position struct{ Before, After string }

// IsZero reports whether p leaves the place to Asana.
func (p Position) IsZero() bool { return p.Before == "" && p.After == "" }

// OptionalRef is a reference that a response may leave out (Set false), give as null
// (Set, Ref nil) or give (Set, Ref).
type OptionalRef struct {
	Set bool
	Ref *Ref
}

func (o *OptionalRef) UnmarshalJSON(b []byte) error {
	o.Set, o.Ref = true, nil
	if string(b) == "null" {
		return nil
	}
	var r Ref
	if err := json.Unmarshal(b, &r); err != nil {
		return err
	}
	o.Ref = &r
	return nil
}

func (o OptionalRef) MarshalJSON() ([]byte, error) {
	if o.Ref == nil {
		return []byte("null"), nil
	}
	return json.Marshal(o.Ref)
}

// GID is the referenced gid ("" for none).
func (o OptionalRef) GID() string {
	if o.Ref == nil {
		return ""
	}
	return o.Ref.GID
}
