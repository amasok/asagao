package richtext

import (
	"strings"
	"testing"
)

func stableHTML(t *testing.T, h string) {
	t.Helper()
	md1, h1, md2, h2 := rt2(h)
	if h1 != h2 {
		t.Errorf("not settled: in %q\nmd1 %q\nh1  %q\nmd2 %q\nh2  %q", h, md1, h1, md2, h2)
	}
}

func stableMD(t *testing.T, src string) {
	t.Helper()
	h1 := FromMarkdown(src)
	md := ToEditableMarkdown(h1)
	if h2 := FromMarkdown(md); h1 != h2 {
		t.Errorf("src %q\nh1 %q\nmd %q\nh2 %q", src, h1, md, h2)
	}
}

// (1) decorated link whose text equals its href
func TestIssue24StyledSelfLinkAttack(t *testing.T) {
	for _, in := range []string{
		`<a href="https://e.com"><strong>https://e.com</strong></a>`,
		`<a href="https://e.com"><em>https://e.com</em> x</a>`,
		`<a href="https://e.com"><code>https://e.com</code></a>`,
		`<a href="mailto:a@b.com"><strong>a@b.com</strong></a>`,
		`<a href="https://e.com/a_b*c"><s>https://e.com/a_b*c</s></a>`,
		`<a href="https://e.com/(x)"><strong>https://e.com/(x)</strong></a>`,
		`<a href="https://e.com"><strong>https://e.com</strong></a>.`,
		`<a href="https://e.com"><strong>https://e.com</strong></a>x`,
		`<a href="https://e.com">https://e.com</a>`,
		`<a href="https://e.com"><u>https://e.com</u></a>`,
	} {
		for _, w := range []string{"%s", "<ul><li>%s</li></ul>", "<h1>%s</h1>", "<blockquote>%s</blockquote>"} {
			stableHTML(t, "<body>"+strings.Replace(w, "%s", in, 1)+"</body>")
		}
	}
	_, h1, _, _ := rt2(`<body><a href="https://e.com"><strong>https://e.com</strong></a></body>`)
	if strings.Count(h1, "<a ") != 1 || !strings.Contains(h1, "<strong>") {
		t.Errorf("link or mark lost: %q", h1)
	}
}

// (2) heading edge spaces
func TestIssue24HeadingEdgeAttack(t *testing.T) {
	for _, src := range []string{
		"## &#32;a", "## a&#32;", "## &#32;&#32;a &#32;", "## &#9;a", "## &#32;**a**", "## &#32;*a*&#32;", "## &#32;![x](asana:1)",
		"## &#32;<!-- c -->a", "# &#32;", "## &#32;&#32;", "## &nbsp;a", "## a\n&#32;b\n---", "&#32;a\n---", "  &#32;a\n---",
		"## &#32;[a](https://e.com)", "## &#32;`a`", "## &#32;@x", "#\t&#32;a", "### &#32;a ###",
	} {
		stableMD(t, src)
	}
}

// (5) hard breaks
func TestIssue24HardBreakAttack(t *testing.T) {
	for _, in := range []string{
		"<em>x</em><br>y", "x<br><em>y</em>", "<em>x</em> <br>y", "<img data-asana-gid=\"1\"><br>y", "x<br><img data-asana-gid=\"1\">",
		"<code>x</code><br>y", "<a href=\"https://e.com\">x</a><br>y", "x \t<br>y", "x<br><br>y", "<br>y", "x<br>", "<em>x<br></em>y",
		"<em><br>x</em>", "<strong>x</strong>\nz<br>w", "x<br>\n<em>y</em>", "<s>x</s><br><s>y</s>", "x<br> <br>y",
	} {
		for _, w := range []string{"%s", "<ul><li>%s</li></ul>", "<blockquote>%s</blockquote>", "<h1>%s</h1>", "<ol><li>x<ul><li>%s</li></ul></li></ol>"} {
			stableHTML(t, "<body>"+strings.Replace(w, "%s", in, 1)+"</body>")
		}
	}
}

// (3)(4) structure
func TestIssue24StructureAttack(t *testing.T) {
	for _, src := range []string{
		"- > a\n  > b", "- > a\n  b", "- > a\n\n  b", "- > > a\n  > b\n  c", "- > a\n  > - b\n  >   c", "> - a\n>   b\n> - c",
		"> - a\n> b\n- c", "> - a\n\n> - b", "> > - a\n> >   b\n> c", "> - a\n- b", "- a\n  > b\n  c\n- d", "- a\n  > b\n\n  c",
		"-\n- b", "-\n\n- b", "-\n\n  b", "-\n\nb", "-\n  b", "- \n  - \n\n  c", "1.\n\n2. x", "1.\n\n   x\n2. y", "-\n\n  - b",
		"-\n\n> q", "-\n\n# h", "- a\n-\n\n  c\n- d", "> -\n>\n> - b", "> - a\n>\n>   b\n>\n> c", "> -\n>\n>   b",
		"- > a\n- > b", "- > - a\n  >   b\n  > - c", "> - > a\n>   > b\n>   c", "1. > a\n   > b\n   c",
		"-\n\n    code", "-\n\n\ttab", "- a\n\n  > q\n\n  b",
	} {
		stableMD(t, src)
	}
	// deep alternation and a long run: no blow up
	var b strings.Builder
	for i := 0; i < 2000; i++ {
		b.WriteString("- > a\n  b\n\n  > c\n-\n\n  d\n")
	}
	h := FromMarkdown(b.String())
	_ = ToEditableMarkdown(h)
}

// Hard breaks: written only after text on the line, and before text (or a following element).
func TestIssue24HardBreakExact(t *testing.T) {
	for h, want := range map[string]string{
		"<em>x</em><br>y": "*x*  \ny",
		"x<br>":           "x",
		"<br>y":           "y",
		"x<br><em>y</em>": "x  \n*y*",
		"<em>x<br></em>y": "*x*  \ny",
		"x<br><br>y":      "x  \n\ny",
	} {
		if got := ToEditableMarkdown("<body>" + h + "</body>"); got != want {
			t.Errorf("%q: got %q, want %q", h, got, want)
		}
	}
}
