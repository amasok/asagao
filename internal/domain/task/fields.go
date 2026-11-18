package task

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// updatable are the task fields a local edit may change (the keys of an update_task
// payload), with whether the value is a bool (the others are strings).
var updatable = map[string]bool{"completed": true, "name": false, "notes": false, "html_notes": false, "due_on": false, "start_on": false, "assignee": false, "liked": true,
	"resource_subtype": false, "approval_status": false}

// ValidateFields checks the field changes of a local edit: at least one, each a field
// that can be changed with a value of its type, a name that is not blank, and a task type
// and approval status that can be set.
func ValidateFields(changes map[string]any) error {
	if len(changes) == 0 {
		return errors.New("変更がありません")
	}
	for k, v := range changes {
		isBool, ok := updatable[k]
		if !ok {
			return fmt.Errorf("unsupported field %q", k)
		}
		if isBool {
			if _, ok := v.(bool); !ok {
				return fmt.Errorf("%s must be bool", k)
			}
		} else if _, ok := v.(string); !ok {
			return fmt.Errorf("%s must be string", k)
		}
	}
	if name, ok := changes["name"].(string); ok && strings.TrimSpace(name) == "" {
		return errors.New("タスク名が空です")
	}
	for _, k := range []string{"start_on", "due_on"} {
		if v, ok := changes[k].(string); ok && v != "" {
			if _, err := time.Parse(time.DateOnly, v); err != nil {
				return fmt.Errorf("%s は YYYY-MM-DD で書いてください: %q", k, v)
			}
		}
	}
	if v, ok := changes["resource_subtype"].(string); ok && !slices.Contains(Subtypes, v) {
		return fmt.Errorf("タスクの種類は %s のどれかです: %q", strings.Join(Subtypes, " / "), v)
	}
	if v, ok := changes["approval_status"].(string); ok && !slices.Contains(ApprovalStatuses, v) {
		return fmt.Errorf("承認の状態は %s のどれかです: %q", strings.Join(ApprovalStatuses, " / "), v)
	}
	return nil
}

// CheckApproval checks that changes may set an approval status: only an approval task has
// one. subtype is the task's type now; a change of type in the same edit counts.
func CheckApproval(subtype string, changes map[string]any) error {
	if _, ok := changes["approval_status"]; !ok {
		return nil
	}
	if v, ok := changes["resource_subtype"].(string); ok {
		subtype = v
	}
	if subtype != "approval" {
		return errors.New("承認タスクではないため、承認の状態は設定できません（種類を「承認」にすると設定できます）")
	}
	return nil
}

// CheckDates checks a task's dates after an edit (Asana's rules): a start date needs a due
// date, and does not come after it. startOn and dueOn are YYYY-MM-DD or ""; dueOn is the
// day of the due date, also when the task is due at a time. dueCleared says that the edit
// removes the due date, so the message can say what to do instead.
func CheckDates(startOn, dueOn string, dueCleared bool) error {
	switch {
	case startOn == "":
		return nil
	case dueOn == "" && dueCleared:
		return fmt.Errorf("開始日 (%s) があるため、期日だけを消すことはできません（開始日も一緒に消してください）", startOn)
	case dueOn == "":
		return errors.New("開始日を付けるには期日も必要です")
	case startOn > dueOn:
		return fmt.Errorf("開始日 (%s) が期日 (%s) より後です", startOn, dueOn)
	}
	return nil
}
