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
	for in, md := range map[string]string{
		"<body><ul><li>x</li></ul>  y</body>":                       "- x\n\n&#32; y",
		"<body><ul><li>x</li></ul>\t\ty\n  z</body>":                "- x\n\n&#9;\ty  \n  z",
		"<body><ul><li>x</li></ul>  <strong>y</strong></body>":      "- x\n\n&#32; **y**",
		"<body><ol><li>x<ul><li>y</li></ul></li></ol>     z</body>": "1. x\n   - y\n\n&#32;    z",
		// not right after a list: as it is
		"<body><ul><li>x</li></ul>y\n\n  z</body>": "- x\n\ny\n\n  z",
	} {
		md1, h1, _, h2 := rt2(in)
		if md1 != md || h1 != in || h2 != in {
			t.Errorf("in  %q\nmd1 %q (want %q)\nh1  %q\nh2  %q", in, md1, md, h1, h2)
		}
	}
}
