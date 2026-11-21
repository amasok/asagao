package task

import (
	"errors"
	"testing"
)

// #62: Asana refuses a start date on a milestone (400), so it is refused before it is queued.
func TestCheckMilestoneStart(t *testing.T) {
	for _, tc := range []struct {
		subtype string
		changes map[string]any
		refused bool
	}{
		{"milestone", map[string]any{"start_on": "2026-12-01"}, true},
		{"milestone", map[string]any{"start_on": "2026-12-01", "due_on": "2026-12-10"}, true},
		{"default_task", map[string]any{"start_on": "2026-12-01", "resource_subtype": "milestone"}, true},
		{"", map[string]any{"start_on": "2026-12-01", "resource_subtype": "milestone"}, true},
		{"milestone", map[string]any{"start_on": ""}, false},                                               // clearing it
		{"milestone", map[string]any{"name": "リリース"}, false},                                               // no start date
		{"milestone", map[string]any{"start_on": "2026-12-01", "resource_subtype": "default_task"}, false}, // no longer one
		{"", map[string]any{"start_on": "2026-12-01"}, false},                                              // not known: Asana decides
		{"default_task", map[string]any{"start_on": "2026-12-01"}, false},
		{"approval", map[string]any{"start_on": "2026-12-01"}, false},
		{"milestone", map[string]any{"start_on": nil}, false},
	} {
		err := CheckMilestoneStart(tc.subtype, "", tc.changes)
		if tc.refused != errors.Is(err, ErrMilestoneStart) || (!tc.refused && err != nil) {
			t.Errorf("CheckMilestoneStart(%q, %v) = %v, want refused = %v", tc.subtype, tc.changes, err, tc.refused)
		}
	}
}

// A task that has a start date is not made a milestone unless the start date is cleared
// with it; a milestone that has one in Asana can be edited otherwise.
func TestCheckMilestoneStartWithStartDate(t *testing.T) {
	for _, tc := range []struct {
		subtype string
		changes map[string]any
		refused bool
	}{
		{"default_task", map[string]any{"resource_subtype": "milestone"}, true},
		{"", map[string]any{"resource_subtype": "milestone"}, true},
		{"default_task", map[string]any{"resource_subtype": "milestone", "start_on": ""}, false},
		{"default_task", map[string]any{"resource_subtype": "approval"}, false},
		{"milestone", map[string]any{"name": "リリース"}, false},
		{"milestone", map[string]any{"due_on": "2026-12-20"}, false},
		{"milestone", map[string]any{"start_on": ""}, false},
		{"milestone", map[string]any{"start_on": "2026-12-02"}, true},
		{"milestone", map[string]any{"resource_subtype": "default_task"}, false},
	} {
		err := CheckMilestoneStart(tc.subtype, "2026-12-01", tc.changes)
		if tc.refused != errors.Is(err, ErrMilestoneStart) || (!tc.refused && err != nil) {
			t.Errorf("CheckMilestoneStart(%q, start, %v) = %v, want refused = %v", tc.subtype, tc.changes, err, tc.refused)
		}
	}
}
