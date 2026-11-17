package store

import (
	"reflect"
	"testing"

	"github.com/amasok/asagao/internal/readmodel"
)

// The names moved in e1 keep their old values through the store's aliases.
func TestE1AliasesKeepValues(t *testing.T) {
	for got, want := range map[string]string{
		KeyLastSync: "last_sync", KeyEvents: "events:", KeyCustomFields: "custom_fields:",
		ResultTask: "gid:", ResultStory: "story:", ResultAttachment: "att:", ResultStatus: "status:", ResultTag: "tag:",
		OpUpdateTask: "update_task", OpCreateTask: "create_task", OpAddComment: "add_comment", OpMoveTask: "move_task",
		OpPending: "pending", OpConflict: "conflict", OpFailed: "failed", OpSending: "sending",
		AmbiguousMessage: readmodel.AmbiguousMessage,
	} {
		if got != want {
			t.Errorf("%q, want %q", got, want)
		}
	}
	if !reflect.DeepEqual(gidList([]string{"b", "a", "a"}), []string{"a", "b"}) || normalizeCFValue(3) != float64(3) {
		t.Error("helpers")
	}
	if CustomFieldAPIValue(CFDate, "2024-01-01") == nil {
		t.Error("api value")
	}
}
