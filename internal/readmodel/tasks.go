package readmodel

import (
	"fmt"
	"strings"

	"github.com/amasok/asagao/internal/asana/model"
)

type MembershipView struct {
	ProjectGID  string `json:"project_gid"`
	ProjectName string `json:"project_name"`
	SectionGID  string `json:"section_gid,omitempty"`
	SectionName string `json:"section_name,omitempty"`
}

type TaskSummary struct {
	GID             string           `json:"gid"`
	Name            string           `json:"name"`
	ResourceSubtype string           `json:"resource_subtype"`
	Completed       bool             `json:"completed"`
	CompletedAt     string           `json:"completed_at,omitempty"`
	DueOn           string           `json:"due_on,omitempty"`
	DueAt           string           `json:"due_at,omitempty"`
	StartOn         string           `json:"start_on,omitempty"`
	Assignee        *UserRef         `json:"assignee"`
	Memberships     []MembershipView `json:"memberships"`
	Tags            []string         `json:"tags"`
	ParentGID       string           `json:"parent_gid,omitempty"`
	ParentName      string           `json:"parent_name,omitempty"`
	NumSubtasks     int              `json:"num_subtasks"`
	PermalinkURL    string           `json:"permalink_url"`
	ModifiedAt      string           `json:"modified_at"`
	// Pending is true while local changes to this task have not been sent to Asana.
	Pending  bool `json:"pending"`
	Liked    bool `json:"liked"`
	NumLikes int  `json:"num_likes"`
	// ApprovalStatus of an approval task: pending, approved, rejected or changes_requested.
	ApprovalStatus string `json:"approval_status,omitempty"`
	// OpenDependencies is how many incomplete tasks this task waits for.
	OpenDependencies int `json:"open_dependencies,omitempty"`
	// CustomFields: in lists, the fields that have a value; in a task's detail, every field
	// of the task (and of its projects), with the choices of enum fields.
	CustomFields []CustomFieldView `json:"custom_fields,omitempty"`
}

type StoryView struct {
	GID             string   `json:"gid"`
	Type            string   `json:"type"`
	ResourceSubtype string   `json:"resource_subtype"`
	Text            string   `json:"text"`
	CreatedBy       *UserRef `json:"created_by"`
	CreatedAt       string   `json:"created_at"`
	Pending         bool     `json:"pending"`
	// HTMLText is the comment as Asana rich text (empty for comments posted offline).
	HTMLText string `json:"html_text,omitempty"`
	// Reactions are the emoji reactions on the comment, as Asana last returned them.
	Reactions []Reaction `json:"reactions,omitempty"`
}

// Reaction is one emoji's reactions on a comment (Asana's reaction_summary item).
type Reaction = model.Reaction

type TaskDetail struct {
	TaskSummary
	Notes string `json:"notes"`
	// HTMLNotes is the description as Asana rich text (HTML subset); empty if unknown.
	HTMLNotes   string           `json:"html_notes,omitempty"`
	CreatedAt   string           `json:"created_at"`
	Parent      *TaskRef         `json:"parent"`
	Subtasks    []TaskSummary    `json:"subtasks"`
	Stories     []StoryView      `json:"stories"`
	Attachments []AttachmentView `json:"attachments"`
	Followers   []UserRef        `json:"followers"`
	// Dependencies are the tasks this task waits for; Dependents the tasks waiting for it.
	Dependencies []DependencyView `json:"dependencies"`
	Dependents   []DependencyView `json:"dependents"`
	// DetailsFetchedAt is when subtasks/stories were last fetched; empty if never.
	DetailsFetchedAt string `json:"details_fetched_at,omitempty"`
}

type TaskRef struct {
	GID  string `json:"gid"`
	Name string `json:"name"`
}

// TaskFilter selects tasks from the store. Zero values mean "no filter".
type TaskFilter struct {
	WorkspaceGID string
	AssigneeGID  string
	ProjectGID   string
	SectionGID   string
	ParentGID    string
	// Completed: nil = both, true = completed only, false = incomplete only.
	Completed *bool
	DueBefore string // inclusive, YYYY-MM-DD
	DueAfter  string // inclusive, YYYY-MM-DD
	// ModifiedAfter keeps tasks modified at or after this time (RFC 3339).
	ModifiedAfter string
	// SortByModified orders by last modification, newest first (instead of due date).
	SortByModified bool
	Tag            string // tag name or gid
	// Field (a custom field's gid or name) keeps tasks whose field has FieldValue (its text,
	// or one of its options); with FieldValue "" any value.
	Field      string
	FieldValue string
	// Query is a full-text search over task name, notes and comments.
	Query string
	Limit int
}

// TagCount is a tag with the number of incomplete tasks that have it.
type TagCount struct {
	GID   string `json:"gid"`
	Name  string `json:"name"`
	Open  int    `json:"open_tasks"`
	Total int    `json:"tasks"`
}

type AttachmentView struct {
	GID       string `json:"gid"`
	Name      string `json:"name"`
	Host      string `json:"host"`
	ViewURL   string `json:"view_url,omitempty"`
	Size      int64  `json:"size"`
	CreatedAt string `json:"created_at"`
	IsImage   bool   `json:"is_image"`
	// LocalPath is set once the file has been downloaded (viewable offline).
	LocalPath string `json:"local_path,omitempty"`
}

// DependencyView is a task linked to another by a dependency.
type DependencyView struct {
	GID             string `json:"gid"`
	Name            string `json:"name"`
	Completed       bool   `json:"completed"`
	ResourceSubtype string `json:"resource_subtype,omitempty"`
	// Pending: the link was added here and not sent yet.
	Pending bool `json:"pending,omitempty"`
}

// FieldOption is a choice of an enum / multi_enum field that tasks in the store use, for
// choosing a filter: "field name: option name".
type FieldOption struct {
	FieldGID  string `json:"field_gid"`
	FieldName string `json:"field_name"`
	Option    string `json:"option"`
	Color     string `json:"color,omitempty"`
	Tasks     int    `json:"tasks"` // incomplete tasks with this value
}

// ReactionsLine shows a comment's reactions in one line, e.g. "👍 2  🎉 1" ("" for none).
// The emoji the user reacted with is marked with "*".
func ReactionsLine(rs []Reaction) string {
	parts := make([]string, 0, len(rs))
	for _, r := range rs {
		if r.Count <= 0 {
			continue
		}
		emoji := r.Variant
		if emoji == "" {
			emoji = r.EmojiBase
		}
		mark := ""
		if r.Reacted {
			mark = "*"
		}
		parts = append(parts, fmt.Sprintf("%s%s %d", emoji, mark, r.Count))
	}
	return strings.Join(parts, "  ")
}

// DeletePlan is what deleting a task does: the task, its subtasks (counted with theirs),
// and the changes queued for them, which are dropped with it.
type DeletePlan struct {
	GID      string `json:"gid"`
	Name     string `json:"name"`
	Subtasks int    `json:"subtasks"`
	// Queued is the number of queued changes to the task or its subtasks that are dropped
	// (once the delete is sent; at once for a task never sent).
	Queued int `json:"queued"`
	// Local: the task was never sent to Asana, so it is removed at once and nothing is queued.
	Local bool `json:"local"`
}
