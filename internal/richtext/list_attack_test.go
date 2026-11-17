package richtext

import (
	"strings"
	"testing"
	"time"
)

// #13 attack: lines after a blank line, indented to (or short of) an open list item's
// content column. The HTML must be stable after one round trip.
func TestListContinuationAttackStable(t *testing.T) {
	srcs := []string{
		"- a\n  - b\n\n  c",
		"- a\n  - b\n\n  - d",
		"- a\n  - b\n\n c",
		"- a\n  - b\n\n    c",
		"- a\n  - b\n\n      c",
		"- a\n  - b\n\n\n  c",
		"- a\n  - b\n\n\tc",
		"- a\n\t- b\n\n\tc",
		"- a\n\t- b\n\n\t\tc",
		"10. a\n    - b\n\n    c",
		"10. a\n    - b\n\n   c",
		"-    a\n\n     b",
		"-     a\n\n      b",
		"-\n  a\n\n  b",
		"- \n\n  a",
		"1.\n\n   a",
		"> - a\n>   - b\n>\n>   c",
		"> - a\n>   - b\n\n  c",
		"> - a\n>\n> - b\n\n  c",
		"- a\n  - b\n>\n  c",
		"- a\n  - b\n\n  *c*",
		"- a\n  - b\n\n  ![x](asana:1)",
		"- a\n  - b\n\n  &#32;c",
		"- a\n  - b\n\n  c\n  d\n\n  e",
		"- a\n  - b\n\n  ```\n  x\n  ```",
		"- a\n  - b\n\n  > q",
		"- a\n  - b\n\n  # h",
		"- a\n  - b\n\n  ---",
		"- a\n  - b\n  c\n\n  d",
		"- a\n  - b\n\nc",
		"- a\n\n  b\n\n  c\n- d",
		"1. a\n   1. b\n\n   c\n2. d",
		"- a\n  1. b\n\n     c\n\n  d",
		"- a\n\n  - b\n\n    c\n\n  d\n\ne",
		"a\n\n  - b\n\n  c",
		"- a\n\n\n\n  b",
		"- a\n  - b\n    - c\n\n    d\n\n  e\n\nf",
	}
	for _, src := range srcs {
		h1 := FromMarkdown(src)
		md := ToEditableMarkdown(h1)
		h2 := FromMarkdown(md)
		if h1 != h2 {
			t.Errorf("src %q\nh1 %q\nmd %q\nh2 %q", src, h1, md, h2)
		}
	}
}

// From HTML: a sentence after a nested list, in several shapes.
func TestListContinuationAttackFromHTML(t *testing.T) {
	ins := []string{
		"<ul><li>a<ul><li>b</li></ul>\n\n  c</li></ul>",
		"<ul><li>a<ul><li>b</li></ul>\n\n<em>c</em></li></ul>",
		"<ul><li>a<ul><li>b</li></ul>\n\n  <img data-asana-gid=\"1\"></li></ul>",
		"<ul><li>a<ul><li>b</li></ul><p>  c</p></li></ul>",
		"<ul><li>a<ul><li>b</li></ul>\n\n\tc</li></ul>",
		"<blockquote><ul><li>a<ul><li>b</li></ul>\n\n  c</li></ul></blockquote>",
		"<ul><li>a</li></ul>  c",
		"<ul><li>a</li></ul>\n\n  c",
		"<ol><li>a</li></ol>   - c",
		"<ul><li>a<ul><li>b</li></ul>\n\n    code</li></ul>",
	}
	for _, in := range ins {
		h := "<body>" + in + "</body>"
		md1, h1, md2, h2 := rt2(h)
		if h1 != h2 {
			t.Errorf("in %q\nmd1 %q\nh1 %q\nmd2 %q\nh2 %q", h, md1, h1, md2, h2)
		}
	}
}

func TestListContinuationExactHTML(t *testing.T) {
	for src, want := range map[string]string{
		"- a\n  - b\n\n  c": "",
	} {
		got := FromMarkdown(src)
		if want == "" {
			if !strings.Contains(got, "c</li>") || strings.Contains(got, "</ul>c") || strings.Contains(got, "</ul>\nc") {
				t.Errorf("%q: c is not inside the list: %q", src, got)
			}
		}
	}
	// the sentence does not become an item of the outer list
	if got := FromMarkdown("- a\n  - b\n\n  - d"); strings.Count(got, "<li>") != 3 {
		t.Errorf("got %q", got)
	}
}

func TestListContinuationLinear(t *testing.T) {
	cases := map[string]string{
		"deep": strings.Repeat("- a\n\n  b\n\n", 5000),
		"nest": func() string {
			var b strings.Builder
			for i := 0; i < 300; i++ {
				b.WriteString(strings.Repeat("  ", i) + "- a\n\n")
			}
			return b.String() + strings.Repeat(" ", 600) + "z"
		}(),
		"blank": "- a\n" + strings.Repeat("\n", 50000) + "  b",
		"quote": strings.Repeat("> - a\n>\n  b\n", 5000),
	}
	for name, src := range cases {
		start := time.Now()
		h := FromMarkdown(src)
		_ = ToEditableMarkdown(h)
		if d := time.Since(start); d > 10*time.Second {
			t.Errorf("%s took %v", name, d)
		}
	}
}

// Content columns: the marker width, 5+ spaces after the marker, and tabs.
func TestListContinuationColumns(t *testing.T) {
	for src, want := range map[string]string{
		"-     a\n\n  b":  "<body><ul><li><code>a</code>\nb</li></ul></body>",
		"-\ta\n\n    b":   "<body><ul><li>a\nb</li></ul></body>",
		"-\ta\n\n   b":    "<body><ul><li>a</li></ul>   b</body>",
		"-    a\n\n \tb":  "<body><ul><li>a</li></ul> \tb</body>",
		"-    a\n\n    b": "<body><ul><li>a</li></ul>    b</body>",
		"10. a\n\n    b":  "<body><ol><li>a\nb</li></ol></body>",
		"10. a\n\n   b":   "<body><ol><li>a</li></ol>   b</body>",
	} {
		if got := FromMarkdown(src); got != want {
			t.Errorf("%q: got %q, want %q", src, got, want)
		}
	}
}
