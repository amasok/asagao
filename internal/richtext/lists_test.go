package richtext

import (
	"regexp"
	"strings"
	"testing"
)

// #61: spaces at the ends of a link's text go outside the link, so the words around it stay
// apart (Markdown would drop them inside "[ b ]"... as the link's text is trimmed).
func TestLinkEdgeSpaces(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{`<body>a<a href="http://x.jp"> b c </a>d</body>`, `<body>a <a href="http://x.jp">b c</a> d</body>`},
		{`<body>a<a href="http://x.jp">` + "\t" + `b</a>d</body>`, "<body>a\t" + `<a href="http://x.jp">b</a>d</body>`},
		{`<body>a<a href="http://x.jp">b<br></a>c</body>`, `<body>a<a href="http://x.jp">b</a>` + "\nc</body>"},
		{`<body><ul><li>a<a href="http://x.jp"> b</a></li></ul></body>`, `<body><ul><li>a <a href="http://x.jp">b</a></li></ul></body>`},
		{`<body>a <a href="http://x.jp">b</a> c</body>`, ""},
	} {
		want := c.want
		if want == "" {
			want = c.in
		}
		md1, h1, md2, h2 := roundtrip(c.in)
		if h1 != want || h2 != h1 {
			t.Errorf("in %q\nmd1 %q\nh1  %q\nmd2 %q\nh2  %q\nwant %q", c.in, md1, h1, md2, h2, want)
		}
	}
}

// #61: lists next to each other stay apart. Asana's rich text documentation says nothing
// against them, and merging them would change what was written; Markdown tells them apart
// by their markers ("-" then "*", "1." then "1)"), so ToEditableMarkdown switches markers.
func TestAdjacentListsStayApart(t *testing.T) {
	for _, h := range []string{
		"<body><ul><li>a</li></ul><ul><li>b</li></ul></body>",
		"<body><ol><li>a</li></ol><ol><li>b</li></ol></body>",
		"<body><ul><li>a</li></ul><ul><li>b</li></ul><ul><li>c</li></ul></body>",
		"<body><ol><li>a</li></ol><ol><li>b</li></ol><ol><li>c</li><li>d</li></ol></body>",
		"<body><ul><li>a</li></ul><ol><li>b</li></ol><ul><li>c</li></ul></body>",
		"<body><ul><li>a<ul><li>b</li></ul><ul><li>c</li></ul></li></ul></body>",
		"<body><ul><li></li></ul><ul><li>b</li></ul></body>",
		"<body><ul><li>a</li></ul>x\n<ul><li>b</li></ul></body>",
	} {
		md1, h1, md2, h2 := roundtrip(h)
		if h1 != h || h2 != h1 {
			t.Errorf("in %q\nmd1 %q\nh1  %q\nmd2 %q\nh2  %q", h, md1, h1, md2, h2)
		}
	}
	// whitespace between them is layout
	if _, h1, _, _ := roundtrip("<body><ul><li>a</li></ul>\n<ul><li>b</li></ul></body>"); h1 != "<body><ul><li>a</li></ul><ul><li>b</li></ul></body>" {
		t.Errorf("h1 %q", h1)
	}
	checkStable(t, []stableCase{
		{"- a\n\n* b", "<body><ul><li>a</li></ul><ul><li>b</li></ul></body>"},
		{"1. a\n1) b", "<body><ol><li>a</li></ol><ol><li>b</li></ol></body>"},
	})
}

