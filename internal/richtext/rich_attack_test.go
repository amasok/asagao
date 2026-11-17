package richtext

import (
	"strings"
	"testing"

	"github.com/yuin/goldmark"
	gast "github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// oldHasLayoutTag is the pre-b5 implementation, kept to compare against.
func oldHasLayoutTag(s string) bool {
	if !strings.Contains(s, "<") {
		return false
	}
	doc, err := html.Parse(strings.NewReader(s))
	if err != nil {
		return false
	}
	var found func(n *html.Node) bool
	found = func(n *html.Node) bool {
		if n.Type == html.ElementNode {
			switch n.DataAtom {
			case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6, atom.Hr, atom.Img, atom.Table:
				return true
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if found(c) {
				return true
			}
		}
		return false
	}
	return found(doc)
}

func TestHasLayoutTagMatchesOld(t *testing.T) {
	tags := []string{"h1", "H2", "h6", "h7", "hr", "HR", "img", "IMG", "table", "tbody", "td", "ul", "pre", "p", "b", "svg", "math", "foo", "image", "head", "title", "template", "noscript", "textarea", "plaintext", "xmp", "select", "option", "frameset", "iframe"}
	var docs []string
	for _, a := range tags {
		docs = append(docs,
			"<"+a+">x</"+a+">", "<"+a+"/>", "<body><"+a+" x=1>", "<"+a,
			"<svg><"+a+"></svg>", "<math><"+a+"></math>", "<svg><foreignObject><"+a+"></foreignObject></svg>",
			"<!-- <"+a+"> -->", "<p><"+a+"></p>", "<ul><li><"+a+"></li></ul>",
			"<textarea><"+a+"></textarea>", "<script><"+a+"></script>", "<title><"+a+"></title>",
			"<"+a+" <"+a+">>", "<\x00"+a+">", "<"+a+"\x00>")
		for _, b := range tags {
			docs = append(docs, "<"+a+"><"+b+">", "<"+a+"></"+b+">")
		}
	}
	for _, d := range docs {
		if g, w := hasLayoutTag(d), oldHasLayoutTag(d); g != w {
			t.Errorf("hasLayoutTag(%q) = %v, old %v", d, g, w)
		}
	}
}

// oldAsText is the pre-b5 switch.
func oldAsText(w *htmlWriter, n gast.Node) bool {
	switch n.Kind() {
	case gast.KindList:
		return w.quote > 0
	case gast.KindBlockquote, gast.KindHeading, gast.KindFencedCodeBlock, gast.KindCodeBlock, gast.KindThematicBreak:
		return w.item > 0
	}
	return false
}

func TestAsTextMatchesOld(t *testing.T) {
	src := "# h\n\n---\n\n> q\n\n- a\n\n```\nc\n```\n\n    ind\n\npara\n"
	doc := goldmark.New().Parser().Parse(textReader(src))
	var nodes []gast.Node
	_ = gast.Walk(doc, func(n gast.Node, enter bool) (gast.WalkStatus, error) {
		if enter {
			nodes = append(nodes, n)
		}
		return gast.WalkContinue, nil
	})
	for _, q := range []int{0, 1, 2} {
		for _, it := range []int{0, 1, 2} {
			w := &htmlWriter{quote: q, item: it}
			for _, n := range nodes {
				if g, o := w.asText(n), oldAsText(w, n); g != o {
					t.Errorf("asText(%v) quote=%d item=%d = %v, old %v", n.Kind(), q, it, g, o)
				}
			}
		}
	}
}

func textReader(s string) text.Reader { return text.NewReader([]byte(s)) }
