package richtext

import "testing"

// Spaces at the ends of a heading's text inside a link or emphasis there, typed as
// character references, go when it is sent, as those outside them do (#24): the editor
// moves them out of the link and writes the heading without them, so they would go on the
// next round trip (#26, 1).
func TestHeadingEdgeSpacesInLinkGo(t *testing.T) {
	for md, want := range map[string]string{
		"## [&#32;a](https://e.com)":              `<body><h2><a href="https://e.com">a</a></h2></body>`,
		"## [a&#32;](https://e.com)":              `<body><h2><a href="https://e.com">a</a></h2></body>`,
		"## [a&#32;](https://e.com)&#32;":         `<body><h2><a href="https://e.com">a</a></h2></body>`,
		"# &#32;[&#9;a](https://e.com) b":         `<body><h1><a href="https://e.com">a</a> b</h1></body>`,
		"# **&#32;a**":                            "<body><h1><strong>a</strong></h1></body>",
		"# *a&#32;*":                              "<body><h1><em>a</em></h1></body>",
		"# ~~&#32;a~~":                            "<body><h1><s>a</s></h1></body>",
		"# [**&#32;a**](https://e.com)":           `<body><h1><a href="https://e.com"><strong>a</strong></a></h1></body>`,
		"[&#32;a](https://e.com)\n---":            `<body><h2><a href="https://e.com">a</a></h2></body>`,
		"# `&#32;a`":                              "<body><h1><code>&amp;#32;a</code></h1></body>",
		"# [&#32;a](https://e.com) ![b](asana:1)": `<body><h1><a href="https://e.com">a</a> <img data-asana-gid="1"/></h1></body>`,
		// emphasis left empty goes, and the spaces around it then are at the edges
		"# *&#32;*":                "<body><h1></h1></body>",
		"# a *&#32;*":              "<body><h1>a</h1></body>",
		"# **&#32;*&#32;*** a":     "<body><h1>a</h1></body>",
		"# [&#32;](https://e.com)": `<body><h1><a href="https://e.com"></a></h1></body>`, // (a link stays)
	} {
		h1 := FromMarkdown(md)
		if h2 := FromMarkdown(ToEditableMarkdown(h1)); h1 != want || h2 != h1 {
			t.Errorf("md %q\nh1 %q (want %q)\nh2 %q", md, h1, want, h2)
		}
	}
}

// The plain text of a heading has no spaces at its ends either, as its HTML (#26, 2): the
// plain text sent first is the one sent after the next edit. Spaces next to an image stay,
// as they do in the HTML.
func TestPlainHeadingEdgeSpacesGo(t *testing.T) {
	for md, want := range map[string]string{
		"## &#32;a":                   "a",
		"# a&#32;":                    "a",
		"  &#32;a\n---":               "a",
		"# &#9; a &#32;":              "a",
		"# &#32;**a**":                "a",
		"## [&#32;a](https://e.com)":  "a",
		"# &#32;\n\nb":                "b",
		"# &#32;![x](asana:5) a":      " a",
		"# a ![x](asana:5)&#32;\n\nb": "a \nb",
		"# a &#32; b":                 "a   b",
	} {
		p1 := TaskNotes.PlainFromMarkdown(md)
		p2 := TaskNotes.PlainFromMarkdown(ToEditableMarkdown(TaskNotes.FromMarkdown(md)))
		if p1 != want || p2 != p1 {
			t.Errorf("md %q\np1 %q (want %q)\np2 %q", md, p1, want, p2)
		}
	}
}

// Back from a quote in a list item, an item line starts a list (the paragraph open is the
// quote's, which it may interrupt), as goldmark reads it: an empty item there ends the lists
// at the blank line after it, and the indentation of the line after them is text, as after
// any empty item (#24, 4; #26, 3).
func TestEmptyItemAfterQuoteInItem(t *testing.T) {
	for md, want := range map[string]string{
		"- a\n  > q\n  -\n\n  c":            "<body><ul><li>a\n&gt; q<ul><li></li></ul></li></ul>  c</body>",
		"- a\n  > q\n  1.\n\n  c":           "<body><ul><li>a\n&gt; q<ol><li></li></ol></li></ul>  c</body>",
		"- a\n  > q\n  2.\n\n  c":           "<body><ul><li>a\n&gt; q<ol><li></li></ol></li></ul>  c</body>",
		"- a\n  > q\n  *\n\n  c":            "<body><ul><li>a\n&gt; q<ul><li></li></ul></li></ul>  c</body>",
		"- a\n  > q\n  b\n  -\n\n  c":       "<body><ul><li>a\n&gt; q\n&gt; b<ul><li></li></ul></li></ul>  c</body>",
		"- a\n  > q\n  > r\n  -\n\n  c":     "<body><ul><li>a\n&gt; q\n&gt; r<ul><li></li></ul></li></ul>  c</body>",
		"- a\n  > q\n    -\n\n  c":          "<body><ul><li>a\n&gt; q<ul><li></li></ul></li></ul>  c</body>",
		"1. a\n   > q\n   1.\n\n   c":       "<body><ol><li>a\n&gt; q<ol><li></li></ol></li></ol>   c</body>",
		"- x\n  - a\n    > q\n    -\n\n  c": "<body><ul><li>x<ul><li>a\n&gt; q<ul><li></li></ul></li></ul></li></ul>  c</body>",
		// (as before: an item with text, and the next item further out)
		"- a\n  > q\n  - b\n\n  c": "<body><ul><li>a\n&gt; q<ul><li>b\nc</li></ul></li></ul></body>",
		"- a\n  > q\n -\n\n  c":    "<body><ul><li>a\n&gt; q</li><li></li></ul>  c</body>",
		// a quote up to 3 columns further in than the item's text is in it; 4 or more is
		// paragraph text, which an empty item there goes on (#24)
		"- a\n   > q\n  -\n\n  c":              "<body><ul><li>a\n&gt; q<ul><li></li></ul></li></ul>  c</body>",
		"- a\n      > q\n  -\n\n  c":           "<body><ul><li>a\n&gt; q\n-\nc</li></ul></body>",
		"- x\n  - a\n       > q\n    -\n\n  c": "<body><ul><li>x<ul><li>a\n&gt; q<ul><li></li></ul></li></ul></li></ul>  c</body>",
		// not back from a quote: an empty item in the item's paragraph is its text (#24)
		"- a\n  -\n\n  c": "<body><ul><li>a\n-\nc</li></ul></body>",
		// after a blank line, the quote's paragraph has ended (#24)
		"- a\n  > q\n\n  -\n\n  c": "<body><ul><li>a\n&gt; q<ul><li></li></ul></li></ul>  c</body>",
	} {
		h1 := FromMarkdown(md)
		md2 := ToEditableMarkdown(h1)
		if h2 := FromMarkdown(md2); h1 != want || h2 != h1 {
			t.Errorf("md  %q\nh1  %q (want %q)\nmd2 %q\nh2  %q", md, h1, want, md2, h2)
		}
	}
}
