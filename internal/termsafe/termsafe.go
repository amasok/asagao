// Package termsafe removes terminal control characters from text that did not come from the
// app itself (task names, notes, comments, notifications from Asana or the local store), so
// that such text cannot drive the terminal: no OSC 52 clipboard writes, fake OSC 8 links,
// screen rewrites, or kitty graphics commands (APC).
//
// It drops C0 controls except \n and \t, DEL, and C1 controls (U+0080–U+009F), and replaces
// invalid UTF-8 with U+FFFD. Dropping ESC and the C1 introducers is enough: what is left of an
// escape sequence is plain, visible text.
package termsafe

import (
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

func unsafeRune(r rune) bool {
	switch {
	case r == '\n' || r == '\t':
		return false
	case r < 0x20 || r == 0x7f:
		return true
	case r >= 0x80 && r <= 0x9f:
		return true
	}
	return false
}

// Safe reports whether s has nothing String would change.
func Safe(s string) bool {
	for i, r := range s {
		if unsafeRune(r) {
			return false
		}
		if r == utf8.RuneError {
			if _, w := utf8.DecodeRuneInString(s[i:]); w == 1 {
				return false
			}
		}
	}
	return true
}

// String returns s without terminal control characters (see the package doc).
func String(s string) string {
	if Safe(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i, r := range s {
		if r == utf8.RuneError {
			if _, w := utf8.DecodeRuneInString(s[i:]); w == 1 {
				b.WriteRune(utf8.RuneError)
				continue
			}
		}
		if !unsafeRune(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Writer returns a writer that passes everything written to it through String. A multibyte
// character split across two writes is kept whole.
func Writer(w io.Writer) io.Writer { return &writer{w: w} }

type writer struct {
	w       io.Writer
	pending []byte // the start of a multibyte character whose rest has not been written yet
}

func (t *writer) Write(p []byte) (int, error) {
	buf := append(t.pending, p...)
	cut := incompleteTail(buf)
	t.pending = append([]byte(nil), buf[cut:]...)
	if _, err := io.WriteString(t.w, String(string(buf[:cut]))); err != nil {
		return 0, err
	}
	return len(p), nil
}

// incompleteTail is where an unfinished UTF-8 sequence at the end of b starts (len(b) if none).
func incompleteTail(b []byte) int {
	for i := len(b) - 1; i >= 0 && i > len(b)-utf8.UTFMax; i-- {
		if utf8.RuneStart(b[i]) {
			if !utf8.FullRune(b[i:]) {
				return i
			}
			break
		}
	}
	return len(b)
}

// JSON is String for JSON text: it also removes control characters written as escapes inside
// its strings (\u001b, \u009b, \b, \f, \r; \n, \t and \u000a/\u0009 stay), so decoding the
// result cannot produce them. Text that is not valid JSON gets String only.
func JSON(s string) string {
	s = String(s)
	if !strings.Contains(s, `\`) || !json.Valid([]byte(s)) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	inString := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !inString {
			inString = c == '"'
			b.WriteByte(c)
			continue
		}
		switch c {
		case '"':
			inString = false
			b.WriteByte(c)
		case '\\': // valid JSON: a complete escape follows
			switch s[i+1] {
			case 'b', 'f', 'r':
				i++
			case 'u':
				if r, err := strconv.ParseUint(s[i+2:i+6], 16, 32); err == nil && unsafeRune(rune(r)) {
					i += 5
					continue
				}
				b.WriteString(s[i : i+6])
				i += 5
			default:
				b.WriteString(s[i : i+2])
				i++
			}
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}
