package termsafe

import (
	"encoding/json"
	"reflect"
	"testing"
)

// JSON escapes what String would drop, so the document is safe for a terminal, still
// valid, and holds the same values.
func TestJSONEscapesControlsKeepingValues(t *testing.T) {
	in := []byte("  {\"name\":\"a\u009b31mb\u007fc\\u001b]52;;x\\u0007\",\"n\":[1,true,null]}\r\n")
	out, ok := JSON(in, "  ")
	if !ok {
		t.Fatal("not JSON")
	}
	if !Safe(string(out)) {
		t.Errorf("output has control characters: %q", out)
	}
	var want, got any
	_ = json.Unmarshal(in, &want)
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("values changed: %v, want %v", got, want)
	}
	if string(out) != "{\n  \"name\": \"a\\u009b31mb\\u007fc\\u001b]52;;x\\u0007\",\n  \"n\": [\n    1,\n    true,\n    null\n  ]\n}" {
		t.Errorf("out = %s", out)
	}
}

// Invalid UTF-8 inside a string becomes U+FFFD; what is not JSON is reported.
func TestJSONInvalidInput(t *testing.T) {
	out, ok := JSON([]byte("{\"a\":\"x\xffy\"}"), "  ")
	var v struct{ A string }
	if !ok || !Safe(string(out)) || json.Unmarshal(out, &v) != nil || v.A != "x�y" {
		t.Errorf("invalid UTF-8: %q %v", out, ok)
	}
	for _, s := range []string{"", "<html>", "{\"a\":1", "\x1b[31m"} {
		if _, ok := JSON([]byte(s), ""); ok {
			t.Errorf("JSON(%q) ok", s)
		}
	}
}
