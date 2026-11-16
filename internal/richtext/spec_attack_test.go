package richtext

import (
	"fmt"
	"math/rand"
	"regexp"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// Asana's rich text spec: lists and list items cannot be nested inside headers or
// quotation blocks (#60). specViolations returns what a FromMarkdown result breaks.
func specViolations(out string) []string {
	var v []string
	var stack []string
	z := html.NewTokenizer(strings.NewReader(out))
	in := func(names ...string) string {
		for _, s := range stack {
			for _, n := range names {
				if s == n {
					return s
				}
			}
		}
		return ""
	}
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}
		switch tt {
		case html.StartTagToken:
			name, _ := z.TagName()
			n := string(name)
			switch n {
			case "ul", "ol", "li":
				if p := in("h1", "h2", "blockquote"); p != "" {
					v = append(v, fmt.Sprintf("<%s> inside <%s>", n, p))
				}
			}
			stack = append(stack, n)
		case html.EndTagToken:
			name, _ := z.TagName()
			n := string(name)
			if len(stack) == 0 || stack[len(stack)-1] != n {
				v = append(v, "unbalanced </"+n+">")
				continue
			}
			stack = stack[:len(stack)-1]
		}
	}
	if len(stack) > 0 {
		v = append(v, "unclosed "+strings.Join(stack, ","))
	}
	return v
}

var wordRe = regexp.MustCompile(`w\d+w`)

// every marker word in the input comes out exactly once, in order
func checkWords(t *testing.T, in, out string) {
	t.Helper()
	want := wordRe.FindAllString(in, -1)
	got := wordRe.FindAllString(out, -1)
	if strings.Join(want, ",") != strings.Join(got, ",") {
		t.Errorf("words changed\n in: %q\nout: %q\nwant %v got %v", in, out, want, got)
	}
}

var specInputs = []string{
	"> - w1w\n> - w2w",
	"> 1. w1w\n> 2. w2w",
	"> text w1w\n> - w2w\n> more w3w",
	"> - w1w\n>   - w2w\n> - w3w",
	"> > - w1w",
	"> > > w1w\n> > > - w2w\n> > > 1. w3w",
	"# w1w\n- w2w",
	"# - w1w",
	"## 1. w1w",
	"> # w1w\n> - w2w",
	"> ```\n> w1w\n> ```\n> - w2w",
	"> -\n> - w1w",
	">\n> - w1w\n>\n> - w2w",
	"- > w1w\n- > w2w",
	"- > - w1w",
	"- w1w\n  > w2w\n  - w3w",
	"1. w1w\n   > quote w2w\n   > - w3w",
	"- \n- w1w",
	"> - \n> - ",
	"> - [ ] w1w",
	"> - **w1w** and *w2w*\n> - `w3w`",
	"> - [w1w](https://example.com)",
	"> - w1w\n\n> - w2w",
	"> - w1w\n> \n> w2w\n> \n> - w3w",
	"para w1w\n> - w2w\npara w3w",
	"```\n- w1w\n> w2w\n```",
	"> ```\n> - w1w\n> ```",
	"    - w1w\n    > w2w",
	"> 1. w1w\n> 1. w2w\n>    - w3w",
	"> - w1w\n> - w2w\n> ---\n> - w3w",
	"> - 日本語w1w\n> - 🙂w2w",
	"> - <b>w1w</b> & w2w <i>",
	"> - w1w\r\n> - w2w\r\n",
}

func TestSpecNoListInQuoteOrHeading(t *testing.T) {
	for _, in := range specInputs {
		out := FromMarkdown(in)
		if v := specViolations(out); len(v) > 0 {
			t.Errorf("in: %q\nout: %q\nviolations: %v", in, out, v)
		}
		checkWords(t, in, out)
	}
}

// The output, turned back into Markdown and converted again, is a fixed point and is
// still within the spec.
func TestSpecRoundTripStable(t *testing.T) {
	for _, in := range specInputs {
		h1 := FromMarkdown(in)
		md1 := ToMarkdown(h1)
		h2 := FromMarkdown(md1)
		md2 := ToMarkdown(h2)
		if v := specViolations(h2); len(v) > 0 {
			t.Errorf("2nd pass violates spec: %v\nin: %q\nmd1: %q\nh2: %q", v, in, md1, h2)
		}
		if md1 != md2 {
			t.Errorf("unstable\nin: %q\nh1: %q\nmd1: %q\nh2: %q\nmd2: %q", in, h1, md1, h2, md2)
		}
		checkWords(t, in, h2)
	}
}

