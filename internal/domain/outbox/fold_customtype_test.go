package outbox

import "testing"

// A change of the type or of a custom type's status keeps them all in the base (#59), and
// leaving the custom type drops its fields from the payload.
func TestFoldCustomType(t *testing.T) {
	cur := map[string]any{"resource_subtype": "custom", "custom_type": "A", "custom_type_status_option": "a1", "completed": false}
	op, del := Fold(Op{}, cur, map[string]any{"custom_type_status_option": "a2"})
	if del || op.Payload["custom_type_status_option"] != "a2" {
		t.Fatalf("payload %v", op.Payload)
	}
	for k, v := range cur {
		if op.Base[k] != v {
			t.Errorf("base[%s] = %v, want %v", k, op.Base[k], v)
		}
	}
	cur["custom_type_status_option"], cur["completed"] = "a2", true
	op, del = Fold(op, cur, map[string]any{"resource_subtype": "milestone"})
	if del || op.Payload["resource_subtype"] != "milestone" || len(op.Payload) != 1 {
		t.Fatalf("payload %v", op.Payload)
	}
	if op.Base["custom_type_status_option"] != "a1" || op.Base["completed"] != false {
		t.Fatalf("base %v", op.Base)
	}
	// Back to the base: nothing to send.
	cur["resource_subtype"] = "milestone"
	if _, del := Fold(op, cur, map[string]any{"resource_subtype": "custom", "custom_type": "A", "custom_type_status_option": "a1"}); !del {
		t.Fatal("a change back to the base is still sent")
	}
}
