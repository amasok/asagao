package richtext

import "testing"

// The lines of an indented paragraph that a setext underline makes a heading lose their
// indentation, as Markdown drops it from the heading's text: the editor writes the heading
// without it, so the round trip is stable after the first send (#12). The indentation of
// other paragraphs stays text.
func TestIndentedSetextHeading(t *testing.T) {
	for md, want := range map[string]string{
		"   a\n  ---":               "<body><h2>a</h2></body>",
		"   a\n-":                   "<body><h2>a</h2></body>",
		" a\n\tb\n===":              "<body><h1>a b</h1></body>",
		"> \ta\n>  b\n> ===":        "<body><blockquote><h1>a b</h1></blockquote></body>",
		"   a@x.jp\n---":            `<body><h2><a href="mailto:a@x.jp">a@x.jp</a></h2></body>`,
		"\t[l](https://e.com)\n---": `<body><h2><a href="https://e.com">l</a></h2></body>`,
		// 4 spaces are not code here: the line is the heading's
		"    a\n---": "<body><h2>a</h2></body>",
		// a lazy line of the quote's paragraph, then the quote's underline
		"> a\n   b\n> ---": "<body><blockquote><h2>a b</h2></blockquote></body>",
		// an item that cannot start a list after a paragraph line goes on with it
		"  a\n2)\n  ---": "<body><h2>a 2)</h2></body>",
		"  a\n2. x\n---": "<body><h2>a 2. x</h2></body>",
		// no underline of this paragraph: the indentation is text
		"  a\nb":          "<body>  a\nb</body>",
		"  a\n1. x\n---":  "<body>  a\n<ol><li>x</li></ol><hr/></body>",
		"  a\n- - -":      "<body>  a\n<hr/></body>",
		"> a\n>   b\n---": "<body><blockquote>a\n  b</blockquote><hr/></body>",
		"  a\n\n---":      "<body>  a\n<hr/></body>",
	} {
		h1 := FromMarkdown(md)
		md2 := ToEditableMarkdown(h1)
		if h2 := FromMarkdown(md2); h1 != want || h2 != h1 {
			t.Errorf("md  %q\nh1  %q (want %q)\nmd2 %q\nh2  %q", md, h1, want, md2, h2)
		}
	}
}