func genSpecBlock(r *rand.Rand, d int, n *int) []string {
	var lines []string
	for i := 1 + r.Intn(3); i > 0; i-- {
		*n++
		w := fmt.Sprintf("w%dw", *n)
		switch k := r.Intn(8); {
		case k == 0:
			lines = append(lines, w)
		case k == 1:
			lines = append(lines, "- "+w)
		case k == 2:
			lines = append(lines, "1. "+w)
		case k == 3:
			lines = append(lines, "# "+w)
		case k == 4 && d < 3:
			for _, l := range genSpecBlock(r, d+1, n) {
				lines = append(lines, "> "+l)
			}
		case k == 5 && d < 3:
			lines = append(lines, "- "+w)
			for _, l := range genSpecBlock(r, d+1, n) {
				lines = append(lines, "  "+l)
			}
		case k == 6:
			lines = append(lines, "")
		default:
			lines = append(lines, "- ", "> "+w)
		}
	}
	return lines
}

func TestSpecRandom(t *testing.T) {
	r := rand.New(rand.NewSource(60))
	for i := 0; i < 3000; i++ {
		n := 0
		in := strings.Join(genSpecBlock(r, 0, &n), "\n")
		out := FromMarkdown(in)
		if v := specViolations(out); len(v) > 0 {
			t.Fatalf("in: %q\nout: %q\nviolations: %v", in, out, v)
		}
		checkWords(t, in, out)
		h2 := FromMarkdown(ToMarkdown(out))
		if v := specViolations(h2); len(v) > 0 {
			t.Fatalf("2nd pass: in %q\nout %q\nh2 %q\n%v", in, out, h2, v)
		}
		checkWords(t, in, h2)
	}
}

// Content that did not break the spec before must not change.
func TestSpecPlainListAndQuoteUnchanged(t *testing.T) {
	for in, want := range map[string]string{
		"- a\n- b":   "<body><ul><li>a</li><li>b</li></ul></body>",
		"> a":        "<body><blockquote>a</blockquote></body>",
		"1. a\n2. b": "<body><ol><li>a</li><li>b</li></ol></body>",
		"# t":        "<body><h1>t</h1></body>",
	} {
		if got := FromMarkdown(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func nonSpace(s string) string {
	return strings.Join(strings.Fields(s), "")
}

// PlainFromMarkdown (Asana's plain notes) carries the same characters as the rich text.
func TestSpecPlainMatchesHTML(t *testing.T) {
	r := rand.New(rand.NewSource(61))
	ins := append([]string{}, specInputs...)
	for i := 0; i < 1000; i++ {
		n := 0
		ins = append(ins, strings.Join(genSpecBlock(r, 0, &n), "\n"))
	}
	for _, in := range ins {
		h := nodeTextOf(FromMarkdown(in))
		p := PlainFromMarkdown(in)
		if nonSpace(h) != nonSpace(p) {
			t.Fatalf("in: %q\nhtml text: %q\nplain: %q", in, h, p)
		}
	}
}

// Code and quotes with list-looking lines keep every character.
func TestSpecCodeInQuoteKeepsChars(t *testing.T) {
	for _, in := range []string{
		"> ```\n> - w1w\n> 1. w2w\n> ```",
		"- w1w\n  ```\n  - w2w\n  > w3w\n  ```",
		"> - w1w\n> ```go\n> w2w <b> &amp;\n> ```",
		"1. w1w\n   > # w2w",
		"- # w1w\n- > w2w",
	} {
		out := FromMarkdown(in)
		if v := specViolations(out); len(v) > 0 {
			t.Errorf("%q -> %q: %v", in, out, v)
		}
		checkWords(t, in, out)
		if !strings.Contains(nodeTextOf(out), "w") {
			t.Errorf("lost text: %q", out)
		}
	}
}
