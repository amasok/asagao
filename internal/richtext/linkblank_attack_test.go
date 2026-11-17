package richtext

import (
	"strings"
	"testing"
)

// #22 attack: blank lines in link text, in every container. After one round trip the HTML
// must settle and the text must stay inside one link.
func TestLinkBlankLineAttack(t *testing.T) {
	inners := []string{
		"a\n\nb", "a\n\n\nb", "a\n\n\n\n\nb", "a\n \nb", "a\n\t\nb", "a\n\n", "\n\na", "a\n\n# b", "a\n\n- b", "a\n\n> b",
		"a\n\n    b", "a\n\n\tb", "a\n\n1. b", "a\n\n---", "a\n\n```", "a\n\n|b|", "a\n\n[b]", "a\n\n<b>", "a\n\n\\",
		"a\n\n**b**", "<em>a\n\nb</em>", "<strong>a</strong>\n\n<code>b</code>", "a\n\n<br>b", "a<br>\n\nb", "a\n\n<img data-asana-gid=\"1\">",
		"a\n\n" + strings.Repeat("b\n\n", 30),
	}
	wraps := []string{
		"%s", "<blockquote>%s</blockquote>", "<h1>%s</h1>", "<ul><li>%s</li></ul>", "<ol><li>x<ul><li>%s</li></ul></li></ol>",
		"<blockquote><ul><li>%s</li></ul></blockquote>", "<ul><li><blockquote>%s</blockquote></li></ul>", "<p>%s</p>", "x %s y",
	}
	for _, w := range wraps {
		for _, in := range inners {
			h := "<body>" + strings.Replace(w, "%s", `<a href="https://e.com">`+in+`</a>`, 1) + "</body>"
			md1, h1, md2, h2 := rt2(h)
			if h1 != h2 {
				t.Errorf("not settled: in %q\nmd1 %q\nh1  %q\nmd2 %q\nh2  %q", h, md1, h1, md2, h2)
			}
			if n := strings.Count(h1, "<a "); n > 1 {
				t.Errorf("link split: in %q\nmd1 %q\nh1 %q", h, md1, h1)
			}
		}
	}
	// the display form must not panic or leave a broken link
	for _, in := range inners {
		_ = ToMarkdown(`<body><a href="https://e.com">` + in + `</a></body>`)
	}
}

// Multi-line link text without a blank line (ef131d3): trailing spaces and tabs.
func TestLinkMultiLineAttack(t *testing.T) {
	inners := []string{"a\nb", "a \nb", "a\t\nb", "a  \nb", "a\n b", "a\n\tb", "a\\\nb", "a\n# b", "a\n- b", "a\n> b", "a\n1. b", "a \n", "a\n b \nc"}
	for _, w := range []string{"%s", "<ul><li>%s</li></ul>", "<blockquote>%s</blockquote>", "<h2>%s</h2>"} {
		for _, in := range inners {
			h := "<body>" + strings.Replace(w, "%s", `<a href="https://e.com">`+in+`</a>`, 1) + "</body>"
			md1, h1, md2, h2 := rt2(h)
			if h1 != h2 || strings.Count(h1, "<a ") != 1 {
				t.Errorf("in %q\nmd1 %q\nh1 %q\nmd2 %q\nh2 %q", h, md1, h1, md2, h2)
			}
		}
	}
}
