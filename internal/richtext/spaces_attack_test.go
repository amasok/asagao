package richtext

import (
	"strings"
	"testing"
)

// #48/#49: trailing spaces at block ends and around emphasis survive a round trip
// and the second round trip changes nothing.
func TestSpacesSurviveAndSettle(t *testing.T) {
	cases := []struct{ name, in, want string }{ // want: substring of h1 (HTML after one trip)
		{"li", "<ul><li>a  </li><li>b</li></ul>", "<li>a  </li>"},
		{"li tab", "<ul><li>a\t</li><li>b</li></ul>", "<li>a\t</li>"},
		{"before nested list", "<ul><li>a  <ul><li>b</li></ul></li></ul>", "a  <ul>"},
		{"quote", "<blockquote>a  </blockquote>b", "<blockquote>a  </blockquote>"},
		{"quote last", "<blockquote>a </blockquote>", "<blockquote>a </blockquote>"},
		{"doc end", "a  ", "a  "},
		{"strong trailing", "<strong>a </strong>b", "<strong>a</strong> b"},
		{"strong leading", "x<strong> a</strong>", "x <strong>a</strong>"},
		{"strong both", "x<strong> a </strong>y", "x <strong>a</strong> y"},
		{"em newline", "<em>a \n</em>b", "<em>a</em>"},
		{"del", "<del>a </del>b", "<s>a</s>"},
		{"nested marks", "<strong><em>a </em></strong>b", "a"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := "<body>" + c.in + "</body>"
			md1, h1, md2, h2 := roundtrip(in)
			if !strings.Contains(h1, c.want) {
				t.Errorf("h1 lacks %q\nmd1 %q\nh1  %q", c.want, md1, h1)
			}
			if h1 != h2 {
				t.Errorf("not stable\nmd1 %q\nh1  %q\nmd2 %q\nh2  %q", md1, h1, md2, h2)
			}
			if strings.Contains(h1, "**") && !strings.Contains(c.in, "**") {
				t.Errorf("mark leaked as text: %q", h1)
			}
		})
	}
}

// Whitespace-only emphasis must not turn into stray marks.
func TestEmptyEmphasisLeavesNoMarks(t *testing.T) {
	for _, in := range []string{"a<strong> </strong>b", "a<em>\n</em>b", "a<strong><br></strong>b", "a<del>\t</del>b", "<strong></strong>", "<strong> <em> </em> </strong>"} {
		md1, h1, _, h2 := roundtrip("<body>" + in + "</body>")
		if strings.ContainsAny(h1, "*~") || h1 != h2 {
			t.Errorf("%q: md1 %q h1 %q h2 %q", in, md1, h1, h2)
		}
	}
}
