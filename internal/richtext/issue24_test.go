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
