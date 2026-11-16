package richtext

import (
	"math/rand"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"
)

var nestTexts = []string{"a", "b", "x y", " ", "  ", "\t", "a ", " a", "a  ", "# h", "- x", "1. y", "> q", "\\", "a\\", "&#32;", "&amp;#32;", "日本", ""}

func genNestInline(r *rand.Rand, d int) string {
	var sb strings.Builder
	for i := r.Intn(3); i > 0; i-- {
		switch r.Intn(7) {
		case 0:
			sb.WriteString("<br>")
		case 1:
			sb.WriteString("\n")
		case 2:
			if d < 1 {
				sb.WriteString("<strong>" + genNestInline(r, d+1) + "x</strong>")
			}
		case 3:
			if d < 1 {
				sb.WriteString("<em>x" + genNestInline(r, d+1) + "</em>")
			}
		case 4:
			sb.WriteString("<code>" + escHTML(nestTexts[r.Intn(len(nestTexts))]) + "c</code>")
		default:
			sb.WriteString(escHTML(nestTexts[r.Intn(len(nestTexts))]))
		}
	}
	return sb.String()
}

func genNestBlock(r *rand.Rand, d int) string {
	n := 6
	if d >= 3 {
		n = 2
	}
	if d > 0 {
		n = 6
	}
	switch r.Intn(n) {
	case 2, 3:
		tag := []string{"ul", "ol"}[r.Intn(2)]
		s := "<" + tag + ">"
		for i := r.Intn(3) + 1; i > 0; i-- {
			s += "<li>" + genNestInline(r, 0)
			for j := r.Intn(2); j > 0; j-- {
				s += genNestBlock(r, d+1)
			}
			s += genNestInline(r, 0) + "</li>"
		}
		return s + "</" + tag + ">"
	case 4:
		return "<blockquote>" + genNestInline(r, 0) + strings.Repeat(genNestBlock(r, d+1), r.Intn(3)) + "</blockquote>"
	case 5:
		return "<pre>p" + escHTML(nestTexts[r.Intn(len(nestTexts))]) + "\n" + escHTML(nestTexts[r.Intn(len(nestTexts))]) + "</pre>"
	case 1:
		if d == 0 {
			return "<h2>" + genNestInline(r, 0) + "x</h2>"
		}
	}
	return genNestInline(r, 0)
}

func roundtrip(in string) (md1, h1, md2, h2 string) {
	md1 = ToEditableMarkdown(in)
	h1 = FromMarkdown(md1)
	md2 = ToEditableMarkdown(h1)
	h2 = FromMarkdown(md2)
	return
}

// Deeply mixed quote/list/pre/empty-item trees must settle after one round trip.
func TestNestedStructuresStable(t *testing.T) {
	r := rand.New(rand.NewSource(4849))
	fails := 0
	for i := 0; i < 60000 && fails < 8; i++ {
		var sb strings.Builder
		sb.WriteString("<body>")
		for j := r.Intn(3) + 1; j > 0; j-- {
			sb.WriteString(genNestBlock(r, 0))
		}
		in := sb.String() + "</body>"
		md1, h1, md2, h2 := roundtrip(in)
		if h1 != h2 {
			fails++
			t.Errorf("not stable\nin  %q\nmd1 %q\nh1  %q\nmd2 %q\nh2  %q", in, md1, h1, md2, h2)
		}
	}
}

// Characters must not appear out of nowhere: no stray backslash line, no &#32; text.
func TestNoLeakedFillers(t *testing.T) {
	r := rand.New(rand.NewSource(52))
	fails := 0
	for i := 0; i < 60000 && fails < 8; i++ {
		var sb strings.Builder
		sb.WriteString("<body>")
		for j := r.Intn(3) + 1; j > 0; j-- {
			sb.WriteString(genNestBlock(r, 0))
		}
		in := sb.String() + "</body>"
		// inputs that legitimately contain a backslash / entity text are skipped
		if strings.Contains(in, "\\") || strings.Contains(in, "&amp;#32;") || strings.Contains(in, "&#32;") {
			continue
		}
		_, h1, _, _ := roundtrip(in)
		doc, _ := html.Parse(strings.NewReader(h1))
		txt := nodeText(doc)
		if strings.Contains(txt, "\\") || strings.Contains(txt, "&#32;") || strings.Contains(txt, "&#") {
			fails++
			t.Errorf("leaked filler\nin %q\nh1 %q", in, h1)
		}
	}
}

func nodeText(n *html.Node) string {
	if n.Type == html.TextNode {
		return n.Data
	}
	var sb strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		sb.WriteString(nodeText(c))
	}
	return sb.String()
}

