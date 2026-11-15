package termsafe

import (
	"encoding/json"
	"reflect"
)

// Unmarshal is json.Unmarshal followed by String on every string in v (struct fields, map
// keys and values, slice elements, values held in interfaces). Use it to decode JSON kept in
// the store: its strings may hold control characters as \u escapes, which only decoding turns
// into raw characters.
func Unmarshal(data []byte, v any) error {
	if err := json.Unmarshal(data, v); err != nil {
		return err
	}
	clean(reflect.ValueOf(v))
	return nil
}

// clean sanitizes the strings in v, which must be settable where it holds strings directly.
func clean(v reflect.Value) {
	switch v.Kind() {
	case reflect.Pointer:
		if !v.IsNil() {
			clean(v.Elem())
		}
	case reflect.String:
		if s := v.String(); !Safe(s) && v.CanSet() {
			v.SetString(String(s))
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				clean(v.Field(i))
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			clean(v.Index(i))
		}
	case reflect.Interface:
		if v.IsNil() {
			return
		}
		// what an interface holds is not addressable: clean a copy and put it back
		e := v.Elem()
		c := reflect.New(e.Type()).Elem()
		c.Set(e)
		clean(c)
		if v.CanSet() {
			v.Set(c)
		}
	case reflect.Map:
		if v.IsNil() {
			return
		}
		iter := v.MapRange()
		type kv struct{ k, v reflect.Value }
		var changed []kv
		var stale []reflect.Value
		for iter.Next() {
			k, val := iter.Key(), iter.Value()
			ck := reflect.New(k.Type()).Elem()
			ck.Set(k)
			clean(ck)
			cv := reflect.New(val.Type()).Elem()
			cv.Set(val)
			clean(cv)
			if !ck.Equal(k) {
				stale = append(stale, k)
			}
			changed = append(changed, kv{ck, cv})
		}
		for _, k := range stale {
			v.SetMapIndex(k, reflect.Value{})
		}
		for _, e := range changed {
			v.SetMapIndex(e.k, e.v)
		}
	}
}
