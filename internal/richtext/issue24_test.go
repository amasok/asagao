package richtext

import "testing"

// rtSame checks that html in survives the editor's round trip as it is, twice.
func rtSame(t *testing.T, in string) {
	t.Helper()
	md1, h1, md2, h2 := rt2(in)
	if h1 != in || h2 != in || md2 != md1 {
		t.Errorf("in  %q\nmd1 %q\nh1  %q\nmd2 %q\nh2  %q", in, md1, h1, md2, h2)
	}
}

// A link whose text is its URL with emphasis or code in it keeps them: it is written as a
// link of its text, not as an autolink (#24, 1).
func TestStyledURLLink(t *testing.T) {
	for _, in := range []string{
		`<body><a href="https://e.com"><strong>https://e.com</strong></a></body>`,
		`<body><a href="https://e.com">https://<em>e</em>.com</a></body>`,
		`<body><a href="https://e.com"><code>https://e.com</code></a></body>`,
		`<body>x<a href="https://e.com"><s>https://e.com</s></a>y</body>`,
		`<body><a href="mailto:a@x.jp"><strong>mailto:a@x.jp</strong></a></body>`,
		// plain: an autolink, as before
		`<body><a href="https://e.com">https://e.com</a></body>`,
	} {
		rtSame(t, in)
	}
	if md := ToEditableMarkdown(`<body><a href="https://e.com">https://e.com</a></body>`); md != "<https://e.com>" {
		t.Errorf("plain URL link: %q", md)
	}
}

// Spaces at the ends of a heading's text, typed as character references, go when it is
// sent: the editor writes a heading without them, so they would go on the next round trip
// (#24, 2).
func TestHeadingEdgeSpacesGo(t *testing.T) {
	for md, want := range map[string]string{
		"## &#32;a":                  "<body><h2>a</h2></body>",
		"  &#32;a\n---":              "<body><h2>a</h2></body>",
		"# &#9; a &#32;":             "<body><h1>a</h1></body>",
		"# &#32;**a**":               "<body><h1><strong>a</strong></h1></body>",
		"## [@x](asana:user:9)&#32;": `<body><h2><a data-asana-gid="9"/></h2></body>`,
		"# a &#32; b":                "<body><h1>a   b</h1></body>", // (inside, they stay)
	} {
		h1 := FromMarkdown(md)
		if h2 := FromMarkdown(ToEditableMarkdown(h1)); h1 != want || h2 != h1 {
			t.Errorf("md %q\nh1 %q (want %q)\nh2 %q", md, h1, want, h2)
		}
	}
}

// A line break written as text after content of another element on the line (an image, a
// mention, emphasis), or before content in the next one, is a hard break as a <br> there
// is: the Markdown of the first and the second round trip is the same (#24, 5).
func TestLineBreakAfterElementSameMarkdown(t *testing.T) {
	for _, in := range []string{
		"<body><ul><li>a\n\n\t<u><img data-asana-gid=\"7\" src=\"x\"><br>x</u></li></ul></body>",
		"<body><ul><li>a\n\n\t<span><a data-asana-gid=\"1\"></a><br>x</span></li></ul></body>",
		"<body>x<br><em>y</em></body>",
		"<body><em>x</em><br>y</body>",
		"<body><strong>a</strong><br><strong>b</strong></body>",
		"<body><blockquote><em>x</em><br>y</blockquote></body>",
		"<body><ul><li><code>a</code><br><code>b</code></li></ul></body>",
	} {
		md1, h1, md2, h2 := rt2(in)
		if md1 != md2 || h1 != h2 {
			t.Errorf("in  %q\nmd1 %q\nh1  %q\nmd2 %q\nh2  %q", in, md1, h1, md2, h2)
		}
	}
}
