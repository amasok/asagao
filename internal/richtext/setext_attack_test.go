package richtext

import (
	"strings"
	"testing"
	"time"
)

// #12 attack: indented lines, setext underlines, quotes, lists, tabs. The HTML read from
// Markdown must be stable (FromMarkdown(ToEditableMarkdown(h)) == h) after one pass.
func TestSetextIndentAttackStable(t *testing.T) {
	srcs := []string{
		"   a\n  ---",
		"   a\n    ---",
		"   a\n  ===",
		"    a\n---",
		"  a\n  b\n  ---",
		"  a\n  b\n---",
		"a\n  b\n    ---",
		"\ta\n---",
		"a\n\tb\n---",
		"  a\n \t---",
		"> a\n  b\n---",
		"> a\n> b\n  ---",
		"> a\n  ---",
		"> > a\n> b\n---",
		"> > a\n  b\n  ---",
		"- a\n  b\n  ---",
		"- a\n   ---",
		"- a\n  ---\n  b",
		"1. a\n   ---",
		"2. x\n---",
		"a\n2. x\n  ---",
		"a\n-\n  ---",
		"a\n  - \n---",
		"```\n  a\n  ---\n```",
		"  a\n```\n  ---",
		"   a  \n   ---",
		"   a\\\n  ---",
		"   *a*\n   ---",
		"   [a](u)\n   ---",
		"  a\n  ---\n  b\n  ---",
		"   a\n  --- x",
		"   a\n  - - -",
		"   a\n  ***",
		"# t\n   a\n  ---",
		"   ---\n  a",
		"  \n  a\n  ---",
	}
	for _, src := range srcs {
		h1 := FromMarkdown(src)
		md := ToEditableMarkdown(h1)
		h2 := FromMarkdown(md)
		if h1 != h2 {
			t.Errorf("src %q\nh1 %q\nmd %q\nh2 %q", src, h1, md, h2)
		}
		_ = PlainFromMarkdown
	}
}

// A paragraph's indented lines stay as text when no underline follows.
func TestSetextIndentKeepsPlainIndent(t *testing.T) {
	h := FromMarkdown("a\n   b\n   c")
	if h2 := FromMarkdown(ToEditableMarkdown(h)); h2 != h {
		t.Errorf("%q != %q", h, h2)
	}
}

// Linear: a long run of indented lines (with and without an underline) must not blow up.
func TestSetextIndentLinear(t *testing.T) {
	for _, tail := range []string{"", "\n---"} {
		src := strings.Repeat("   a\n", 20000) + "   b" + tail
		start := time.Now()
		FromMarkdown(src)
		if d := time.Since(start); d > 5*time.Second {
			t.Errorf("tail %q took %v", tail, d)
		}
	}
	src := strings.Repeat("> a\n  b\n", 10000)
	start := time.Now()
	FromMarkdown(src)
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("lazy lines took %v", d)
	}
}

// The underline may be indented by 3 spaces at most; a deeper quote ends the paragraph.
func TestSetextIndentExactHTML(t *testing.T) {
	for src, want := range map[string]string{
		"   a\n  ---":       "<body><h2>a</h2></body>",
		"   a\n   ===":      "<body><h1>a</h1></body>",
		"   a\n    ---":     "<body>   a\n---</body>",
		"a\n    ---":        "<body>a\n---</body>",
		"> a\n> > b\n  ---": "<body><blockquote>a\n<blockquote>b</blockquote></blockquote><hr/></body>",
		"> a\n   b\n> > c":  "<body><blockquote>a\n   b\n<blockquote>c</blockquote></blockquote></body>",
	} {
		if got := FromMarkdown(src); got != want {
			t.Errorf("%q: got %q, want %q", src, got, want)
		}
	}
}
