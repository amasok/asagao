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

// #65: inline code over a line break keeps the line break: each line is code of its own,
// and the line break is text between them (a code span cannot hold one: Markdown reads it
// as a space, and a blank line would end the paragraph).
func TestInlineCodeOverLineBreak(t *testing.T) {
	checkStable(t, []stableCase{
		{"`x\ny`", "<body><code>x</code>\n<code>y</code></body>"},
		{"a `x\ny` b", "<body>a <code>x</code>\n<code>y</code> b</body>"},
		{"- `x\n  y`", "<body><ul><li><code>x</code>\n<code>y</code></li></ul></body>"},
		{"> `x\n> y`", "<body><blockquote><code>x</code>\n<code>y</code></blockquote></body>"},
	})
	for _, c := range []struct{ in, want string }{
		{"<body><code>x\ny</code></body>", "<body><code>x</code>\n<code>y</code></body>"},
		{"<body>a <code>x\n\ny</code> b</body>", "<body>a <code>x</code>\n\n<code>y</code> b</body>"},
		{"<body><code>\nx\n</code></body>", "<body><code>x</code></body>"},
		{"<body><ul><li><code>x\n\n# y</code></li></ul></body>", "<body><ul><li><code>x</code>\n\n<code># y</code></li></ul></body>"},
		{"<body><blockquote><code>x\n- y</code></blockquote></body>", "<body><blockquote><code>x</code>\n<code>- y</code></blockquote></body>"},
		{"<body><strong><code>x\ny</code></strong></body>", "<body><strong><code>x</code>\n<code>y</code></strong></body>"},
	} {
		md1, h1, md2, h2 := roundtrip(c.in)
		if h1 != c.want || h2 != h1 {
			t.Errorf("in %q\nmd1 %q\nh1  %q\nmd2 %q\nh2  %q\nwant %q", c.in, md1, h1, md2, h2, c.want)
		}
	}
	if p := PlainFromMarkdown("`x\ny`"); p != "x\ny" {
		t.Errorf("plain = %q", p)
	}
}

// #65: a code block keeps its lines as they are: blank lines in a row, lines of only spaces,
// and a line break at its start. Asana's rich text is XML, where "<pre>\nx" holds the line
// break; an HTML reader drops it, so ToEditableMarkdown puts it back before reading.
func TestCodeBlockKeepsLines(t *testing.T) {
	for _, h := range []string{
		"<body><pre>a\n\n\nb</pre></body>",
		"<body><pre>a\n  \n\t\nb</pre></body>",
		"<body><pre>\nx</pre></body>",
		"<body><pre>\n\n x</pre></body>",
		"<body><blockquote><pre>a\n\n\n  \nb</pre></blockquote></body>",
		"<body><blockquote><pre>\nx</pre></blockquote></body>",
		"<body>p\n<pre>a\n\n\nb</pre>q\n\nr</body>",
	} {
		md1, h1, md2, h2 := roundtrip(h)
		if h1 != h || h2 != h1 {
			t.Errorf("in %q\nmd1 %q\nh1  %q\nmd2 %q\nh2  %q", h, md1, h1, md2, h2)
		}
	}
	checkStable(t, []stableCase{
		{"```\n\nx\n```", "<body><pre>\nx</pre></body>"},
		{"```\na\n\n\n\nb\n```", "<body><pre>a\n\n\n\nb</pre></body>"},
		{"> ```\n> a\n>\n>\n> b\n> ```", "<body><blockquote><pre>a\n\n\nb</pre></blockquote></body>"},
	})
	// blank lines outside code are still one blank line, as before
	if md := ToEditableMarkdown("<body>a\n\n\n\nb<pre>c</pre></body>"); md != "a\n\nb\n\n```\nc\n```" {
		t.Errorf("md = %q", md)
	}
}

