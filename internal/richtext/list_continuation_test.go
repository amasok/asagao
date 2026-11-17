package richtext

import "testing"

// After a blank line, a line indented as far as the text of an open list item goes on that
// item, as Markdown reads it (#13): the deepest item it reaches. A line that reaches none
// keeps its indentation as text.
func TestIndentedLineAfterBlankLineInItem(t *testing.T) {
	for md, want := range map[string]string{
		// text after a nested list goes into its last item (#59)
		"- a\n  - b\n\n  c": "<body><ul><li>a<ul><li>b\nc</li></ul></li></ul></body>",
		// and the list after it is a list next to it
		"- a\n  - b\n\n  c\n\n  - d": "<body><ul><li>a<ul><li>b\nc</li></ul><ul><li>d</li></ul></li></ul></body>",
		"- a\n\n  c":                 "<body><ul><li>a\nc</li></ul></body>",
		"- a\n\n\tc":                 "<body><ul><li>a\nc</li></ul></body>",
		"1. a\n\n   c":               "<body><ol><li>a\nc</li></ol></body>",
		"10. a\n\n    c":             "<body><ol><li>a\nc</li></ol></body>",
		"-   a\n\n    c":             "<body><ul><li>a\nc</li></ul></body>",
		"- a\n  - b\n\n    c":        "<body><ul><li>a<ul><li>b\nc</li></ul></li></ul></body>",
		"> - a\n>\n>   c":            "<body><blockquote>- a\n\nc</blockquote></body>",
		// short of the item's text: the item ends, the indentation is text
		"- a\n\n c":        "<body><ul><li>a</li></ul> c</body>",
		"1. a\n\n  c":      "<body><ol><li>a</li></ol>  c</body>",
		"-   a\n\n   c":    "<body><ul><li>a</li></ul>   c</body>",
		"- a\n  - b\n\n c": "<body><ul><li>a<ul><li>b</li></ul></li></ul> c</body>",
		// an item with nothing on its first line ends at a blank line
		"-\n\n  c":      "<body><ul><li></li></ul>  c</body>",
		"-\n  b\n\n  c": "<body><ul><li>b\nc</li></ul></body>",
		// a blank line with fewer ">" ends the quote, and the list in it
		"> > - a\n>\n> > \tc": "<body><blockquote><blockquote>- a</blockquote><blockquote>\tc</blockquote></blockquote></body>",
	} {
		h1 := FromMarkdown(md)
		md2 := ToEditableMarkdown(h1)
		if h2 := FromMarkdown(md2); h1 != want || h2 != h1 {
			t.Errorf("md  %q\nh1  %q (want %q)\nmd2 %q\nh2  %q", md, h1, want, md2, h2)
		}
	}
}

// The editor writes the indentation on the line right after a list as a character
// reference: written as it is, it would go on the list's last item (#13).
func TestIndentedTextAfterList(t *testing.T) {
	cases := []struct{ in, md, out string }{ // out "" is in
		{"<body><ul><li>x</li></ul>  y</body>", "- x\n\n&#32; y", ""},
		{"<body><ul><li>x</li></ul>\t\ty\n  z</body>", "- x\n\n&#9;\ty  \n  z", ""},
		{"<body><ul><li>x</li></ul>  <strong>y</strong></body>", "- x\n\n&#32; **y**", ""},
		{"<body><ol><li>x<ul><li>y</li></ul></li></ol>     z</body>", "1. x\n   - y\n\n&#32;    z", ""},
		// the first line with text after a list, after blank lines or line breaks (which go)
		{"<body><ul><li>x</li></ul>\n\n  y</body>", "- x\n\n&#32; y", "<body><ul><li>x</li></ul>  y</body>"},
		{"<body><ul><li>x</li></ul>\n  y</body>", "- x\n\n&#32; y", "<body><ul><li>x</li></ul>  y</body>"},
		{"<body><ul><li>x</li></ul><br>  y</body>", "- x\n\n&#32; y", "<body><ul><li>x</li></ul>  y</body>"},
		{"<body><ul><li>x</li></ul>  <br>  y</body>", "- x\n\n&#32; y", "<body><ul><li>x</li></ul>  y</body>"},
		{"<body><ul><li>x</li></ul>\n  <em>y</em></body>", "- x\n\n&#32; *y*", "<body><ul><li>x</li></ul>  <em>y</em></body>"},
		{"<body><ul><li>x</li></ul><p>  y</p></body>", "- x\n\n&#32; y", "<body><ul><li>x</li></ul>  y</body>"},
		// not right after a list: as it is
		{"<body><ul><li>x</li></ul>y\n\n  z</body>", "- x\n\ny\n\n  z", ""},
	}
	for _, tc := range cases {
		out := tc.out
		if out == "" {
			out = tc.in
		}
		md1, h1, _, h2 := rt2(tc.in)
		if md1 != tc.md || h1 != out || h2 != out {
			t.Errorf("in  %q\nmd1 %q (want %q)\nh1  %q (want %q)\nh2  %q", tc.in, md1, tc.md, h1, out, h2)
		}
	}
}
