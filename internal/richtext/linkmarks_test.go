package richtext

import "testing"

// Emphasis and code in a link's text stay in the link: ToEditableMarkdown writes them in
// the link text, and FromMarkdown reads them back (#15).
func TestLinkTextKeepsMarks(t *testing.T) {
	cases := []struct{ in, md, out string }{
		{
			`<body>x<a href="https://e.com"> <strong>b</strong> </a>y</body>`,
			"x [**b**](https://e.com) y",
			`<body>x <a href="https://e.com"><strong>b</strong></a> y</body>`,
		},
		{
			`<body><a href="https://e.com">a <em>b</em> <code>c]d</code> <s>e</s></a></body>`,
			"[a *b* `c]d` ~~e~~](https://e.com)",
			`<body><a href="https://e.com">a <em>b</em> <code>c]d</code> <s>e</s></a></body>`,
		},
		{
			`<body><a href="https://e.com"><strong>a</strong>]b[</a></body>`,
			`[**a**\]b\[](https://e.com)`,
			`<body><a href="https://e.com"><strong>a</strong>]b[</a></body>`,
		},
		{
			`<body><h1><a href="https://e.com"><u><em>a</em></u> b</a></h1></body>`,
			"# [*a* b](https://e.com)",
			`<body><h1><a href="https://e.com"><em>a</em> b</a></h1></body>`,
		},
		{
			"<body><ul><li><a href=\"https://e.com\"><strong>a</strong>\n\nb</a></li></ul></body>",
			"- [**a**\n\\\nb](https://e.com)",
			"<body><ul><li><a href=\"https://e.com\"><strong>a</strong>\n\nb</a></li></ul></body>",
		},
		// a link of plain text is written as before
		{
			`<body><a href="https://e.com">a*b</a></body>`,
			`[a\*b](https://e.com)`,
			`<body><a href="https://e.com">a*b</a></body>`,
		},
	}
	for _, tc := range cases {
		md1, h1, md2, h2 := rt2(tc.in)
		if md1 != tc.md || h1 != tc.out || md2 != md1 || h2 != h1 {
			t.Errorf("in  %q\nmd1 %q (want %q)\nh1  %q (want %q)\nmd2 %q\nh2  %q", tc.in, md1, tc.md, h1, tc.out, md2, h2)
		}
	}
}

// A blank line in a link's text is written as a line of a backslash, as in a list item: a
// blank line would end the paragraph and leave "[" and "](…)" as text (#22).
func TestLinkTextBlankLine(t *testing.T) {
	cases := []struct{ in, md, out string }{ // out "" is in
		{"<body><a href=\"https://e.com\">a\n\nb</a></body>", "[a\n\\\nb](https://e.com)", ""},
		{"<body><a href=\"https://e.com\"><strong>a</strong>\n\nb</a></body>", "[**a**\n\\\nb](https://e.com)", ""},
		{"<body><a href=\"https://e.com\">a\n\n\nb</a></body>", "[a\n\\\n\\\nb](https://e.com)", ""},
		{"<body>x <a href=\"https://e.com\">a\n\nb</a> y</body>", "x [a\n\\\nb](https://e.com) y", ""},
		{"<body><blockquote><a href=\"https://e.com\">a\n\nb</a></blockquote></body>", "> [a\n> \\\n> b](https://e.com)", ""},
		{"<body><ul><li><a href=\"https://e.com\">a\n\nb</a></li></ul></body>", "- [a\n\\\nb](https://e.com)", ""},
		{"<body><a href=\"https://e.com\">a\n\n# b</a></body>", "[a\n\\\n\\# b](https://e.com)", ""},
		// spaces on the blank line go, as in a list item
		{"<body><a href=\"https://e.com\">a\n \t\nb</a></body>", "[a  \n\\\nb](https://e.com)", "<body><a href=\"https://e.com\">a\n\nb</a></body>"},
		// one line break: as before
		{"<body><a href=\"https://e.com\">a\nb</a></body>", "[a\nb](https://e.com)", ""},
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
