package termsafe

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

// JSON keeps the values (also with raw DEL, C1 controls and invalid UTF-8 in strings and
// keys), leaves nothing a terminal acts on, and its output is valid JSON.
func TestJSONAttackValuesKeptAndSafe(t *testing.T) {
	for _, in := range []string{
		"{\"a\x7f\":\"b\x7f\u009b2J\u0085\"}",
		"[\"\xff\xfe\",\"\xc3\x28\",\"\xf0\x9f\"]",
		`{"\u001b[2J":"\u0007\u0000\u009b"}`,
		"{\"k\":\"\u009d0;title\u009c\"}",
		"[1,2.5e10,-0,true,false,null,\"\\\\u001b\"]",
		"  \n{\"a\":[]}\n\n",
		`"` + string([]byte{0x7f, 0x80 /* invalid */}) + `"`,
		`{"a":{"b":{"c":["  ‮"]}}}`,
	} {
		out, ok := JSON([]byte(in), "  ")
		if !ok {
			t.Errorf("JSON(%q) not ok", in)
			continue
		}
		if !json.Valid(out) {
			t.Errorf("JSON(%q) = %q is not valid JSON", in, out)
		}
		if !Safe(string(out)) {
			t.Errorf("JSON(%q) = %q has unsafe characters", in, out)
		}
		var a, b any
		if err := json.Unmarshal([]byte(in), &a); err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if err := json.Unmarshal(out, &b); err != nil || !reflect.DeepEqual(a, b) {
			t.Errorf("JSON(%q): values changed: %v vs %v (%v)", in, a, b, err)
		}
	}
}

// Not JSON: ok is false, so the caller uses String. Also trailing garbage, empty, BOM.
func TestJSONAttackNotJSON(t *testing.T) {
	for _, in := range []string{"", "   ", "{", "{} {}", "{}x", "\xef\xbb\xbf{}", "<html>\x1b[2J</html>", "{'a':1}", "{\"a\":\"\x1b\"}", "nul"} {
		if out, ok := JSON([]byte(in), "  "); ok {
			t.Errorf("JSON(%q) ok = true, out %q", in, out)
		}
	}
}

// A big document does not blow up and gives the same document.
func TestJSONAttackBig(t *testing.T) {
	var b bytes.Buffer
	b.WriteString(`{"data":[`)
	for i := 0; i < 20000; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString("{\"n\":\"\x7fx\"}")
	}
	b.WriteString(`]}`)
	out, ok := JSON(b.Bytes(), "  ")
	if !ok || !json.Valid(out) || !Safe(string(out)) {
		t.Fatal("big document")
	}
}
