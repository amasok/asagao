package outbox

import "testing"

// The kinds and states are stored in the outbox table, so their values must not change.
func TestStoredValues(t *testing.T) {
	for got, want := range map[string]string{
		KindUpdateTask:       "update_task",
		KindCreateTask:       "create_task",
		KindAddComment:       "add_comment",
		KindMoveTask:         "move_task",
		KindRenameSection:    "rename_section",
		KindCreateStatus:     "create_status",
		KindAddTag:           "add_tag",
		KindRemoveTag:        "remove_tag",
		KindAddFollower:      "add_follower",
		KindRemoveFollower:   "remove_follower",
		KindAddProject:       "add_project",
		KindRemoveProject:    "remove_project",
		KindDuplicateTask:    "duplicate_task",
		KindUploadAttachment: "upload_attachment",
		KindDeleteTask:       "delete_task",
		KindSetParent:        "set_parent",
		KindAddDependency:    "add_dependency",
		KindRemoveDependency: "remove_dependency",
		KindSetCustomField:   "set_custom_field",
		KindMarkNotification: "mark_notification",
		StatePending:         "pending",
		StateConflict:        "conflict",
		StateFailed:          "failed",
		StateSending:         "sending",
	} {
		if got != want {
			t.Errorf("%q, want %q", got, want)
		}
	}
}