// #59: text after a nested list in an item (which editable Markdown cannot write there:
// Markdown reads it as the nested list's last item going on) is a new line of that last
// item, as Markdown reads it, both ways: FromMarkdown sends it so, and rich text holding it
// comes back so from an edit, with its words apart. Then a trip changes nothing.
func TestTextAfterNestedList(t *testing.T) {
	checkStable(t, []stableCase{
		{"- # x\n  - a\n  # y", "<body><ul><li># x<ul><li>a\n# y</li></ul></li></ul></body>"},
		{"- a\n  - b\n  > q", "<body><ul><li>a<ul><li>b\n&gt; q</li></ul></li></ul></body>"},
		{"1. a\n   - b\n   ```\n   x\n   ```", "<body><ol><li>a<ul><li>b\n<code>x</code></li></ul></li></ol></body>"},
		{"- a\n  - b\n    - c\n  # y", "<body><ul><li>a<ul><li>b<ul><li>c\n# y</li></ul></li></ul></li></ul></body>"},
		{"- a\n  - b\n  # c\n  * d\n  # e", "<body><ul><li>a<ul><li>b\n# c</li></ul><ul><li>d\n# e</li></ul></li></ul></body>"},
		{"- a\n  - b\n  # c\n- f", "<body><ul><li>a<ul><li>b\n# c</li></ul></li><li>f</li></ul></body>"},
	})
	if p := PlainFromMarkdown("- # x\n  - a\n  # y"); p != "# x\na\n# y" {
		t.Errorf("plain = %q", p)
	}
	for _, c := range []struct{ in, want string }{
		{"<body><ul><li>a<ul><li>b</li></ul>c</li></ul></body>", "<body><ul><li>a<ul><li>b\nc</li></ul></li></ul></body>"},
		{"<body><ul><li>a<ul><li>b</li></ul> c</li></ul></body>", "<body><ul><li>a<ul><li>b\n c</li></ul></li></ul></body>"},
		{"<body><ul><li>a<ul><li></li></ul>c</li></ul></body>", "<body><ul><li>a<ul><li>c</li></ul></li></ul></body>"},
		{"<body><ul><li>a<ul><li>b</li></ul><strong>c</strong> d</li></ul></body>", "<body><ul><li>a<ul><li>b\n<strong>c</strong> d</li></ul></li></ul></body>"},
		{"<body><ol><li>a<ol><li>b</li></ol>c<ol><li>d</li></ol>e</li></ol></body>", "<body><ol><li>a<ol><li>b\nc</li></ol><ol><li>d\ne</li></ol></li></ol></body>"},
		{"<body><ul><li>a<ul><li>b<ul><li>c</li></ul></li></ul>y</li></ul></body>", "<body><ul><li>a<ul><li>b<ul><li>c\ny</li></ul></li></ul></li></ul></body>"},
		{"<body><ul><li>a<ul><li>b</li></ul>\n# c</li></ul></body>", "<body><ul><li>a<ul><li>b\n# c</li></ul></li></ul></body>"},
		// what Asana's rich text does not take in an item is its text there, as FromMarkdown
		// sends it (#60), and the list stays one list
		{"<body><ul><li>a<blockquote>q</blockquote></li></ul></body>", "<body><ul><li>a\n&gt; q</li></ul></body>"},
		{"<body><ul><li>a<blockquote>q\nr<br><strong>s</strong></blockquote></li></ul></body>", "<body><ul><li>a\n&gt; q\n&gt; r\n&gt; <strong>s</strong></li></ul></body>"},
		{"<body><ul><li>a<pre>x</pre>b</li></ul></body>", "<body><ul><li>a\n<code>x</code>\nb</li></ul></body>"},
		{"<body><ol><li>a<pre>x\n\ny</pre></li><li>b</li></ol></body>", "<body><ol><li>a\n<code>x</code>\n\n<code>y</code></li><li>b</li></ol></body>"},
		{"<body><ol><li>a<h1><em>h</em></h1>b</li><li>c</li></ol></body>", "<body><ol><li>a\n# <em>h</em>\nb</li><li>c</li></ol></body>"},
		{"<body><ul><li>a<blockquote><ul><li>q</li></ul></blockquote></li></ul></body>", "<body><ul><li>a\n&gt; - q</li></ul></body>"},
	} {
		md1, h1, md2, h2 := roundtrip(c.in)
		if h1 != c.want || h2 != h1 {
			t.Errorf("in %q\nmd1 %q\nh1  %q\nmd2 %q\nh2  %q\nwant %q", c.in, md1, h1, md2, h2, c.want)
		}
	}
}

