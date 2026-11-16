package richtext

import (
	"fmt"
	"math/rand"
	"regexp"
	"strings"
	"testing"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// nestingViolation returns how rich text breaks Asana's nesting rules, or "" if it does not.
// https://developers.asana.com/docs/rich-text: "lists and list items (<ol>, <ul>, <li>)
// cannot be nested inside headers, quotation blocks and pre-formatted text elements", and
// "Nesting headers (<h1>, <h2>), quotation blocks (<blockquote>) or pre-formatted text
// (<pre>) inside list items is not supported". Asana rejects such html_text with 400.
func nestingViolation(s string) string {
	doc, err := parseRich(s)
	if err != nil {
		return err.Error()
	}
	var walk func(n *html.Node, inNoList, inItem string) string
	walk = func(n *html.Node, inNoList, inItem string) string {
		if n.Type == html.ElementNode {
			switch n.DataAtom {
			case atom.Ul, atom.Ol, atom.Li:
				if inNoList != "" {
					return fmt.Sprintf("<%s> in <%s>", n.Data, inNoList)
				}
			case atom.H1, atom.H2, atom.H3, atom.Blockquote, atom.Pre:
				if inItem != "" {
					return fmt.Sprintf("<%s> in <li>", n.Data)
				}
			}
			switch n.DataAtom {
			case atom.H1, atom.H2, atom.H3, atom.Blockquote, atom.Pre:
				if inNoList == "" {
					inNoList = n.Data
				}
			case atom.Li:
				inItem = "li"
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if v := walk(c, inNoList, inItem); v != "" {
				return v
			}
		}
		return ""
	}
	return walk(doc, "", "")
}

// selfClosedLink matches a mention as FromMarkdown writes it (<a data-asana-gid="1"/>).
var selfClosedLink = regexp.MustCompile(`<a ([^>]*)/>`)

// parseRich parses rich text as XML would read it: an HTML parser takes "<a .../>" for an
// open link and moves what follows into it.
func parseRich(s string) (*html.Node, error) {
	return html.Parse(strings.NewReader(selfClosedLink.ReplaceAllString(s, "<a $1></a>")))
}

func TestNestingViolation(t *testing.T) {
	for in, bad := range map[string]bool{
		"<body><blockquote><ul><li>x</li></ul></blockquote></body>":                               true,
		"<body><blockquote><blockquote><ol><li>x</li></ol></blockquote></blockquote></body>":      true,
		"<body><ul><li><blockquote>x</blockquote></li></ul></body>":                               true,
		"<body><ul><li><ul><li><h1>x</h1></li></ul></li></ul></body>":                             true,
		"<body><ol><li><pre>x</pre></li></ol></body>":                                             true,
		"<body><blockquote>- x</blockquote><ul><li>&gt; q</li></ul></body>":                       false,
		"<body><ul><li>a<ul><li>b</li></ul></li></ul></body>":                                     false,
		"<body><blockquote>a<blockquote>b</blockquote><pre>c</pre><h2>d</h2></blockquote></body>": false,
		`<body><a data-asana-gid="1"/><ul><li>x</li></ul></body>`:                                 false,
		`<body><ul><li><a data-asana-gid="1"/></li></ul><blockquote>x</blockquote></body>`:        false,
	} {
		if got := nestingViolation(in) != ""; got != bad {
			t.Errorf("%q: violation %v, want %v", in, got, bad)
		}
	}
}

// Markdown that nests what Asana cannot nest is sent as rich text Asana accepts (#60): a list
// in a quote, and a quote, heading or code block in a list item, are kept as the text that
// was written (markers and all), inside the quote or the item; the quote, and the list with
// its items and numbers, stay as they are. Every case is stable through another trip.
func TestFromMarkdownFollowsNestingRules(t *testing.T) {
	for _, c := range []struct{ md, want string }{
		{"> - x", "<body><blockquote>- x</blockquote></body>"},
		{"> * x\n> * y", "<body><blockquote>* x\n* y</blockquote></body>"},
		{"> 引用\n> - 項目1\n> - 項目2", "<body><blockquote>引用\n- 項目1\n- 項目2</blockquote></body>"},
		{"> 1. a\n> 1. b\n>    - c", "<body><blockquote>1. a\n2. b\n   - c</blockquote></body>"},
		{"> 3) a\n> 4) b", "<body><blockquote>3) a\n4) b</blockquote></body>"},
		{"> - **太字** [l](http://x.jp)\n> - ![画像](asana:7) [@A](asana:user:9)",
			`<body><blockquote>- <strong>太字</strong> <a href="http://x.jp">l</a>` + "\n" + `- <img data-asana-gid="7"/> <a data-asana-gid="9"/></blockquote></body>`},
		{"> - a\n>\n> - b", "<body><blockquote>- a\n\n- b</blockquote></body>"},
		{"> - a\n\n後", "<body><blockquote>- a</blockquote>後</body>"},
		{"> > - x", "<body><blockquote><blockquote>- x</blockquote></blockquote></body>"},
		{"> - > x", "<body><blockquote>- &gt; x</blockquote></body>"},
		{"> - a <b>", "<body><blockquote>- a &lt;b&gt;</blockquote></body>"},
		{"- > q", "<body><ul><li>&gt; q</li></ul></body>"},
		{"- a\n  > q\n  > r", "<body><ul><li>a\n&gt; q\n&gt; r</li></ul></body>"},
		{"1. a\n   > q\n2. b", "<body><ol><li>a\n&gt; q</li><li>b</li></ol></body>"},
		{"- # 見出し", "<body><ul><li># 見出し</li></ul></body>"},
		{"- a\n  ## h\n  - b", "<body><ul><li>a\n## h<ul><li>b</li></ul></li></ul></body>"},
		{"1. a\n   ```go\n   x := 1 < 2\n\n     y@x.jp [l](http://x.jp)\n   ```\n2. b",
			"<body><ol><li>a\n<code>x := 1 &lt; 2</code>\n\n<code>  y@x.jp [l](http://x.jp)</code></li><li>b</li></ol></body>"},
		{"- ```\n  a ``` b\n  ```", "<body><ul><li><code>a ``` b</code></li></ul></body>"},
		{"- a\n  ```\n  ```\n- b", "<body><ul><li>a</li><li>b</li></ul></body>"},
		{"> - a\n>   ```\n>   c\n>   ```", "<body><blockquote>- a\n<code>c</code></blockquote></body>"},
		{"- a\n\n      code", "<body><ul><li>a</li></ul>      code</body>"}, // as before: indentation after a blank line is text
		// what Asana takes stays as it was
		{"> # h\n> ```\n> c\n> ```", "<body><blockquote><h1>h</h1><pre>c</pre></blockquote></body>"},
		{"- a\n  - b", "<body><ul><li>a<ul><li>b</li></ul></li></ul></body>"},
	} {
		h1 := FromMarkdown(c.md)
		if h1 != c.want {
			t.Errorf("md %q\ngot  %q\nwant %q", c.md, h1, c.want)
		}
		if v := nestingViolation(h1); v != "" {
			t.Errorf("md %q: %s in %q", c.md, v, h1)
		}
		md2 := ToEditableMarkdown(h1)
		if h2 := FromMarkdown(md2); h2 != h1 {
			t.Errorf("md %q not stable\nh1  %q\nmd2 %q\nh2  %q", c.md, h1, md2, h2)
		}
	}
}

// The plain text sent with it has the same text (markers included).
func TestPlainFromMarkdownNestedText(t *testing.T) {
	for md, want := range map[string]string{
		"> - x\n> - y": "- x\n- y",
		"- > q":        "> q",
	} {
		if got := PlainFromMarkdown(md); got != want {
			t.Errorf("%q: %q, want %q", md, got, want)
		}
	}
}

// genUserMarkdown writes Markdown as a user might, mixing quotes, lists, headings, fences
// and indentation on each line. Each word is unique (w0, w1, ...), to check none is lost.
// A heading has only plain text: marks and mentions in a heading, and an empty heading, do
// not survive a trip on main either (they are not about nesting).
func genUserMarkdown(r *rand.Rand) (string, []string) {
	// one bullet and one ordered marker outside quotes: adjacent lists with different
	// markers ("- " then "* ") become one list on the next trip, on main too
	prefixes := []string{"", "> ", "> > ", "- ", "1. ", "  ", "   ", "    ", "  - ", "   1. ", "> - ", "> * ", "> 1. ", "> 2) ", ">    - ", "- > ", "  > ", "- # ", "  ## ", "# ", "> # ", "- >     "}
	inlines := []string{"", "**b** ", "*e* ", "~~s~~ ", "`c` ", "[l](http://x.jp) ", "a@x.jp ", "![画像](asana:7) ", "[@A](asana:user:9) ", " ", "\t"}
	var lines, words []string
	fence := false
	for i := r.Intn(8) + 1; i > 0; i-- {
		p := prefixes[r.Intn(len(prefixes))]
		heading := strings.HasSuffix(p, "# ") && !strings.HasPrefix(p, "- ")
		// a line of only "-" under text makes it a setext heading (see above)
		item := strings.Contains(p, "- ")
		switch r.Intn(10) {
		case 0:
			if heading || item {
				break
			}
			lines = append(lines, strings.TrimRight(p, " ")) // a blank (quote) line
			continue
		case 1:
			lines = append(lines, p+"```")
			fence = !fence
			continue
		}
		w := fmt.Sprintf("w%d", len(words))
		words = append(words, w)
		if heading {
			lines = append(lines, p+w)
			continue
		}
		lines = append(lines, p+inlines[r.Intn(len(inlines))]+w+strings.TrimRight(inlines[r.Intn(len(inlines))], " "))
	}
	if fence {
		lines = append(lines, "```")
	}
	return strings.Join(lines, "\n"), words
}

// Whatever the user writes, FromMarkdown sends rich text Asana accepts, keeps every word,
// and is stable through another trip (#60).
func TestUserMarkdownFollowsNestingRules(t *testing.T) {
	r := rand.New(rand.NewSource(60))
	fails := 0
	for i := 0; i < 30000 && fails < 8; i++ {
		md, words := genUserMarkdown(r)
		h1 := FromMarkdown(md)
		if v := nestingViolation(h1); v != "" {
			fails++
			t.Errorf("md %q: %s\nh1 %q", md, v, h1)
			continue
		}
		txt := nodeTextOf(h1)
		for _, w := range words {
			if !strings.Contains(txt, w) {
				fails++
				t.Errorf("md %q: lost %q\nh1 %q", md, w, h1)
				break
			}
		}
		if textAfterNestedList(h1) {
			// on main too: text after a nested list in an item is not stable
			continue
		}
		md2 := ToEditableMarkdown(h1)
		if h2 := FromMarkdown(md2); h2 != h1 {
			fails++
			t.Errorf("not stable\nmd  %q\nh1  %q\nmd2 %q\nh2  %q", md, h1, md2, h2)
		}
	}
}

// textAfterNestedList reports whether a list item has text after a list inside it.
func textAfterNestedList(s string) bool {
	doc, _ := parseRich(s)
	var walk func(n *html.Node) bool
	walk = func(n *html.Node) bool {
		if n.Type == html.ElementNode && n.DataAtom == atom.Li {
			seen := false
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if c.Type == html.ElementNode && (c.DataAtom == atom.Ul || c.DataAtom == atom.Ol) {
					seen = true
				} else if seen {
					return true
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if walk(c) {
				return true
			}
		}
		return false
	}
	return walk(doc)
}

// Rich text with nesting Asana does not take (which it does not send, but a broken cache or
// another client might hold) comes back from an edit as rich text it takes.
func TestEditedRichTextFollowsNestingRules(t *testing.T) {
	r := rand.New(rand.NewSource(6060))
	fails := 0
	for i := 0; i < 20000 && fails < 8; i++ {
		var sb strings.Builder
		sb.WriteString("<body>")
		for j := r.Intn(3) + 1; j > 0; j-- {
			if r.Intn(2) == 0 {
				sb.WriteString(genNestBlock(r, 0))
			} else {
				sb.WriteString(genBlockRT(r, 0))
			}
		}
		in := sb.String() + "</body>"
		if _, h1, _, _ := roundtrip(in); nestingViolation(h1) != "" {
			fails++
			t.Errorf("%s\nin %q\nh1 %q", nestingViolation(h1), in, h1)
		}
	}
}
