package outbox

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

// After the exact comparison of integers: values still compare across Go types, JSON
// round trips of ordinary numbers hold, and odd json.Numbers do not panic.
func TestAttackSameValueExactNumbers(t *testing.T) {
	huge := json.Number(strings.Repeat("9", 400))
	for _, tc := range []struct {
		name string
		a, b any
		want bool
	}{
		{"int64 and uint64", int64(5), uint64(5), true},
		{"negative int and uint", int64(-1), uint64(math.MaxUint64), false},
		{"big int and the same float", int64(1 << 53), float64(1 << 53), true},
		{"big int and the next float", int64(1<<53 + 1), float64(1 << 53), false},
		{"json.Number int exact", json.Number("9007199254740993"), int64(1<<53 + 1), true},
		{"json.Number int differs", json.Number("9007199254740993"), int64(1 << 53), false},
		{"json.Number decimal as float", json.Number("0.1"), 0.1, true},
		{"json.Number 1.0 and 1", json.Number("1.0"), 1, true},
		{"json.Number 1e2 and 100", json.Number("1e2"), 100, true},
		{"json.Number huge and itself", huge, huge, true},
		{"json.Number huge and float", huge, math.MaxFloat64, false},
		{"json.Number overflow float", json.Number("1e999"), json.Number("1e999"), true},
		{"json.Number empty", json.Number(""), "", true},
		{"float32 and its JSON float64", float32(0.1), 0.1, true},
		{"float32 inf", float32(math.Inf(1)), math.Inf(1), true},
		{"-0 and 0 ints", math.Copysign(0, -1), 0, true},
		{"smallest float and zero", math.SmallestNonzeroFloat64, 0, false},
		{"max float", math.MaxFloat64, math.MaxFloat64, true},
		{"inf and max", math.Inf(1), math.MaxFloat64, false},
		{"NaN in list", []any{math.NaN()}, []any{math.NaN()}, false},
		{"rat is not a string", 3, "3", false},
	} {
		if got := SameValue(tc.a, tc.b); got != tc.want {
			t.Errorf("%s: %#v vs %#v = %v, want %v", tc.name, tc.a, tc.b, got, tc.want)
		}
		if got := SameValue(tc.b, tc.a); got != tc.want {
			t.Errorf("%s swapped = %v, want %v", tc.name, got, tc.want)
		}
	}
	// Values the outbox really holds survive JSON.
	for _, v := range []any{0, -7, 3, 2.5, 1e15, 1e-7, int64(1 << 52), 123456789.123456789} {
		b, _ := json.Marshal(v)
		var back any
		if err := json.Unmarshal(b, &back); err != nil || !SameValue(v, back) {
			t.Errorf("%v through JSON: %v (%v)", v, back, err)
		}
	}
}
