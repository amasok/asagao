package richtext

import "testing"

// stableCase is Markdown with the rich text FromMarkdown writes for it; the rich text comes
// back unchanged from an edit (ToEditableMarkdown -> FromMarkdown).
type stableCase struct{ md, html string }

func checkStable(t *testing.T, cases []stableCase) {
	t.Helper()
	for _, c := range cases {
		h1 := FromMarkdown(c.md)
		if h1 != c.html {
			t.Errorf("FromMarkdown(%q)\ngot  %q\nwant %q", c.md, h1, c.html)
			continue
		}
		md2 := ToEditableMarkdown(h1)
		if h2 := FromMarkdown(md2); h2 != h1 {
			t.Errorf("not stable: %q\nh1  %q\nmd2 %q\nh2  %q", c.md, h1, md2, h2)
		}
	}
}

// #72: line breaks typed as CRLF (or CR) are line breaks; no "\r" is left in what is sent.
func TestFromMarkdownCRLF(t *testing.T) {
	checkStable(t, []stableCase{
		{"```\r\na\r\nb\r\n```\r\n", "<body><pre>a\nb</pre></body>"},
		{"a\r\nb\r\n\r\nc", "<body>a\nb\n\nc</body>"},
		{"a\rb", "<body>a\nb</body>"},
		{"> q\r\n> r", "<body><blockquote>q\nr</blockquote></body>"},
		{"- a\r\n- b", "<body><ul><li>a</li><li>b</li></ul></body>"},
	})
	for _, md := range []string{"```\r\na\r\n```", "a\r\nb", "a\rb"} {
		if p := PlainFromMarkdown(md); p != "a" && p != "a\nb" {
			t.Errorf("PlainFromMarkdown(%q) = %q", md, p)
		}
	}
}

// #72: raw HTML as a block is text, with no line break of its own at its end (a quote or
// the text after it would keep it once, and lose it on the next trip).
func TestFromMarkdownHTMLBlockEnd(t *testing.T) {
	checkStable(t, []stableCase{
		{"> <div>x</div>", "<body><blockquote>&lt;div&gt;x&lt;/div&gt;</blockquote></body>"},
		{"> <div>\n> x", "<body><blockquote>&lt;div&gt;\nx</blockquote></body>"},
		{"<div>x</div>\n\np", "<body>&lt;div&gt;x&lt;/div&gt;\n\np</body>"},
		{"<div>x</div>\n\n- a", "<body>&lt;div&gt;x&lt;/div&gt;\n<ul><li>a</li></ul></body>"},
		{"- <div>x</div>\n- b", "<body><ul><li>&lt;div&gt;x&lt;/div&gt;</li><li>b</li></ul></body>"},
	})
}

// #72: a heading with no text in a list item is written as its marker alone ("#"), not "# "
// with a space before what follows (a nested list).
func TestFromMarkdownEmptyHeadingInItem(t *testing.T) {
	checkStable(t, []stableCase{
		{"- #\n  - a", "<body><ul><li>#<ul><li>a</li></ul></li></ul></body>"},
		{"- #", "<body><ul><li>#</li></ul></body>"},
		{"- ##\n- b", "<body><ul><li>##</li><li>b</li></ul></body>"},
	})
}

// #72: in plain text a quote ends its line, as a code block, a heading and a list item do:
// the words before and after it are not run together.
func TestPlainFromMarkdownQuoteEndsLine(t *testing.T) {
	for _, c := range []struct{ md, want string }{
		{"> q\n\n```\ncode\n```", "q\ncode"},
		{"> q\n\n- a", "q\na"},
		{"> q\n\n# h", "q\nh"},
		{"> q\n\n> r", "q\nr"},
		{"> q\n\np", "q\np"},
		{"> > q\n>\n> r", "q\nr"},
	} {
		if got := PlainFromMarkdown(c.md); got != c.want {
			t.Errorf("PlainFromMarkdown(%q) = %q, want %q", c.md, got, c.want)
		}
	}
}
