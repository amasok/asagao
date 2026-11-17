package richtext

import "testing"

// Whitespace in a list item right before an inline element that starts with a line break
// (<u><br>x</u>) is at the end of a line, not before content on it: the round trip is
// stable after one pass (#14).
func TestSpaceBeforeInlineStartingWithBreak(t *testing.T) {
	cases := []string{
		"<body><ul><li>a\n\n\t<u><br>*</u></li></ul></body>",
		"<body><ul><li>a\n\n\t<u><em><br>*</em></u></li></ul></body>",
		"<body><ul><li>a\n\n <span><br>b</span></li></ul></body>",
		"<body><ul><li>a\n\n\t<u>\nx</u></li></ul></body>",
		// content before the line break in the element: the whitespace is kept before it
		"<body><ul><li>a\n\n\t<u>x<br>y</u></li></ul></body>",
		"<body><ul><li>a\n\n\t<u><img data-asana-gid=\"7\" src=\"x\"><br>x</u></li></ul></body>",
		"<body><ul><li>a\n\n\t<span><a data-asana-gid=\"1\"></a><br>x</span></li></ul></body>",
	}
	for _, in := range cases {
		md1, h1, md2, h2 := rt2(in)
		if h1 != h2 {
			t.Errorf("not stable\nin  %q\nmd1 %q\nh1  %q\nmd2 %q\nh2  %q", in, md1, h1, md2, h2)
		}
	}
	// a line in a list item that a line break in an element follows is not a list marker
	in := "<body><ul><li>a\n1.<u><br>!</u></li></ul></body>"
	if md1, h1, md2, h2 := rt2(in); h1 != "<body><ul><li>a\n1.\n!</li></ul></body>" || h2 != h1 {
		t.Errorf("in  %q\nmd1 %q\nh1  %q\nmd2 %q\nh2  %q", in, md1, h1, md2, h2)
	}
	// the whitespace before content in the element stays
	if _, h1, _, _ := rt2(cases[4]); h1 != "<body><ul><li>a\n\n\tx\ny</li></ul></body>" {
		t.Errorf("h1 %q", h1)
	}
}

// An image that writes nothing (no gid, no alt text) is not content on the line: the
// whitespace before it in a list item is not kept for it (#14).
func TestSpaceBeforeEmptyImage(t *testing.T) {
	for _, in := range []string{
		"<body><ul><li>a\n\n\t<u><img src=\"x\"><br>x</u></li></ul></body>",
		"<body><ul><li>a\n\n\t<img src=\"x\"><br>x</li></ul></body>",
		"<body><ul><li>a\n\n\t<img alt=\"p\"><br>x</li></ul></body>",
		"<body><ul><li>a\n\n\t<u><img alt=\"p\"><br>x</u></li></ul></body>",
		"<body><ul><li>a \t<img src=\"x\"><br>x</li></ul></body>",
	} {
		md1, h1, md2, h2 := rt2(in)
		if h1 != h2 {
			t.Errorf("not stable\nin  %q\nmd1 %q\nh1  %q\nmd2 %q\nh2  %q", in, md1, h1, md2, h2)
		}
	}
}
