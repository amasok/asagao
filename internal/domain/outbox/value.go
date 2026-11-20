package outbox

import (
	"encoding/json"
	"math"
	"math/big"
	"reflect"
	"strconv"
)

// SameValue reports whether two field values are the same. A value read back from the
// outbox's JSON may not have the type it was written with, so values are compared by what
// JSON keeps of them, type by type:
//
//   - numbers of any Go type are equal when their values are exactly (3 and 3.0 are;
//     integers beyond 2^53 are not rounded to a float64 first; a json.Number holding an
//     integer compares exactly, any other one as the float64 JSON reads); NaN equals
//     nothing;
//   - lists of any element type are equal when they have the same length and their
//     elements are equal in order ([]string{"a"} and []any{"a"} are); objects likewise,
//     key by key;
//   - strings and bools are equal only to a string or a bool of the same value: "3" is not
//     3, "true" is not true, and "<nil>" is not nil;
//   - nil equals only nil (a missing value, not "" nor an empty list: those clear a field,
//     which nil does not say).
//
// Values of other types (structs, pointers, ...) are not written to the outbox; they are
// equal when reflect.DeepEqual says so.
func SameValue(a, b any) bool {
	a, b = jsonShape(a), jsonShape(b)
	switch x := a.(type) {
	case nil:
		return b == nil
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	case string:
		y, ok := b.(string)
		return ok && x == y
	case float64: // NaN or an infinity (finite numbers are *big.Rat)
		y, ok := b.(float64)
		return ok && x == y // NaN != NaN
	case *big.Rat:
		y, ok := b.(*big.Rat)
		return ok && x.Cmp(y) == 0
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !SameValue(x[i], y[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, v := range x {
			w, ok := y[k]
			if !ok || !SameValue(v, w) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(a, b)
}

// jsonShape returns v as encoding/json decodes it into an any, for the kinds of values
// the outbox holds: a number becomes its exact value as a *big.Rat (a float64 NaN or
// infinity stays a float64), a list []any, an object with string keys
// map[string]any, a named string or bool type its plain type. A nil list, map or pointer
// is nil. Other values are returned as they are.
func jsonShape(v any) any {
	switch x := v.(type) {
	case nil, bool, string:
		return v
	case float64:
		return exact(x)
	case []any:
		if x == nil {
			return nil
		}
		return x
	case map[string]any:
		if x == nil {
			return nil
		}
		return x
	case json.Number:
		// An integer exactly; any other number as the float64 JSON reads it as.
		if i, ok := new(big.Int).SetString(string(x), 10); ok {
			return new(big.Rat).SetInt(i)
		}
		if f, err := strconv.ParseFloat(string(x), 64); err == nil {
			return exact(f)
		}
		return string(x) // not a number a float64 holds: compared as written
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Bool:
		return rv.Bool()
	case reflect.String:
		return rv.String()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return new(big.Rat).SetInt64(rv.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return new(big.Rat).SetUint64(rv.Uint())
	case reflect.Float32:
		f := rv.Float()
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return f
		}
		// As JSON writes a float32 (its shortest decimal form) and reads it back.
		if g, err := strconv.ParseFloat(strconv.FormatFloat(f, 'g', -1, 32), 64); err == nil {
			return exact(g)
		}
		return exact(f)
	case reflect.Float64:
		return exact(rv.Float())
	case reflect.Slice, reflect.Array:
		if rv.Kind() == reflect.Slice && rv.IsNil() {
			return nil
		}
		out := make([]any, rv.Len())
		for i := range out {
			out[i] = rv.Index(i).Interface()
		}
		return out
	case reflect.Map:
		if rv.IsNil() {
			return nil
		}
		if rv.Type().Key().Kind() != reflect.String {
			return v
		}
		out := make(map[string]any, rv.Len())
		for it := rv.MapRange(); it.Next(); {
			out[it.Key().String()] = it.Value().Interface()
		}
		return out
	case reflect.Pointer, reflect.Interface:
		if rv.IsNil() {
			return nil
		}
	}
	return v
}

// exact returns the value of f as a *big.Rat, or f itself for a NaN or an infinity
// (which a big.Rat cannot hold).
func exact(f float64) any {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return f
	}
	return new(big.Rat).SetFloat64(f)
}