// #80: a rule in a list item is sent as its text, as typed, as a heading, a quote or code
// there is (#60): editable Markdown would write it after the list, and Asana's rich text
// documentation does not say an item takes <hr/>.
func TestRuleInItemIsText(t *testing.T) {
	checkStable(t, []stableCase{
		{"- a\n\n  ---", "<body><ul><li>a\n---</li></ul></body>"},
		{"- a\n  ***", "<body><ul><li>a\n***</li></ul></body>"},
		{"- # x\n  ***", "<body><ul><li># x\n***</li></ul></body>"},
		{"1. ---", "<body><ol><li>---</li></ol></body>"},
		{"- a\n  - - -", "<body><ul><li>a\n- - -</li></ul></body>"},
	})
	if h := FromMarkdown("---\n\n- a"); h != "<body><hr/><ul><li>a</li></ul></body>" {
		t.Errorf("a rule outside a list is a rule: %q", h)
	}
	// rich text with one (not sent by FromMarkdown) comes back with it as text (#59)
	if _, h1, _, h2 := roundtrip("<body><ol><li>a<hr/></li><li>b</li></ol></body>"); h1 != "<body><ol><li>a\n---</li><li>b</li></ol></body>" || h2 != h1 {
		t.Errorf("h1 %q h2 %q", h1, h2)
	}
}

// A quote in an item that holds a paragraph (or anything else not written as text) stays a
// block, untouched: its words in their order (review M1). A block written as text in a quote ends
// its line (review L3). A line break between lists does not make them one (review L2).
func TestQuoteInItemLeftAsIs(t *testing.T) {
	for _, c := range []struct{ in, md string }{
		{"<body><ul><li>a<blockquote>t1<ul><li>x</li></ul>t2</blockquote>z</li></ul></body>", ""},
		{"<body><ul><li>a<blockquote>t1<p>x</p>t2</blockquote>z</li></ul></body>", ""},
		{"<body><ul><li>a<blockquote>x<h1>h</h1>y<ol><li>z</li></ol>w</blockquote></li></ul></body>", ""},
	} {
		md := ToEditableMarkdown(c.in)
		if c.md != "" && md != c.md {
			t.Errorf("ToEditableMarkdown(%q)\ngot  %q\nwant %q", c.in, md, c.md)
		}
		if got, want := wordOrder(md), wordOrder(nodeTextOf(c.in)); got != want {
			t.Errorf("words of %q: %q, want %q (md %q)", c.in, got, want, md)
		}
	}
	for _, c := range []struct{ in, want string }{
		{"<body><ul><li>a<blockquote><strong>x</strong><pre>p</pre>y</blockquote></li></ul></body>", "<body><ul><li>a\n&gt; <strong>x</strong>\n&gt; <code>p</code>\n&gt; y</li></ul></body>"},
		{"<body><ul><li>a</li></ul><br><ul><li>b</li></ul></body>", "<body><ul><li>a</li></ul><ul><li>b</li></ul></body>"},
		// a list in a quote in an item is text too, as FromMarkdown sends it
		{"<body><ul><li>a<blockquote>t1<ul><li>x<ol><li>y</li></ol></li></ul>t2</blockquote>z</li><li>b</li></ul></body>",
			"<body><ul><li>a\n&gt; t1\n&gt; - x\n&gt;   1. y\n&gt; t2\nz</li><li>b</li></ul></body>"},
	} {
		md1, h1, md2, h2 := roundtrip(c.in)
		if h1 != c.want || h2 != h1 {
			t.Errorf("in %q\nmd1 %q\nh1  %q\nmd2 %q\nh2  %q\nwant %q", c.in, md1, h1, md2, h2, c.want)
		}
	}
}

// wordOrder is the letters and digits of s in order.
func wordOrder(s string) string {
	return strings.Join(itemWordRe.FindAllString(s, -1), "")
}

var itemWordRe = regexp.MustCompile(`[a-z]+[0-9]*`)
