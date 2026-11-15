package termsafe

import (
	"bytes"
	"encoding/json"
	"testing"
)

func controlFree(s string) bool {
	for _, r := range s {
		if (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return false
		}
	}
	return true
}

// walk calls f on every string (and map key) in a decoded JSON value.
func walk(v any, f func(string)) {
	switch x := v.(type) {
	case string:
		f(x)
	case []any:
		for _, e := range x {
			walk(e, f)
		}
	case map[string]any:
		for k, e := range x {
			f(k)
			walk(e, f)
		}
	}
}

// A UTF-8 encoded C1 control split across two writes must still be dropped.
func TestWriterSplitC1(t *testing.T) {
	var b bytes.Buffer
	w := Writer(&b)
	for _, p := range []string{"a\xc2", "\x9b2J", "\xc2", "\x9d52;c;eA==\xc2", "\x9c", "z"} {
		if _, err := w.Write([]byte(p)); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := b.String(), "a2J52;c;eA==z"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// Controls written as JSON escapes in all their spellings are gone after Unmarshal; a
// literal backslash before "u001b" (\\u001b) is text and stays.
func TestUnmarshalEscapeSpellings(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{`"\u001B[2J"`, "[2J"},
		{`"\u009B2J\u009b"`, "2J"},
		{`"a\u007Fb\u0000c\u000Dd\u0008e"`, "abcde"},
		{`"\b\f\rx"`, "x"},
		{`"\\u001b"`, `\u001b`},
		{`"\\\u001b"`, `\`},
		{`"\"\u001b]52;c;eA==\u0007"`, `"]52;c;eA==`},
		{`"\n\t\u000a\u0009"`, "\n\t\n\t"},
		{`"\ud83d\ude00\u00a0"`, "😀\u00a0"},
		{`"\ud800"`, "\ufffd"},
	} {
		var s string
		if err := Unmarshal([]byte(c.in), &s); err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if s != c.want {
			t.Errorf("Unmarshal(%s) = %q, want %q", c.in, s, c.want)
		}
	}
}

type inner struct {
	Name string
	Tags []string
}

type outer struct {
	Name    string
	Ptr     *string
	Inner   inner
	PInner  *inner
	Any     any
	Map     map[string]any
	ByKey   map[string]string
	List    []inner
	Arr     [2]string
	Ptrs    []*inner
	private string //nolint:unused // json ignores it; clean must not panic on it
}

// Every place a string can land in a decoding target is cleaned, and map keys that become
// equal after cleaning do not panic.
func TestUnmarshalReachesEveryString(t *testing.T) {
	const e = `\u001b]52;c;eA==\u0007\u009b2J`
	in := `{"Name":"n` + e + `","Ptr":"p` + e + `","Inner":{"Name":"i` + e + `","Tags":["t` + e + `"]},
		"PInner":{"Name":"pi` + e + `"},"Any":{"k` + e + `":["a` + e + `",{"deep":"d` + e + `"}]},
		"Map":{"m` + e + `":"v` + e + `","x":{"y":"z` + e + `"}},"ByKey":{"a` + e + `":"1","a]52;c;eA==2J":"2"},
		"List":[{"Name":"l` + e + `"}],"Arr":["r` + e + `","s"],"Ptrs":[{"Name":"pp` + e + `"},null]}`
	var o outer
	if err := Unmarshal([]byte(in), &o); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	n := 0
	walk(v, func(s string) {
		n++
		if !controlFree(s) {
			t.Errorf("control characters left: %q", s)
		}
	})
	if n < 15 {
		t.Errorf("only %d strings seen: %s", n, b)
	}

	// decoding straight into an interface or a map
	var a any
	if err := Unmarshal([]byte(`[{"k`+e+`":"v`+e+`"},"s`+e+`"]`), &a); err != nil {
		t.Fatal(err)
	}
	walk(a, func(s string) {
		if !controlFree(s) {
			t.Errorf("any: control characters left: %q", s)
		}
	})
	m := map[string]any{}
	if err := Unmarshal([]byte(`{"k`+e+`":["v`+e+`"]}`), &m); err != nil {
		t.Fatal(err)
	}
	walk(map[string]any(m), func(s string) {
		if !controlFree(s) {
			t.Errorf("map: control characters left: %q", s)
		}
	})
}

// Whatever goes in, String's output is Safe and stable, and whatever Unmarshal decodes
// holds no control characters.
func FuzzSanitize(f *testing.F) {
	for _, s := range []string{
		"", "plain", "\x1b]52;c;eA==\x07", "\xc2\x9b", "\x9b", "\xff\xfe", "a\r\b\x7f",
		`{"a":"\u001b"}`, `["\u009B"]`, `"\u001b"`, `["\\u001b"]`, `[1,"\ud800"]`,
		`{"x":["\u001b",{"y\u0007":"\u0007"}]}`, " [\"\\r\"]",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out := String(s)
		if !Safe(out) || !controlFree(out) {
			t.Fatalf("String(%q) = %q", s, out)
		}
		if String(out) != out {
			t.Fatalf("String is not idempotent on %q", s)
		}
		check := func(data string) {
			var v any
			if Unmarshal([]byte(data), &v) != nil {
				return
			}
			walk(v, func(x string) {
				if !controlFree(x) {
					t.Fatalf("Unmarshal(%q) left %q", data, x)
				}
			})
		}
		check(s)
		b, _ := json.Marshal(map[string]any{"s": s, s: []string{s}})
		check(string(b))
	})
}
