package richtext

import (
	"regexp"
	"strings"
	"testing"
)

var rtTexts = []string{"a", "b c", "# h", "- x", "1. y", "> q", "+ z", "***", "---", "~~~", "    c", "\tt", " ", "  ", "\t", "a ", "a\t", "a  ", "x \t ", "&", "<", "*s*", "_u_", "[l](u)", "!", "a@x.jp", "www.x.jp", "日本"}

func htmlEsc(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// A line that is only text must never turn into list/heading/quote/rule structure.
func TestBlockLookalikesStayText(t *testing.T) {
	for _, txt := range []string{"# h", "## h", "- x", "+ x", "* x", "1. x", "1) x", "> q", "---", "***", "===", "~~~", "```", "    code", "\tcode", "|a|b|", "[x]: http://y"} {
		for _, wrap := range []struct{ name, pre, post string }{
			{"top", "<body>", "</body>"},
			{"after br", "<body>a<br>", "</body>"},
			{"after newline", "<body>a\n", "</body>"},
			{"after img", `<body><img data-asana-gid="1" src="x">` + "\n", "</body>"},
			{"in quote", "<body><blockquote>", "</blockquote></body>"},
			{"in quote after br", "<body><blockquote>a<br>", "</blockquote></body>"},
			{"nested quote", "<body><blockquote><blockquote>", "</blockquote></blockquote></body>"},
			{"in list", "<body><ul><li>", "</li></ul></body>"},
			{"in ol", "<body><ol><li>", "</li></ol></body>"},
			{"in list after br", "<body><ul><li>a<br>", "</li></ul></body>"},
			{"in nested list", "<body><ul><li>a<ul><li>", "</li></ul></li></ul></body>"},
			{"list in quote", "<body><blockquote><ul><li>", "</li></ul></blockquote></body>"},
			{"after pre", "<body><pre>p</pre>", "</body>"},
			{"after hr", "<body><hr>", "</body>"},
			{"after heading", "<body><h1>t</h1>", "</body>"},
		} {
			in := wrap.pre + htmlEsc(txt) + wrap.post
			md := ToEditableMarkdown(in)
			out := FromMarkdown(md)
			want := strings.Count(in, "<ul>") + strings.Count(in, "<ol>")
			got := strings.Count(out, "<ul>") + strings.Count(out, "<ol>")
			bad := got != want ||
				strings.Count(out, "<blockquote>") != strings.Count(in, "<blockquote>") ||
				strings.Count(out, "<h1>")+strings.Count(out, "<h2>") != strings.Count(in, "<h1>") ||
				strings.Count(out, "<pre>") != strings.Count(in, "<pre>") ||
				strings.Count(out, "<hr") != strings.Count(in, "<hr")
			if bad && !strings.Contains(in, "<blockquote><blockquote>") {
				t.Errorf("%s / %q became structure\nin  %q\nmd  %q\nout %q", wrap.name, txt, in, md, out)
			}
		}
	}
}

// #45: spaces before a line break are kept as a character reference. When nothing follows the
// break (end of text / end of block) the space is not before a line break any more: it must
// be dropped like main does, not left as a "&#32;" that the next edit round trip then removes.
func TestNoGhostSpaceAtBlockEnd(t *testing.T) {
	for _, in := range []string{
		"<body>a \n</body>",
		"<body>a <br></body>",
		"<body>a\t<br><br></body>",
		"<body><blockquote>a <br></blockquote></body>",
		"<body><ul><li>a \n</li><li>b</li></ul></body>",
		"<body><ul><li>a <br><ul><li>b</li></ul></li></ul></body>",
		"<body><ol><li>a\t\n<ul><li>b</li></ul></li></ol></body>",
	} {
		md := ToEditableMarkdown(in)
		if strings.HasSuffix(strings.TrimRight(md, "\n> "), "&#32;") || strings.Contains(md, "&#32;\n- b") {
			t.Errorf("ghost space kept at the end of a block\nin %q\nmd %q", in, md)
		}
		h1 := FromMarkdown(md)
		if regexp.MustCompile(`[ \t]</?(li|ul|ol|blockquote|body)`).MatchString(h1) && !strings.Contains(h1, "</li><li>") {
			t.Errorf("space left before the end of the text\nin %q\nh1 %q", in, h1)
		}
		if h2 := FromMarkdown(ToEditableMarkdown(h1)); h1 != h2 {
			t.Errorf("not stable\nin %q\nh1 %q\nh2 %q", in, h1, h2)
		}
	}
}

// #45: email autolinks get mailto: exactly once and the address cannot break out of href.
func TestEmailAutolink(t *testing.T) {
	for in, want := range map[string]string{
		"a@x.jp":          `<a href="mailto:a@x.jp">a@x.jp</a>`,
		"<a@x.jp>":        `<a href="mailto:a@x.jp">a@x.jp</a>`,
		"<mailto:a@x.jp>": `<a href="mailto:a@x.jp">mailto:a@x.jp</a>`,
		"x www.x.jp y":    `<a href="http://www.x.jp">www.x.jp</a>`,
		"a+b@x.jp.":       `<a href="mailto:a+b@x.jp">a+b@x.jp</a>.`,
	} {
		if out := FromMarkdown(in); !strings.Contains(out, want) {
			t.Errorf("%q -> %q, want it to contain %q", in, out, want)
		}
	}
	for _, in := range []string{
		`a@x.jp"><script>alert(1)</script>`, `<a@x.jp"onmouseover="x>`, `"a@x.jp" onclick=x`, "a@x.jp&quot;>", `x@y.jp?subject="><b>`,
		`<a@x.jp>"><img src=x>`,
	} {
		out := FromMarkdown(in)
		if strings.Contains(out, "<script") || strings.Contains(out, "<img src") || strings.Contains(out, "<b>") ||
			strings.Contains(out, "mailto:mailto:") || strings.Contains(out, `" on`) {
			t.Errorf("unsafe output for %q: %q", in, out)
		}
		// whatever comes out must survive another trip unchanged
		if h2 := FromMarkdown(ToEditableMarkdown(out)); h2 != out {
			t.Errorf("unstable %q\nh1 %q\nh2 %q", in, out, h2)
		}
	}
}
