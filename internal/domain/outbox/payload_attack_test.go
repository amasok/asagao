package outbox

import (
	"reflect"
	"strings"
	"testing"
)

// Every key of every payload struct, written with each of the "empty" values the store
// may have written ("", false, 0, [], null), reads and writes back as it was: "" and
// absent and null are kept apart where it matters (nothing is dropped or invented).
func TestAttackEmptyValuesRoundTrip(t *testing.T) {
	for _, k := range AllKinds() {
		typ, _ := PayloadType(k)
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			key := strings.Split(f.Tag.Get("json"), ",")[0]
			var vals []any
			switch ft := f.Type; {
			case ft.Kind() == reflect.String || (ft.Kind() == reflect.Pointer && ft.Elem().Kind() == reflect.String):
				vals = []any{"", "x"}
			case ft.Kind() == reflect.Bool || (ft.Kind() == reflect.Pointer && ft.Elem().Kind() == reflect.Bool):
				vals = []any{false, true}
			case ft.Kind() == reflect.Int:
				vals = []any{float64(0), float64(3)}
			case ft.Kind() == reflect.Slice:
				vals = []any{[]any{}, []any{"a"}}
			case ft.Kind() == reflect.Map:
				vals = []any{map[string]any{}, map[string]any{"a": []any{}}}
			default:
				vals = []any{nil, "x", float64(1.5), []any{}, map[string]any{}}
			}
			for _, v := range vals {
				m := map[string]any{key: v}
				p, err := DecodePayload(Op{Kind: k, Payload: m})
				if err != nil {
					t.Errorf("%s {%s:%#v}: %v", k, key, v, err)
					continue
				}
				back, err := EncodePayload(k, p)
				if err != nil {
					t.Errorf("%s {%s:%#v}: encode %v", k, key, v, err)
					continue
				}
				// keys a struct always writes appear even when the entry lacked them (readers
				// treat "" and absent alike); the key that was set must come back as it was.
				got, present := back[key]
				if !present && (v == "" || v == false || reflect.ValueOf(v).Kind() == reflect.Map && reflect.ValueOf(v).Len() == 0) {
					continue // an "" that only said nothing (omitempty), not one that clears something
				}
				if !present || !reflect.DeepEqual(got, v) {
					t.Errorf("%s: {%s:%#v} written back as %#v", k, key, v, back)
				}
				if false {
				}
			}
		}
	}
}

// null for a key reads as the key being absent for pointer fields; it must not be an
// error ("json null" is what a cleared value may have been stored as by older code).
func TestAttackNullKeysRead(t *testing.T) {
	for _, k := range AllKinds() {
		typ, _ := PayloadType(k)
		m := map[string]any{}
		for i := 0; i < typ.NumField(); i++ {
			m[strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]] = nil
		}
		if _, err := DecodePayload(Op{Kind: k, Payload: m}); err != nil {
			t.Errorf("%s with every key null: %v", k, err)
		}
	}
}
