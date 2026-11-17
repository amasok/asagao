package richtext

import "testing"

// Text after a list that starts with line breaks, indented: it must not be taken into
// the last item on the way back.
func TestAfterListIndentedTextAttack(t *testing.T) {
	ls := []string{"<ul><li>x</li></ul>", "<ol><li>x<ul><li>y</li></ul></li></ol>", "<blockquote><ul><li>x</li></ul>%s</blockquote>", "<ul><li>x</li></ul><ul><li>z</li></ul>"}
	afters := []string{
		"\n\n  y", "\n  y", "<br>  y", "  <br>  y", "\n\n\ty", "<br>\n  y", "\n\n  <em>y</em>", "\n\n  <code>y</code>",
		"\n\n  <img data-asana-gid=\"1\">", "\n\n   - y", "\n\n    y", "<br><br>  y", "\n \n  y", "\n\n  ", "<p>  y</p>",
	}
	for _, l := range ls {
		for _, a := range afters {
			h := "<body>" + l + a + "</body>"
			if l == ls[2] {
				h = "<body><blockquote><ul><li>x</li></ul>" + a + "</blockquote></body>"
			}
			md1, h1, md2, h2 := rt2(h)
			if h1 != h2 {
				t.Errorf("in %q\nmd1 %q\nh1 %q\nmd2 %q\nh2 %q", h, md1, h1, md2, h2)
			}
		}
	}
	for _, src := range []string{"-\n\n  c", "-\n  c", "- \n\n  c\n\n  d", "1.\n\n   c", "- a\n\n-\n\n  c"} {
		h1 := FromMarkdown(src)
		if h2 := FromMarkdown(ToEditableMarkdown(h1)); h1 != h2 {
			t.Errorf("src %q: %q vs %q", src, h1, h2)
		}
	}
}
