package task

import (
	"fmt"
	"strings"
)

// Task types (resource_subtype) that can be chosen by name, and approval statuses. A task
// of a custom task type (#59) has SubtypeCustom: it is set with the type (CustomType), so
// it is not one of Subtypes.
var (
	Subtypes         = []string{"default_task", "milestone", "approval"}
	ApprovalStatuses = []string{"pending", "approved", "rejected", "changes_requested"}
)

// SubtypeCustom is the resource_subtype of a task of a custom task type.
const SubtypeCustom = "custom"

// SubtypeMilestone is the resource_subtype of a milestone, which has a due date and no
// start date (CheckMilestoneStart).
const SubtypeMilestone = "milestone"

// SubtypeLabel is the Japanese name of a task type (resource_subtype). A custom task type
// has its own name, which is shown instead where it is known.
func SubtypeLabel(s string) string {
	if l, ok := map[string]string{"default_task": "タスク", "milestone": "マイルストーン", "approval": "承認", SubtypeCustom: "カスタムタイプ"}[s]; ok {
		return l
	}
	return s
}

// ApprovalLabel is the Japanese name of an approval status.
func ApprovalLabel(s string) string {
	if l, ok := map[string]string{"pending": "承認待ち", "approved": "承認済み", "rejected": "却下", "changes_requested": "要修正"}[s]; ok {
		return l
	}
	return s
}

// ParseSubtype accepts a task type as Asana names it or as SubtypeLabel shows it
// ("default" and "task" mean default_task).
func ParseSubtype(s string) (string, error) {
	return parseChoice(s, Subtypes, SubtypeLabel, map[string]string{"default": "default_task", "task": "default_task", "normal": "default_task"})
}

// ParseApprovalStatus accepts an approval status as Asana names it or as ApprovalLabel
// shows it (also "承認" / "approve" for approved, "reject" for rejected).
func ParseApprovalStatus(s string) (string, error) {
	return parseChoice(s, ApprovalStatuses, ApprovalLabel, map[string]string{"approve": "approved", "承認": "approved",
		"reject": "rejected", "却下する": "rejected", "request_changes": "changes_requested", "修正依頼": "changes_requested"})
}

func parseChoice(s string, values []string, label func(string) string, aliases map[string]string) (string, error) {
	s = strings.TrimSpace(s)
	if v, ok := aliases[strings.ToLower(s)]; ok {
		return v, nil
	}
	var labels []string
	for _, v := range values {
		if strings.EqualFold(s, v) || s == label(v) {
			return v, nil
		}
		labels = append(labels, v+"（"+label(v)+"）")
	}
	return "", fmt.Errorf("%q は使えません。%s のどれかを指定してください", s, strings.Join(labels, " / "))
}
