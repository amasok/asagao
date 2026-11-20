package outbox

import "testing"

// The kinds and states are stored in the outbox table, so their values must not change.
func TestStoredValues(t *testing.T) {
	kinds := map[Kind]string{
		KindUpdateTask:         "update_task",
		KindCreateTask:         "create_task",
		KindAddComment:         "add_comment",
		KindMoveTask:           "move_task",
		KindRenameSection:      "rename_section",
		KindCreateSection:      "create_section",
		KindDeleteSection:      "delete_section",
		KindInsertSection:      "insert_section",
		KindUpdateTag:          "update_tag",
		KindDeleteTag:          "delete_tag",
		KindCreateStatus:       "create_status",
		KindAddTag:             "add_tag",
		KindRemoveTag:          "remove_tag",
		KindAddFollower:        "add_follower",
		KindRemoveFollower:     "remove_follower",
		KindAddProject:         "add_project",
		KindRemoveProject:      "remove_project",
		KindDuplicateTask:      "duplicate_task",
		KindUploadAttachment:   "upload_attachment",
		KindDeleteTask:         "delete_task",
		KindSetParent:          "set_parent",
		KindAddDependency:      "add_dependency",
		KindRemoveDependency:   "remove_dependency",
		KindSetCustomField:     "set_custom_field",
		KindMarkNotification:   "mark_notification",
		KindUpdateStory:        "update_story",
		KindDeleteStory:        "delete_story",
		KindDeleteAttachment:   "delete_attachment",
		KindMoveMySection:      "move_my_section",
		KindUpdateProject:      "update_project",
		KindUpdateProjectBrief: "update_project_brief",
		KindAddTimeEntry:       "add_time_entry",
		KindUpdateTimeEntry:    "update_time_entry",
		KindDeleteTimeEntry:    "delete_time_entry",
		KindCreateEnumOption:   "create_enum_option",
		KindAddFieldSetting:    "add_custom_field_setting",
		KindRemoveFieldSetting: "remove_custom_field_setting",
	}
	for got, want := range kinds {
		if string(got) != want {
			t.Errorf("%q, want %q", got, want)
		}
	}
	if len(kinds) != len(AllKinds()) {
		t.Errorf("%d stored values checked, %d kinds", len(kinds), len(AllKinds()))
	}
	for got, want := range map[string]string{
		StatePending:  "pending",
		StateConflict: "conflict",
		StateFailed:   "failed",
		StateSending:  "sending",
	} {
		if got != want {
			t.Errorf("%q, want %q", got, want)
		}
	}
}
