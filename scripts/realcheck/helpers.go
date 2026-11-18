package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
)

// M is a decoded JSON object.
type M = map[string]any

// get walks a decoded JSON value: keys for objects, "#gid" for "the gids of an array".
func get(v any, path ...string) any {
	for _, p := range path {
		switch x := v.(type) {
		case M:
			v = x[p]
		case []any:
			if p == "#gid" || strings.HasPrefix(p, "#") {
				key := strings.TrimPrefix(p, "#")
				out := []any{}
				for _, e := range x {
					out = append(out, get(e, key))
				}
				v = out
			} else {
				return nil
			}
		default:
			return nil
		}
	}
	return v
}

func str(v any, path ...string) string {
	s, _ := get(v, path...).(string)
	return s
}

func strs(v any, path ...string) []string {
	var out []string
	if a, ok := get(v, path...).([]any); ok {
		for _, e := range a {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

func boolean(v any, path ...string) bool {
	b, _ := get(v, path...).(bool)
	return b
}

func decode(data json.RawMessage) any {
	var v any
	_ = json.Unmarshal(data, &v)
	return v
}

// same reports whether the cache and Asana agree and both are want. It returns a detail
// for the report when they do not.
func same(what string, cache, asana, want any) (bool, string) {
	cache, asana, want = normalize(cache), normalize(asana), normalize(want)
	ok := reflect.DeepEqual(cache, want) && reflect.DeepEqual(asana, want)
	if ok {
		return true, ""
	}
	return false, fmt.Sprintf("%s: 期待 %v / キャッシュ %v / Asana %v", what, show(want), show(cache), show(asana))
}

func show(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func containsAll(have []string, want ...string) bool {
	for _, w := range want {
		if !slices.Contains(have, w) {
			return false
		}
	}
	return true
}

// nilIfEmpty turns "" into nil, so a missing key and an empty string compare equal.
func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// renamedColors is what Asana's GET answers for each written color (measured with -colors;
// the same table as internal/domain/tag, which this program does not import).
var renamedColors = map[string]string{
	"dark-pink": "hot-pink", "dark-green": "green", "dark-blue": "blue", "dark-red": "red",
	"dark-teal": "aqua", "dark-brown": "yellow", "dark-orange": "orange", "dark-purple": "indigo",
	"dark-warm-gray": "cool-gray", "light-pink": "magenta", "light-green": "yellow-green",
	"light-blue": "blue", "light-red": "pink", "light-teal": "blue-green", "light-yellow": "yellow",
	"light-orange": "yellow-orange", "light-purple": "purple", "light-warm-gray": "cool-gray",
}

// canonColor names a color as Asana's GET does; no color ("" / "none") is "".
func canonColor(c string) string {
	c = strings.ToLower(strings.TrimSpace(c))
	if c == "none" {
		return ""
	}
	if n, ok := renamedColors[c]; ok {
		return n
	}
	return c
}
