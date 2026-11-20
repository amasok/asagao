package outbox

import (
	"encoding/json"
	"math"
	"testing"
)

type namedString string

func TestSameValue(t *testing.T) {
	for _, tc := range []struct {
		name string
		a, b any
		want bool
	}{
		{"same string", "a", "a", true},
		{"other string", "a", "b", false},
		{"empty strings", "", "", true},
		{"int and float", 1, 1.0, true},
		{"float and int", float64(30), 30, true},
		{"int64 and float", int64(1 << 40), float64(1 << 40), true},
		{"uint8 and int", uint8(7), 7, true},
		{"float32 as JSON keeps it", float32(0.1), 0.1, true},
		{"json.Number", json.Number("12.5"), 12.5, true},
		{"other numbers", 1, 2, false},
		{"NaN", math.NaN(), math.NaN(), false},
		{"bools", true, true, true},
		{"other bools", true, false, false},
		{"bool is not its text", true, "true", false},
		{"number is not its text", 3, "3", false},
		{"float is not its text", 1.5, "1.5", false},
		{"nil", nil, nil, true},
		{"nil is not the text <nil>", nil, "<nil>", false},
		{"the text <nil> is not nil", "<nil>", nil, false},
		{"nil is not empty", nil, "", false},
		{"nil is not false", nil, false, false},
		{"nil is not 0", nil, 0, false},
		{"nil list is nil", []string(nil), nil, true},
		{"nil map is nil", map[string]any(nil), nil, true},
		{"nil pointer is nil", (*int)(nil), nil, true},
		{"empty list is not nil", []string{}, nil, false},
		{"empty lists of other types", []string{}, []any{}, true},
		{"list read back from JSON", []string{"a", "b"}, []any{"a", "b"}, true},
		{"list order counts", []string{"a", "b"}, []any{"b", "a"}, false},
		{"list length counts", []string{"a"}, []any{"a", "a"}, false},
		{"numbers in lists", []int{1, 2}, []any{1.0, 2.0}, true},
		{"list is not its text", []string{"a"}, "[a]", false},
		{"list of text is not a list of numbers", []string{"1"}, []any{1.0}, false},
		{"maps read back from JSON", map[string]string{"x": "1"}, map[string]any{"x": "1"}, true},
		{"map values by type", map[string]any{"x": 1}, map[string]any{"x": "1"}, false},
		{"map keys", map[string]any{"x": 1}, map[string]any{"y": 1}, false},
		{"nested", map[string]any{"l": []int{1}}, map[string]any{"l": []any{1.0}}, true},
		{"named string", namedString("a"), "a", true},
		{"named string differs", namedString("a"), "b", false},
		{"kind", KindUpdateTask, "update_task", true},
	} {
		if got := SameValue(tc.a, tc.b); got != tc.want {
			t.Errorf("%s: SameValue(%#v, %#v) = %v, want %v", tc.name, tc.a, tc.b, got, tc.want)
		}
		if got := SameValue(tc.b, tc.a); got != tc.want {
			t.Errorf("%s: SameValue(%#v, %#v) = %v, want %v (swapped)", tc.name, tc.b, tc.a, got, tc.want)
		}
	}
}

// A value written to the outbox and read back is the same as the one written.
func TestSameValueThroughJSON(t *testing.T) {
	for _, v := range []any{nil, true, "", "<nil>", 0, 3, -2.5, int64(1 << 50), []string{"a", "b"}, []string{},
		map[string]any{"a": []int{1}}, map[string]string{}} {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var back any
		if err := json.Unmarshal(b, &back); err != nil {
			t.Fatal(err)
		}
		if !SameValue(v, back) {
			t.Errorf("%#v read back as %#v is not the same", v, back)
		}
	}
}

// A field changed to the text "<nil>" is sent even when the base had no value, and a
// field set back to its base read from JSON (a float64) leaves the payload.
func TestFoldTypedValues(t *testing.T) {
	op, del := Fold(Op{}, map[string]any{"name": nil}, map[string]any{"name": "<nil>"})
	if del || op.Payload["name"] != "<nil>" {
		t.Fatalf("payload %v (deleted %v)", op.Payload, del)
	}
	op = Op{Payload: map[string]any{"completed": true}, Base: map[string]any{"completed": "true"}}
	if op, del = Fold(op, nil, map[string]any{"completed": true}); del || op.Payload["completed"] != true {
		t.Fatalf("a bool equal to the text of the base is dropped: %v", op.Payload)
	}
	op = Op{Payload: map[string]any{"x": 5}, Base: map[string]any{"x": float64(3)}}
	if _, del = Fold(op, nil, map[string]any{"x": 3}); !del {
		t.Fatal("a number back to its base (read from JSON) is still sent")
	}
}
