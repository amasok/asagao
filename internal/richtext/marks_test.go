package richtext

import (
	"math/rand"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode"

	gast "github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// styledText is the text of rich text h, each character (but whitespace, which moves out of
// emphasis) with the styles it is shown in: "b" bold, "i" italic, "s" struck, "c" code.
// Links and line structure are left out.
func styledText(h string) []string {
	doc, err := html.Parse(strings.NewReader(h))
	if err != nil {
		return []string{"parse error " + err.Error()}
	}
	var out []string
	var walk func(n *html.Node, st map[string]bool)
	walk = func(n *html.Node, st map[string]bool) {
		if n.Type == html.TextNode {
			var keys []string
			for k, on := range st {
				if on {
					keys = append(keys, k)
				}
			}
			sort.Strings(keys)
			for _, r := range n.Data {
				if !unicode.IsSpace(r) {
					out = append(out, string(r)+":"+strings.Join(keys, ""))
				}
			}
			return
		}
		key := ""
		switch n.DataAtom {
		case atom.Strong, atom.B:
			key = "b"
		case atom.Em, atom.I:
			key = "i"
		case atom.S, atom.Del, atom.Strike:
			key = "s"
		case atom.Code:
			key = "c"
		}
		was := st[key]
		if key != "" {
			st[key] = true
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			walk(ch, st)
		}
		if key != "" {
			st[key] = was
		}
	}
	walk(doc, map[string]bool{})
	return out
}

func sameStyle(a, b []string) bool {
	return strings.Join(a, "|") == strings.Join(b, "|")
}

// #58: emphasis next to punctuation, links, other emphasis, email addresses and blank lines
// is read back as the same emphasis, and stays so on the next round trip.
func TestEmphasisBoundaries(t *testing.T) {
	cases := []struct{ in, md string }{
		// next to punctuation and links: an escaped space sets the delimiters apart
		{`<body>x<strong><a href="https://u.jp/">l</a></strong>y</body>`, `x\ **[l](https://u.jp/)**\ y`},
		{"<body>これは<strong>「重要」</strong>です</body>", `これは\ **「重要」**\ です`},
		{"<body><strong>(a)</strong>b</body>", `**(a)**\ b`},
		{"<body>a<strong>!</strong></body>", `a\ **!**`},
		{"<body>a<s>(b)</s>c</body>", `a\ ~~(b)~~\ c`},
		{"<body>a<em>「b」</em>c</body>", `a\ *「b」*\ c`},
		{`<body>a<strong><img data-asana-gid="7" src="x"></strong>b</body>`, `a\ **![画像](asana:7)**\ b`},
		// next to the same emphasis, or code next to code: set apart
		{"<body><strong>a</strong><strong>b</strong></body>", `**a**\ **b**`},
		{"<body><em>x</em><em>y</em></body>", `*x*\ *y*`},
		{"<body><s>a</s><s>b</s></body>", `~~a~~\ ~~b~~`},
		{"<body><u><strong>a</strong></u><strong>b</strong></body>", `**a**\ **b**`},
		{"<body><code>a</code><code>b</code></body>", "`a`\\ `b`"},
		// "~~" after a "~" (goldmark's strikethrough does not open or close there)
		{"<body><s>a~</s>b</body>", `~~a&#126;~~\ b`},
		{"<body>a~<s>b</s></body>", "a&#126;~~b~~"},
		// a URL is not read on into the delimiters (#77)
		{"<body><s>https://x.jp/a</s>a</body>", `~~https://x.jp/a~~\ a`},
		{"<body>https://x.jp/a<s>b</s></body>", `https://x.jp/a\ ~~b~~`},
		{"<body>https://x.jp/a<code>b</code></body>", "https://x.jp/a\\ `b`"},
		// next to other emphasis
		{"<body><em>a</em><strong>b</strong></body>", `*a*\ **b**`},
		{"<body><strong>a</strong><em>b</em></body>", `**a**\ *b*`},
		// inside the same emphasis: the outer one
		{"<body><strong><strong>a</strong></strong></body>", "**a**"},
		{"<body><em><em>a</em></em></body>", "*a*"},
		{"<body><em>a<em>b</em>c</em></body>", "*abc*"},
		{"<body><s>a<s>b</s></s></body>", "~~ab~~"},
		// an email address: the delimiters are not part of it
		{"<body>z<strong>a@x.jp</strong></body>", `z\ **a@x.jp**`},
		{"<body>z<strong>a</strong>b@x.jp</body>", `z**a**\ b@x.jp`},
		{"<body><strong>a</strong>b@x.jp</body>", `**a**\ b@x.jp`},
		{"<body>z<s>a@x.jp</s></body>", `z\ ~~a@x.jp~~`},
		// over a blank line: emphasis of each paragraph
		{"<body><strong>a\n\nb</strong></body>", "**a**\n\n**b**"},
		{"<body><strong>a<br><br>b</strong></body>", "**a**  \n\n**b**"},
		{"<body><strong>a\n \nb</strong></body>", "**a**\n\n**b**"},
		{"<body><strong>a<em>b\n\nc</em>d</strong></body>", "**a*b***\n\n***c*d**"},
		{"<body><strong>a\nb</strong></body>", "**a  \nb**"}, // one line break: one emphasis
		// as before
		{"<body><em>x<strong>x</strong></em></body>", "*x**x***"},
		{"<body><strong>a<em>b</em>c</strong></body>", "**a*b*c**"},
		{"<body><strong>a</strong>!</body>", "**a**!"},
		{"<body>a <strong>b</strong> c</body>", "a **b** c"},
	}
	for _, c := range cases {
		md1, h1, md2, h2 := rt2(c.in)
		if md1 != c.md {
			t.Errorf("%q: markdown %q, want %q", c.in, md1, c.md)
		}
		if !sameStyle(styledText(c.in), styledText(h1)) {
			t.Errorf("%q: styles lost\nmd1 %q\nh1  %q", c.in, md1, h1)
		}
		if h1 != h2 {
			t.Errorf("%q: unstable\nmd1 %q\nh1  %q\nmd2 %q\nh2  %q", c.in, md1, h1, md2, h2)
		}
		if strings.Contains(h1, `\`) {
			t.Errorf("%q: the escaped space is sent: %q", c.in, h1)
		}
	}
}

// #58: an email address next to emphasis keeps the delimiters out of the link.
func TestEmphasisNextToEmailNotLinked(t *testing.T) {
	for in, want := range map[string]string{
		// the address after the emphasis is linked, as one right after "**" is
		"<body>z<strong>a</strong>b@x.jp</body>": `<body>z<strong>a</strong><a href="mailto:b@x.jp">b@x.jp</a></body>`,
		"<body><strong>a</strong>b@x.jp</body>":  `<body><strong>a</strong><a href="mailto:b@x.jp">b@x.jp</a></body>`,
		// the address alone in the emphasis is linked, as one alone in a line is
		"<body>z<strong>a@x.jp</strong></body>": `<body>z<strong><a href="mailto:a@x.jp">a@x.jp</a></strong></body>`,
		// #77: inline code right after a letter
		"<body>z<code>a@x.jp</code></body>": "<body>z<code>a@x.jp</code></body>",
		"<body>z<code>a</code>@x.jp</body>": "<body>z<code>a</code>@x.jp</body>",
	} {
		if _, h1, _, _ := rt2(in); h1 != want {
			t.Errorf("%q -> %q, want %q", in, h1, want)
		}
	}
}

// Code spans next to each other stay apart: "`a“b`" would be the code "a“b".
func TestAdjacentCodeSpans(t *testing.T) {
	in := "<body><code>a</code><code>b</code></body>"
	md1, h1, _, _ := rt2(in)
	if md1 != "`a`\\ `b`" || h1 != in {
		t.Errorf("%q -> %q -> %q", in, md1, h1)
	}
}

// An escaped space right next to a delimiter is nothing (ToEditableMarkdown writes it
// there); elsewhere it is text as typed (in Markdown a space cannot be escaped).
func TestEscapedSpaceNextToDelimitersOnly(t *testing.T) {
	for md, want := range map[string]string{
		`x\ **(a)**\ y`:         "<body>x<strong>(a)</strong>y</body>",
		`a\ ~~(b)~~\ c`:         "<body>a<s>(b)</s>c</body>",
		"`a`\\ `b`":             "<body><code>a</code><code>b</code></body>",
		`x\ [l](https://x.jp/)`: `<body>x<a href="https://x.jp/">l</a></body>`,
		`https://x.jp/a\ &#32;`: `<body><a href="https://x.jp/a">https://x.jp/a</a> </body>`,
		// typed by the user: kept
		`cd My\ Documents`:      `<body>cd My\ Documents</body>`,
		`C:\ drive`:             `<body>C:\ drive</body>`,
		`a\\ b`:                 `<body>a\ b</body>`,
		"`a\\ b`":               "<body><code>a\\ b</code></body>",
		`[a\ b](https://x.jp/)`: `<body><a href="https://x.jp/">a\ b</a></body>`,
	} {
		if got := FromMarkdown(md); got != want {
			t.Errorf("%q -> %q, want %q", md, got, want)
		}
	}
	if got := PlainFromMarkdown(`x\ **(a)**\ y`); got != "x(a)y" {
		t.Errorf("plain %q", got)
	}
	// and what the user typed goes back to the editor as typed
	for _, md := range []string{`cd My\ Documents`, `C:\ drive`} {
		if got := ToEditableMarkdown(FromMarkdown(md)); got != strings.ReplaceAll(md, `\`, `\\`) {
			t.Errorf("%q -> %q", md, got)
		}
	}
}

// For display a plain space sets emphasis apart (the renderer shows "\ " as it is).
func TestEmphasisBoundariesForDisplay(t *testing.T) {
	for in, want := range map[string]string{
		"<body>これは<strong>「重要」</strong>です</body>":                            "これは **「重要」** です",
		`<body>x<a data-asana-gid="1" data-asana-type="user">@N</a>y</body>`: "x **@N**y",
		// a mention in bold is not bold again ("****" is not bold)
		`<body><strong>x<a data-asana-gid="1" data-asana-type="user">@N</a></strong></body>`: "**x@N**",
	} {
		if got := ToMarkdown(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

// Emphasis in a heading: the delimiters move with the text after the "#"s.
func TestEmphasisBoundariesInHeading(t *testing.T) {
	in := "<body><h1>  a<strong>「b」</strong>c</h1></body>"
	md1, h1, _, h2 := rt2(in)
	if md1 != `# a\ **「b」**\ c` || h1 != h2 || !sameStyle(styledText(in), styledText(h1)) {
		t.Errorf("%q -> %q -> %q", in, md1, h1)
	}
}

var markTexts = []string{"a", "b c", "日本", "(", ")", "「", "」", "!", ".", "-", "_", "*", "~", "`", "@", "a@x.jp", "x.jp", "www.x.jp", "https://x.jp/a", "1.", "-", "\u00a0", "\u3000", " ", "\n", "\n\n", "\n \n", "\t", "&", "<", "#"}

func genMarkInline(r *rand.Rand, depth int) string {
	var sb strings.Builder
	for i := r.Intn(4) + 1; i > 0; i-- {
		switch k := r.Intn(9); {
		case k < 3 && depth < 3:
			tag := []string{"strong", "b", "em", "i", "s", "del", "u"}[r.Intn(7)]
			inner := genMarkInline(r, depth+1)
			if tag == "u" {
				// (whitespace before an element starting with a line break, in a list item,
				// is not stable on main either: not about emphasis, #83)
				inner = "x" + inner
			}
			sb.WriteString("<" + tag + ">" + inner + "</" + tag + ">")
		case k == 3:
			sb.WriteString("<br>")
		case k == 4:
			sb.WriteString(`<a href="https://x.jp/">` + escHTML(strings.TrimSpace(markTexts[r.Intn(9)])+"l") + "</a>")
		case k == 5:
			// code without line breaks (#65 is about those) next to the emphasis
			sb.WriteString("<code>" + escHTML([]string{"a", "x y", "`", "a@x.jp", "*"}[r.Intn(5)]) + "</code>")
		default:
			sb.WriteString(escHTML(markTexts[r.Intn(len(markTexts))]))
		}
	}
	return sb.String()
}

// #58: random emphasis (nested, next to each other, next to punctuation, links, code and
// email addresses, over line breaks and blank lines) keeps its styles over the round trip,
// and the round trip is stable.
func TestEmphasisRandomRoundTrip(t *testing.T) {
	r := rand.New(rand.NewSource(58))
	fails := 0
	for i := 0; i < 8000 && fails < 8; i++ {
		in := "<body>" + genMarkInline(r, 0) + genMarkInline(r, 0) + "</body>"
		if r.Intn(4) == 0 {
			in = "<body><ul><li>" + genMarkInline(r, 0) + "</li></ul><blockquote>" + genMarkInline(r, 0) + "</blockquote></body>"
		}
		md1, h1, md2, h2 := rt2(in)
		if !sameStyle(styledText(in), styledText(h1)) || h1 != h2 {
			fails++
			t.Errorf("in  %q\nmd1 %q\nh1  %q\nmd2 %q\nh2  %q\nstyles %v\n    -> %v", in, md1, h1, md2, h2, styledText(in), styledText(h1))
		}
	}
}

// Setting delimiters apart looks only near them: one long line of emphasis that needs it is
// converted in linear time.
func TestEmphasisBoundariesLinear(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test")
	}
	in := "<body>" + strings.Repeat(" x<strong>(a)</strong>y <em>a</em><em>b</em> https://x.jp/a<s>b</s>", 4000)[1:] + "</body>"
	start := time.Now()
	md := ToEditableMarkdown(in)
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("converting %d bytes took %v", len(in), d)
	}
	if want := `x\ **(a)**\ y *a*\ *b* https://x.jp/a\ ~~b~~`; !strings.HasPrefix(md, want) || strings.Count(md, `\ `) != 4*4000 {
		t.Fatalf("unexpected output %q", md[:80])
	}
}

// #77: Linkify reads the editable Markdown as it reads Markdown without escapes: a URL
// holding characters that are escaped stays text (it was linked cut short at the first
// backslash), a URL without them is linked as Linkify reads it, and an address or URL after indentation written as "&#32;" is linked, as one
// after a space is (it was text, and became a link once the indentation was gone).
func TestLinkifyAsWithoutEscapes(t *testing.T) {
	for _, c := range []struct{ in, md, h1 string }{
		{"<body>see https://x.jp/a_b</body>", `see https\://x.jp/a\_b`, "<body>see https://x.jp/a_b</body>"},
		{"<body>www.x.jp/a~b</body>", `www\.x.jp/a\~b`, "<body>www.x.jp/a~b</body>"},
		// Linkify leaves out of the URL what it would leave out without escapes
		{"<body>www.x.jp/*a*</body>", `www.x.jp/\*a\*`, `<body><a href="http://www.x.jp/">www.x.jp/</a>*a*</body>`},
		{"<body>see https://x.jp/a_.</body>", `see https://x.jp/a\_.`, `<body>see <a href="https://x.jp/a">https://x.jp/a</a>_.</body>`},
		{"<body>see https://x.jp/?a=1&amp;copy;</body>", `see https://x.jp/?a=1\&copy;`, `<body>see <a href="https://x.jp/?a=1">https://x.jp/?a=1</a>&amp;copy;</body>`},
		// where Linkify starts a URL: after "(" too, and not in the domain of an address
		{"<body>x(https://x.jp/a_b</body>", `x(https\://x.jp/a\_b`, "<body>x(https://x.jp/a_b</body>"},
		{"<body>a@x.jpwww.x.jp/_</body>", `a@x.jpwww.x.jp/\_`, `<body><a href="mailto:a@x.jpwww.x.jp">a@x.jpwww.x.jp</a>/_</body>`},
		// after emphasis, where Linkify starts too
		{"<body><b>a</b>www.x.jp/a_b</body>", `**a**www\.x.jp/a\_b`, "<body><strong>a</strong>www.x.jp/a_b</body>"},
		// without escapes, linked as before
		{"<body>see https://x.jp/ab</body>", "see https://x.jp/ab", `<body>see <a href="https://x.jp/ab">https://x.jp/ab</a></body>`},
		// after indentation in a list item, and after a space at the end of a line
		{"<body><ul><li> www.x.jp</li></ul></body>", "- &#32;www.x.jp", `<body><ul><li> <a href="http://www.x.jp">www.x.jp</a></li></ul></body>`},
		{"<body><ul><li>a\n\tb@x.jp</li></ul></body>", "- a  \n&#9;b@x.jp", `<body><ul><li>a` + "\n\t" + `<a href="mailto:b@x.jp">b@x.jp</a></li></ul></body>`},
		{"<body>https://x.jp/a <br>b</body>", "https://x.jp/a\\ &#32;  \nb", `<body><a href="https://x.jp/a">https://x.jp/a</a> ` + "\n" + `b</body>`},
	} {
		md1, h1, _, h2 := rt2(c.in)
		if md1 != c.md || h1 != c.h1 || h1 != h2 {
			t.Errorf("%q\nmd1 %q want %q\nh1  %q want %q\nh2  %q", c.in, md1, c.md, h1, c.h1, h2)
		}
	}
}

// Text after indentation written by text before it (in another element) still starts the
// line as Markdown reads it: "\t" then "#" was a heading, or an indented code block.
func TestBlockSyntaxAfterIndentInAnotherElement(t *testing.T) {
	for _, in := range []string{
		"<body>\t<u>#</u>x</body>",
		"<body>a\n <u># b</u></body>",
		"<body> <u>- b</u></body>",
	} {
		md1, h1, _, h2 := rt2(in)
		if want := strings.NewReplacer("<u>", "", "</u>", "").Replace(in); h1 != want || h1 != h2 {
			t.Errorf("%q -> %q -> %q -> %q", in, md1, h1, h2)
		}
	}
}

// linkifyURL is a copy of goldmark's Linkify (its regular expressions and what it leaves
// out at the end): it must find the same URL as goldmark does, or escapeInline decides on
// a URL that is not the one linked.
func TestLinkifyURLAsGoldmark(t *testing.T) {
	r := rand.New(rand.NewSource(77))
	chars := "aZ09-_.~#!?&/=();,'\"^{}[]`*:@%+$<> "
	for i := 0; i < 20000; i++ {
		var sb strings.Builder
		sb.WriteString([]string{"https://x.jp", "http://x.jp", "ftp://x.jp", "www.x.jp", "https://x", "www.x"}[r.Intn(6)])
		for j := r.Intn(12); j > 0; j-- {
			sb.WriteByte(chars[r.Intn(len(chars))])
		}
		s := sb.String()
		want := linkifyURL(s)
		// goldmark's link text, read from what it makes of the URL after a space
		got := 0
		doc := md.Parser().Parse(text.NewReader([]byte("x " + s)))
		_ = gast.Walk(doc, func(n gast.Node, entering bool) (gast.WalkStatus, error) {
			if l, ok := n.(*gast.AutoLink); ok && entering && got == 0 {
				got = len(l.Label([]byte("x " + s)))
				return gast.WalkStop, nil
			}
			return gast.WalkContinue, nil
		})
		if got != want {
			t.Fatalf("%q: linkifyURL %d, goldmark %d", s, want, got)
		}
	}
}

// Line structure next to emphasis and text that is not ASCII whitespace:
//   - a line break and an NBSP at the end of emphasis move out of it in their order (the
//     NBSP went before the <br>, which then met the next one: a blank line in the italic);
//   - "1." before more text on its line, in another element, is not a list marker (it was
//     escaped, which kept "1.a@x.jp" from being linked, until the text was one);
//   - spaces at the end of a line before a line of only NBSP or U+3000 are kept (that line
//     is text, not the end of the block).
func TestLineStructureNextToEmphasis(t *testing.T) {
	for in, want := range map[string]string{
		"<body><em><strong>x<br> </strong><br>a</em></body>": "<body><em><strong>x</strong>\n \na</em></body>",
		"<body><em>x<br>1.<i>a@x.jp</i></em></body>":         `<body><em>x` + "\n" + `<a href="mailto:1.a@x.jp">1.a@x.jp</a></em></body>`,
		"<body>x<br>1.<u>a@x.jp</u></body>":                  `<body>x` + "\n" + `<a href="mailto:1.a@x.jp">1.a@x.jp</a></body>`,
		"<body>x<br>1.<u> a</u></body>":                      "<body>x\n1. a</body>",
		"<body>a\t \n </body>":                               "<body>a\t \n </body>",
		"<body>x \t<br>　</body>":                             "<body>x \t\n　</body>",
	} {
		md1, h1, md2, h2 := rt2(in)
		if h1 != want || h1 != h2 {
			t.Errorf("%q\nmd1 %q\nh1  %q want %q\nmd2 %q\nh2  %q", in, md1, h1, want, md2, h2)
		}
	}
}