// Visible (non-space) characters are neither lost nor added by one round trip.
func TestNonSpaceCharsPreserved(t *testing.T) {
	r := rand.New(rand.NewSource(51))
	strip := func(s string) string {
		doc, _ := html.Parse(strings.NewReader(s))
		return strings.Join(strings.Fields(nodeText(doc)), "")
	}
	fails := 0
	for i := 0; i < 60000 && fails < 8; i++ {
		var sb strings.Builder
		sb.WriteString("<body>")
		for j := r.Intn(3) + 1; j > 0; j-- {
			sb.WriteString(genNestBlock(r, 0))
		}
		in := sb.String() + "</body>"
		if strings.Contains(in, "&#32;") {
			continue
		}
		// markdown-looking lines ("# h", "- x", "1. y", "> q") are escaped, so they stay text
		_, h1, _, _ := roundtrip(in)
		if strip(in) != strip(h1) {
			fails++
			t.Errorf("chars changed\nin %q\nh1 %q\n a %q\n b %q", in, h1, strip(in), strip(h1))
		}
	}
}

// What a user types by hand must not be eaten or turned into something else.
func TestUserTypedFillerLookalikes(t *testing.T) {
	for _, md := range []string{"- &#32;", "- &#32;\n- b", "> &#32;", "a\n\\\nb", "- a\n  \\\n  b", "> a\n> \\\n> b", "- \\", "\\", "1. &#32;"} {
		h1 := FromMarkdown(md)
		h2 := FromMarkdown(ToEditableMarkdown(h1))
		if h1 != h2 {
			t.Errorf("md %q: h1 %q h2 %q", md, h1, h2)
		}
	}
	// a literal backslash and a literal "&#32;" in text survive
	for _, in := range []string{"<body>a\\</body>", "<body><ul><li>\\</li></ul></body>", "<body><ul><li>&amp;#32;</li></ul></body>", "<body><blockquote>&amp;#32;</blockquote></body>"} {
		_, h1, _, h2 := roundtrip(in)
		if h1 != h2 || strings.ReplaceAll(nodeTextOf(in), "\n", "") != strings.ReplaceAll(nodeTextOf(h1), "\n", "") {
			t.Errorf("in %q h1 %q h2 %q", in, h1, h2)
		}
	}
}

func nodeTextOf(s string) string { d, _ := html.Parse(strings.NewReader(s)); return nodeText(d) }

// Comment nodes, processing junk and long runs of leading <br> must not hang or panic.
func TestLeadingBreaksTerminate(t *testing.T) {
	ins := []string{
		"<ul><li><!--c--><br><!--d--><br></li></ul>",
		"<ul><li><br><br><br><strong><br> </strong></li></ul>",
		"<ul><li><span></span><em></em><br></li></ul>",
		"<ul><li>" + strings.Repeat("<br>", 20000) + "x</li></ul>",
		"<ul><li>" + strings.Repeat("<strong>", 500) + "<br>x" + strings.Repeat("</strong>", 500) + "</li></ul>",
		"<ul><li>\n</li><li>\n\n</li><li> </li><li><br></li></ul>",
		"<blockquote><blockquote><blockquote></blockquote></blockquote></blockquote>",
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, in := range ins {
			_, h1, _, h2 := roundtrip("<body>" + in + "</body>")
			if h1 != h2 {
				t.Errorf("unstable %.80q\nh1 %.200q\nh2 %.200q", in, h1, h2)
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("hang")
	}
}

func TestDeepAndLongStayFast(t *testing.T) {
	start := time.Now()
	in := strings.Repeat("<blockquote>", 200) + "x" + strings.Repeat("</blockquote>", 200)
	in += "<ul>" + strings.Repeat("<li>a  </li>", 20000) + "</ul>"
	in += strings.Repeat("<blockquote><ul><li>a<ul><li></li></ul></li></ul></blockquote>", 3000)
	roundtrip("<body>" + in + "</body>")
	if d := time.Since(start); d > 15*time.Second {
		t.Fatalf("too slow: %v", d)
	}
}

// Table of minimal cases found by TestNestedStructuresStable: each must be stable.
func TestMinimalCasesStable(t *testing.T) {
	cases := map[string]string{
		"EmptyNestedItemBeforeQuote_ul": "<ul><li><ul><li><blockquote>a</blockquote></li></ul></li></ul>",  // nested "- " + blank line reads as a setext underline: parent becomes <h2>
		"EmptyNestedItemBeforePre_ol":   "<ol><li><ol><li><pre>a</pre></li></ol></li></ol>",                // nested "1. " + blank line stays the text "1."
		"BreakLineStartsQuoteInItem":    "<ul><li><blockquote>\n<strong>x</strong></blockquote></li></ul>", // "> \\" leaks out of the item
		"BreakLineAfterBlockInItem":     "<ul><li><pre>p</pre><br><strong>x</strong></li></ul>",            // "\\" line after the pre
		"BreakAtQuoteStartAfterBr":      "<ul><li><br><blockquote><br>x</blockquote></li></ul>",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			md1, h1, md2, h2 := roundtrip("<body>" + in + "</body>")
			if h1 != h2 {
				t.Errorf("not stable\nmd1 %q\nh1  %q\nmd2 %q\nh2  %q", md1, h1, md2, h2)
			}
		})
	}
}
