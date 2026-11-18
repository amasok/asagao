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
	// KindMoveTask moves a task to a section. payload: project, section, section_name, and
	// insert_before / insert_after (a task of the section, optional: where in it); base: section.
	KindMoveTask = "move_task"

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

	// KindDeleteTask deletes a task (and its subtasks) in Asana. Until it is sent the task
	// stays in the store, hidden; discarding the entry shows it again. payload: name.
	KindDeleteTask = "delete_task"
	// KindSetParent makes a task a subtask of another, or a top-level task (parent "").
	// payload: parent, parent_name, insert_before / insert_after (a sibling, optional);
	// base: parent (before the local change).
	KindSetParent = "set_parent"

	KindAddDependency    = "add_dependency"    // payload: dependency, dependency_name
	KindRemoveDependency = "remove_dependency" // payload: dependency, dependency_name

	// KindSetCustomField sets one custom field of a task. payload: field, field_name, type,
	// value, display; base / conflict: value, display.
	KindSetCustomField = "set_custom_field"

	// KindMarkNotification marks a notification read or unread. The outbox task_gid column
	// holds the notification gid. payload: read bool, workspace, name (what it is about, for
	// the outbox list); base: read (before the local change).
	KindMarkNotification = "mark_notification"

	// Comment and attachment edits (#34). task_gid holds the task the comment or the
	// attachment belongs to.
	//
	// KindUpdateStory edits one of the user's comments. payload: story (gid, "local:..."
	// while its add_comment is being sent or checked), text, html_text; base: text,
	// html_text (before the local edit).
	KindUpdateStory = "update_story"
	// KindDeleteStory deletes one of the user's comments. payload: story, text (for the
	// outbox list); base: the comment as it was stored (to put it back if discarded).
	KindDeleteStory = "delete_story"
	// KindDeleteAttachment deletes an attachment. payload: attachment, name; base: the
	// attachment as it was stored.
	KindDeleteAttachment = "delete_attachment"

	// KindMoveMySection moves a task to another section of the user's My Tasks list.
	// payload: section, section_name, workspace; base: section (before the local move).
	KindMoveMySection = "move_my_section"

	// KindUpdateProject changes a project's fields (see domain/project). The outbox task_gid
	// column holds the project gid. payload: the changed fields (notes, html_notes, owner,
	// start_on, due_on, color); base: their values before the local edit.
	KindUpdateProject = "update_project"

	// Time tracking entries of a task (#6). task_gid holds the task.
	//
	// KindAddTimeEntry records time on the task as the logged-in user. payload: entry (its
	// local gid), duration_minutes, entered_on ("YYYY-MM-DD").
	KindAddTimeEntry = "add_time_entry"
	// KindUpdateTimeEntry changes an entry. payload: entry (gid, "local:..." while its
	// add_time_entry is being sent or checked), duration_minutes, entered_on; base: both
	// before the local edit.
	KindUpdateTimeEntry = "update_time_entry"
	// KindDeleteTimeEntry deletes an entry. payload: entry, duration_minutes, entered_on
	// (for the outbox list); base: the entry as it was stored (to put it back if discarded).
	KindDeleteTimeEntry = "delete_time_entry"

	// Custom fields themselves (#37).
	//
	// KindCreateEnumOption adds an option to an enum or multi_enum field. The outbox
	// task_gid column holds the option's gid ("local:...", replaced once Asana created it:
	// the values that use it wait for it). payload: field, field_name, name. It is matched
	// by name in Asana before it is sent and when it is verified (never created twice).
	KindCreateEnumOption = "create_enum_option"
	// KindAddFieldSetting adds an existing custom field to a project, KindRemoveFieldSetting
	// removes it. The outbox task_gid column holds the project gid. payload: field,
	// field_name; base (remove only): position, is_important (to restore).
	KindAddFieldSetting    = "add_custom_field_setting"
	KindRemoveFieldSetting = "remove_custom_field_setting"
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
