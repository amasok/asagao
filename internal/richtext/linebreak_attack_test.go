package richtext

import (
	"fmt"
	"testing"
)

// #14 attack: inline elements that start with a line break, nested, in lists, quotes,
// headings; the round trip must settle after one pass.
func TestLineBreakInlineAttackSettles(t *testing.T) {
	wraps := []string{
		"<ul><li>a\n\n\t%s</li></ul>",
		"<ul><li>a \t%s</li></ul>",
		"<ol><li>1.%s</li></ol>",
		"<ol><li>x\n\n%s</li><li>y</li></ol>",
		"<blockquote>a \n%s</blockquote>",
		"<ul><li><blockquote>a\n\n %s</blockquote></li></ul>",
		"<h2>a %s</h2>",
		"%s",
		"a  %s",
	}
	inners := []string{
		"<u><br>*</u>",
		"<u><br>!</u>",
		"<span><u><br>x</u></span>",
		"<font><span><br>- a</span></font>",
		"<u><strong><br>b</strong></u>",
		"<u><br></u>",
		"<u></u><u><br>#</u>",
		"<u>\n#</u>",
		"<u> <br>#</u>",
		"<u><img data-asana-gid=\"1\"><br>x</u>",
		"<u><a href=\"https://e.com\"><br>x</a></u>",
		"<u><code><br>x</code></u>",
		"<em><br>1.</em>",
		"<u><br><br>> q</u>",
		"<u><ul><li>q</li></ul></u>",
		"<u><span><br></span>#</u>",
		"<span><u>!</u></span>",
		"<span><u> x</u></span>",
		"<span><u></u><u>x</u></span>",
		"<span><u><br></u>!</span>",
		"<span><u></u><u><br>*</u></span>",
		"<u><br>" + "<u>" + "<br>" + "</u>" + "x</u>",
	}
	for _, w := range wraps {
		for _, in := range inners {
			h := "<body>" + fmt.Sprintf(w, in) + "</body>"
			md1, h1, md2, h2 := rt2(h)
			if h1 != h2 {
				t.Errorf("not settled: in %q\nmd1 %q\nh1  %q\nmd2 %q\nh2  %q", h, md1, h1, md2, h2)
			}
		}
	}
}
