package richtext

import "testing"

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