// #65: a code block's language is sent as <pre data-language="...">, which Asana documents
// for code blocks (https://developers.asana.com/docs/rich-text: "You can specify a language
// with data-language"), and comes back as the fence's info string. Only a plain name is
// kept (letters, digits and "+#.-_"): anything else could not be written back as a fence.
func TestCodeBlockLanguage(t *testing.T) {
	checkStable(t, []stableCase{
		{"```go\nx\n```", `<body><pre data-language="go">x</pre></body>`},
		{"``` js\nx\n```", `<body><pre data-language="js">x</pre></body>`},
		{"```c++ linenums\nx\n```", `<body><pre data-language="c++">x</pre></body>`},
		{"~~~python\nx\n~~~", `<body><pre data-language="python">x</pre></body>`},
		{"> ```go\n> x\n> ```", `<body><blockquote><pre data-language="go">x</pre></blockquote></body>`},
		{"```a&b\nx\n```", "<body><pre>x</pre></body>"},
		{"```{go}\nx\n```", "<body><pre>x</pre></body>"},
		// in a list item code is text (#60): no <pre>, no language
		{"- a\n  ```go\n  x\n  ```", "<body><ul><li>a\n<code>x</code></li></ul></body>"},
	})
	for _, c := range []struct{ in, md string }{
		{`<body><pre data-language="go">x</pre></body>`, "```go\nx\n```"},
		{`<body><pre data-language="objective-c">x</pre></body>`, "```objective-c\nx\n```"},
		{"<body><pre data-language=\"a`b\">x</pre></body>", "```\nx\n```"},
		{`<body><pre data-language="a b">x</pre></body>`, "```\nx\n```"},
		{`<body><pre data-language="go&#10;# h">x</pre></body>`, "```\nx\n```"},
	} {
		if md := ToEditableMarkdown(c.in); md != c.md {
			t.Errorf("ToEditableMarkdown(%q) = %q, want %q", c.in, md, c.md)
		}
	}
	for _, tg := range []Target{Comment, StatusUpdate} {
		if h := tg.FromMarkdown("```go\nx\n```"); h != `<body><pre data-language="go">x</pre></body>` {
			t.Errorf("%v: %q", tg, h)
		}
	}
	if p := PlainFromMarkdown("```go\nx\n```"); p != "x" {
		t.Errorf("plain = %q", p)
	}
}

// #64: a heading keeps its marks, links, mentions, code and images through an edit, and an
// empty heading stays an empty heading (it was the text "# "). Line breaks in a heading are
// spaces: a heading is one line in Markdown.
func TestHeadingKeepsInlines(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"<body><h1><strong>b</strong> x</h1></body>", ""},
		{`<body><h2>a <a data-asana-gid="9" data-asana-type="user">@A</a> <em>e</em> <s>s</s> <code>c</code> <a href="http://x.jp">l</a></h2></body>`,
			`<body><h2>a <a data-asana-gid="9"/> <em>e</em> <s>s</s> <code>c</code> <a href="http://x.jp">l</a></h2></body>`},
		{`<body><h1><img data-asana-gid="5"/> w</h1></body>`, ""},
		{"<body><blockquote><h1><strong>b</strong> <em>e</em></h1></blockquote></body>", ""},
		{"<body><h1></h1></body>", ""},
		{"<body><h2></h2></body>", ""},
		{"<body><h1> </h1></body>", "<body><h1></h1></body>"},
		{"<body><h1><br></h1></body>", "<body><h1></h1></body>"},
		{"<body><h1><strong> </strong></h1></body>", "<body><h1></h1></body>"},
		{"<body>a<h1></h1>b</body>", "<body>a\n<h1></h1>b</body>"},
		{"<body><blockquote><h1></h1></blockquote></body>", ""},
		// "#"s at the end are text, not a closing sequence
		{"<body><h1>a #</h1></body>", ""},
		{"<body><h1>a ##</h1></body>", ""},
		{"<body><h1>#</h1></body>", ""},
		{"<body><h1>a#</h1></body>", ""},
		{"<body><h1><strong>a</strong> #</h1></body>", ""},
		{"<body><h1>a\nb</h1></body>", "<body><h1>a b</h1></body>"},
		{"<body><h1>a<br>b</h1></body>", "<body><h1>a b</h1></body>"},
		{"<body><h1><code>x\ny</code></h1></body>", "<body><h1><code>x</code> <code>y</code></h1></body>"},
		{"<body><h1>  a  </h1></body>", "<body><h1>a</h1></body>"},
		{"<body><h1>*a* [b] &lt;c&gt;</h1></body>", ""},
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
	// for display: marks stay, a mention is bold, an image is a block of its own after it
	if md := ToMarkdown(`<body><h1><strong>b</strong> <a data-asana-gid="9" data-asana-type="user">@A</a><img data-asana-gid="5"/></h1>x</body>`); md != "# **b** **@A**\n\n"+ImageToken("5")+"\n\nx" {
		t.Errorf("ToMarkdown = %q", md)
	}
	// a heading over lines (setext) is one line, so an edit gives it back unchanged
	checkStable(t, []stableCase{
		{"a\nb\n---", "<body><h2>a b</h2></body>"},
		{"**a**\n*b*\n===", "<body><h1><strong>a</strong> <em>b</em></h1></body>"},
		{"#", "<body><h1></h1></body>"},
		{"## ##", "<body><h2></h2></body>"},
		{"# a \\#", "<body><h1>a #</h1></body>"},
	})
	if p := PlainFromMarkdown("a\nb\n---\nc"); p != "a b\nc" {
		t.Errorf("plain = %q", p)
	}
}
