package termsafe

import (
	"bytes"
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

// JSON returns the JSON document b indented with indent, made safe for a terminal without
// changing its values: the characters String would drop are written as \uXXXX escapes
// (JSON allows DEL and the C1 controls raw inside strings), and invalid UTF-8 as the
// escape of U+FFFD. The result is still valid JSON, for a pipe to jq as well. ok is false
// when b is not valid JSON (the caller then shows it through String).
func JSON(b []byte, indent string) (out []byte, ok bool) {
	b = bytes.TrimSpace(b)
	if !json.Valid(b) {
		return nil, false
	}
	var ind bytes.Buffer
	if err := json.Indent(&ind, b, "", indent); err != nil {
		return nil, false
	}
	src := ind.Bytes()
	// Outside strings a valid, indented document has only ASCII tokens, spaces and \n:
	// whatever needs escaping is inside a string, where an escape keeps it valid.
	var buf bytes.Buffer
	buf.Grow(len(src))
	for len(src) > 0 {
		r, w := utf8.DecodeRune(src)
		switch {
		case r == utf8.RuneError && w == 1:
			fmt.Fprintf(&buf, `\u%04x`, utf8.RuneError)
		case unsafeRune(r):
			fmt.Fprintf(&buf, `\u%04x`, r)
		default:
			buf.Write(src[:w])
		}
		src = src[w:]
	}
	return buf.Bytes(), true
}
