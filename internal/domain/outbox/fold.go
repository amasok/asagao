package outbox

import (
	"fmt"
	"maps"
)

// Fold folds field changes of a task into its update_task entry op: consecutive edits of
// the same task are one entry, whose base stays the value the task had before the first of
// them (cur holds the task's current local values, for the fields not in the base yet).
//
//   - A field changed back to its base value leaves the payload (and the conflict): it is
//     not sent.
//   - Deciding an approval (approval_status) completes the task locally, so the base keeps
//     completed too, which discarding the entry restores. So does a change of the type or
//     of a custom type's status (#59), with the type, the custom type and its status.
//   - A conflicted entry with no conflicting field left is pending again.
//
// It returns the entry, and deleteOp when nothing is left to send: then the entry is not
// saved, and an entry already in the outbox (op.ID != 0) is removed. op is not changed; the
// returned entry has its own maps.
func Fold(op Op, cur, changes map[string]any) (Op, bool) {
	op.Payload = cloneOrNew(op.Payload)
	op.Base = cloneOrNew(op.Base)
	op.Conflict = maps.Clone(op.Conflict)
	keep := func(keys ...string) {
		for _, k := range keys {
			if _, ok := op.Base[k]; !ok {
				op.Base[k] = cur[k]
			}
		}
	}
	if _, ok := changes["approval_status"]; ok {
		keep("completed")
	}
	for _, k := range []string{"resource_subtype", "custom_type", "custom_type_status_option"} {
		if _, ok := changes[k]; ok {
			// One of them changes the others locally (a type of another kind has no custom
			// type; a status may complete the task): the base keeps them all.
			keep("resource_subtype", "custom_type", "custom_type_status_option", "completed")
			break
		}
	}
	for k, v := range changes {
		if _, ok := op.Base[k]; !ok {
			op.Base[k] = cur[k]
		}
		if SameValue(op.Base[k], v) {
			delete(op.Payload, k) // back to the original value
			delete(op.Conflict, k)
		} else {
			op.Payload[k] = v
		}
	}
	if v, ok := op.Payload["resource_subtype"]; ok && v != "custom" {
		// The task leaves its custom type: no custom type or status of it is sent.
		for _, k := range []string{"custom_type", "custom_type_status_option"} {
			delete(op.Payload, k)
			delete(op.Conflict, k)
		}
	}
	if op.State == StateConflict && len(op.Conflict) == 0 {
		op.State = StatePending // no conflicting field left
	}
	return op, len(op.Payload) == 0
}

// SameValue reports whether two field values are the same: they print the same with
// fmt.Sprint (a value read back from the outbox's JSON may not have the type it was
// written with).
func SameValue(a, b any) bool { return fmt.Sprint(a) == fmt.Sprint(b) }

func cloneOrNew(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return maps.Clone(m)
}
