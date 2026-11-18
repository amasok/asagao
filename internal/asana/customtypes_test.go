package asana

import (
	"slices"
	"testing"
)

// Tasks are read with their custom type and its status (#59), as optional fields: a
// workspace that refuses them still syncs its tasks.
func TestTaskFieldsHaveCustomType(t *testing.T) {
	for _, f := range []string{"custom_type.name", "custom_type_status_option.name"} {
		if !slices.Contains(TaskFields, f) {
			t.Errorf("TaskFields lack %s", f)
		}
		if !isOptionalTaskField(f) {
			t.Errorf("%s is not optional", f)
		}
	}
}
