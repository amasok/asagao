// Package outbox holds the rules of the outbox: the changes made locally (offline or not)
// and queued to be sent to Asana. Each entry has a kind (what to do) and a state (where it
// is in being sent). The store keeps the entries; the sync engine sends them.
//
// update_task payloads use these keys (values are what Asana accepts):
//
//	completed bool, name string, notes string, due_on "YYYY-MM-DD" or "", assignee gid or ""
//
// "" means "clear" for due_on and assignee.
package outbox

// Kinds of outbox entries (the outbox kind column).
const (
	KindUpdateTask = "update_task"
	KindCreateTask = "create_task"
	KindAddComment = "add_comment"
	KindMoveTask   = "move_task" // payload: project, section, section_name; base: section

	// KindRenameSection renames a section. The outbox task_gid column holds the section gid.
	// payload: name; base: name (before the local edit).
	KindRenameSection = "rename_section"

	// Section operations of a project. The outbox task_gid column holds the section gid
	// ("local:..." for a section created offline, replaced once Asana created it).
	//   create_section: payload: project, name, before / after (a section gid, or absent)
	//   delete_section: payload: project, name (only empty sections can be deleted)
	//   insert_section: payload: project, name, before / after (exactly one); base: position
	KindCreateSection = "create_section"
	KindDeleteSection = "delete_section"
	KindInsertSection = "insert_section"

	// Tag operations on the tag itself. The outbox task_gid column holds the tag gid.
	//   update_tag: payload: name and/or color ("" for none), workspace; base: name, color
	//   delete_tag: payload: name, color, workspace (to restore)
	KindUpdateTag = "update_tag"
	KindDeleteTag = "delete_tag"

	// KindCreateStatus posts a project status update. task_gid holds the project gid;
	// payload: status (local gid), status_type, title, text, html_text.
	KindCreateStatus = "create_status"

	// Tag operations. payload: tag (gid, "local:..." for a tag created offline), tag_name, workspace.
	KindAddTag    = "add_tag"
	KindRemoveTag = "remove_tag"

	KindAddFollower      = "add_follower"    // payload: user, user_name
	KindRemoveFollower   = "remove_follower" // payload: user, user_name
	KindAddProject       = "add_project"     // payload: project, project_name, section
	KindRemoveProject    = "remove_project"  // payload: project, project_name, section (to restore)
	KindDuplicateTask    = "duplicate_task"  // task_gid: the local copy; payload: source, name
	KindUploadAttachment = "upload_attachment"

	KindAddDependency    = "add_dependency"    // payload: dependency, dependency_name
	KindRemoveDependency = "remove_dependency" // payload: dependency, dependency_name

	// KindSetCustomField sets one custom field of a task. payload: field, field_name, type,
	// value, display; base / conflict: value, display.
	KindSetCustomField = "set_custom_field"

	// KindMarkNotification marks a notification read or unread. The outbox task_gid column
	// holds the notification gid. payload: read bool, workspace, name (what it is about, for
	// the outbox list); base: read (before the local change).
	KindMarkNotification = "mark_notification"

	// KindMoveMySection moves a task to another section of the user's My Tasks list.
	// payload: section, section_name, workspace; base: section (before the local move).
	KindMoveMySection = "move_my_section"
)

// SectionRefKeys are the payload keys that may hold a section gid, so that a section
// created offline can be replaced by its Asana gid everywhere.
var SectionRefKeys = []string{"section", "before", "after"}

// States of outbox entries (the outbox state column).
const (
	StatePending  = "pending"
	StateConflict = "conflict"
	StateFailed   = "failed"
	// StateSending is the state of an entry while Push is sending it.
	StateSending = "sending"
)

// Op is one outbox entry.
type Op struct {
	ID             int64          `json:"id"`
	Kind           string         `json:"kind"`
	TaskGID        string         `json:"task_gid"`
	TaskName       string         `json:"task_name"`
	Payload        map[string]any `json:"payload"`
	Base           map[string]any `json:"base,omitempty"`
	BaseModifiedAt string         `json:"base_modified_at,omitempty"`
	State          string         `json:"state"`
	Conflict       map[string]any `json:"conflict,omitempty"`
	LastError      string         `json:"last_error,omitempty"`
	Attempts       int            `json:"attempts"`
	CreatedAt      string         `json:"created_at"`
	// SentAt is when the entry was (first) sent; Verify means Asana may already have
	// applied it, so Push checks Asana before sending it again (see store/outbox_send.go).
	SentAt     string `json:"sent_at,omitempty"`
	LastSentAt string `json:"last_sent_at,omitempty"`
	Verify     bool   `json:"verify,omitempty"`
}

// MoveTarget is where a task created in Asana still has to be moved: a create sends the
// task into the project, and the entry becomes a move_task to its section.
type MoveTarget struct {
	Project, Section string
	// From is the section Asana put the task in (restored if the move is discarded).
	From string
}
