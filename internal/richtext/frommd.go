package richtext

import (
	"html"
	"regexp"
	"strings"

	"github.com/yuin/goldmark"
	gast "github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

// Editable Markdown keeps Asana-specific references in a form that survives editing:
//
//	![画像](asana:1220507679870013)   an image attached to the task
//	[@Hanako](asana:user:123)          a mention
const (
	assetScheme   = "asana:"
	mentionScheme = "asana:user:"
)

var md = goldmark.New(goldmark.WithExtensions(extension.Strikethrough, extension.Linkify))

// FromMarkdown converts Markdown to Asana rich text (the HTML subset accepted in
// html_notes / html_text), wrapped in <body>.
func FromMarkdown(src string) string {
	b := []byte(src)
	doc := md.Parser().Parse(text.NewReader(b))
	w := &htmlWriter{src: b}
	w.blocks(doc)
	return "<body>" + strings.TrimRight(w.b.String(), "\n") + "</body>"
}

// PlainFromMarkdown returns the text of Markdown without markup (for Asana's plain notes).
func PlainFromMarkdown(src string) string {
	b := []byte(src)
	doc := md.Parser().Parse(text.NewReader(b))
	w := &htmlWriter{src: b, plain: true}
	w.blocks(doc)
	// keep the indentation of the first line (written as "&#32;" / "&#9;" in Markdown)
	return strings.TrimLeft(strings.TrimRight(w.b.String(), " \t\n"), "\n")
}

type htmlWriter struct {
	b     strings.Builder
	src   []byte
	plain bool
}

func (w *htmlWriter) tag(open string) {
	if !w.plain {
		w.b.WriteString(open)
	}
}

func (w *htmlWriter) textOut(s string) {
	if w.plain {
		w.b.WriteString(s)
	} else {
		w.b.WriteString(html.EscapeString(s))
	}
}

func isBlockElement(n gast.Node) bool {
	switch n.Kind() {
	case gast.KindList, gast.KindHeading, gast.KindThematicBreak, gast.KindFencedCodeBlock,
		gast.KindCodeBlock, gast.KindBlockquote:
		return true
	}
	return false
}

// blocks writes the block children of n. Paragraphs are separated by a blank line, as
// Asana stores them; block elements start on their own line.
func (w *htmlWriter) blocks(n gast.Node) {
	var prev gast.Node
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		if prev != nil {
			switch {
			case isBlockElement(prev):
				// a block element already ends its line
			case isBlockElement(c):
				w.b.WriteString("\n")
			default:
				w.b.WriteString("\n\n")
			}
		}
		w.block(c)
		prev = c
	}
}

func (w *htmlWriter) block(n gast.Node) {
	switch n := n.(type) {
	case *gast.Paragraph, *gast.TextBlock:
		w.inlines(n)
	case *gast.Heading:
		t := "h2"
		if n.Level == 1 {
			t = "h1"
		}
		w.tag("<" + t + ">")
		w.inlines(n)
		w.tag("</" + t + ">")
		if w.plain {
			w.b.WriteString("\n")
		}
	case *gast.ThematicBreak:
		w.tag("<hr/>")
	case *gast.FencedCodeBlock, *gast.CodeBlock:
		w.tag("<pre>")
		lines := n.Lines()
		var code strings.Builder
		for i := 0; i < lines.Len(); i++ {
			seg := lines.At(i)
			code.Write(seg.Value(w.src))
		}
		w.textOut(strings.TrimRight(code.String(), "\n"))
		w.tag("</pre>")
		if w.plain {
			w.b.WriteString("\n")
		}
	case *gast.Blockquote:
		w.tag("<blockquote>")
		w.blocks(n)
		w.tag("</blockquote>")
	case *gast.List:
		t := "ul"
		if n.IsOrdered() {
			t = "ol"
		}
		w.tag("<" + t + ">")
		for li := n.FirstChild(); li != nil; li = li.NextSibling() {
			w.tag("<li>")
			first := true
			for c := li.FirstChild(); c != nil; c = c.NextSibling() {
				if !first && !isBlockElement(c) {
					w.b.WriteString("\n")
				}
				w.block(c)
				first = false
			}
			w.tag("</li>")
			if w.plain {
				w.b.WriteString("\n")
			}
		}
		w.tag("</" + t + ">")
	case *gast.HTMLBlock:
		lines := n.Lines()
		for i := 0; i < lines.Len(); i++ {
			seg := lines.At(i)
			w.textOut(string(seg.Value(w.src)))
		}
	default:
		w.blocks(n)
	}
}

func (w *htmlWriter) inlines(n gast.Node) {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		w.inline(c)
	}
}

func (w *htmlWriter) inline(n gast.Node) {
	switch n := n.(type) {
	case *gast.Text:
		w.textOut(decodeText(n.Segment.Value(w.src)))
		if n.SoftLineBreak() || n.HardLineBreak() {
			w.b.WriteString("\n")
		}
	case *gast.String:
		w.textOut(string(n.Value))
	case *gast.CodeSpan:
		w.tag("<code>")
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			if t, ok := c.(*gast.Text); ok {
				w.textOut(string(t.Segment.Value(w.src)))
			}
		}
		w.tag("</code>")
	case *gast.Emphasis:
		t := "em"
		if n.Level >= 2 {
			t = "strong"
		}
		w.tag("<" + t + ">")
		w.inlines(n)
		w.tag("</" + t + ">")
	case *east.Strikethrough:
		w.tag("<s>")
		w.inlines(n)
		w.tag("</s>")
	case *gast.Link:
		// Mentions and images are recognised by the destination as written; the href is the
		// destination with backslash escapes and character references resolved.
		if gid, ok := strings.CutPrefix(string(n.Destination), mentionScheme); ok {
			if w.plain {
				w.inlines(n)
			} else {
				w.b.WriteString(`<a data-asana-gid="` + html.EscapeString(gid) + `"/>`)
			}
			return
		}
		w.tag(`<a href="` + html.EscapeString(decodeText(n.Destination)) + `">`)
		w.inlines(n)
		w.tag("</a>")
	case *gast.AutoLink:
		u := string(n.URL(w.src))
		w.tag(`<a href="` + html.EscapeString(u) + `">`)
		w.textOut(u)
		w.tag("</a>")
	case *gast.Image:
		dest := string(n.Destination)
		if gid, ok := ImageGID(dest); ok {
			if !w.plain {
				w.b.WriteString(`<img data-asana-gid="` + html.EscapeString(gid) + `"/>`)
			}
			return
		}
		w.tag(`<a href="` + html.EscapeString(decodeText(n.Destination)) + `">`)
		w.inlines(n)
		w.tag("</a>")
	case *gast.RawHTML:
		segs := n.Segments
		for i := 0; i < segs.Len(); i++ {
			seg := segs.At(i)
			w.textOut(string(seg.Value(w.src)))
		}
	default:
		w.inlines(n)
	}
}

// textEscape matches a backslash escape ("\*") or a character reference ("&amp;", "&#32;").
var textEscape = regexp.MustCompile(`\\[!-/:-@\[-` + "`" + `{-~]|&(?:#[0-9]{1,7}|#[xX][0-9a-fA-F]{1,6}|[A-Za-z][A-Za-z0-9]{0,31});`)

// decodeText turns the raw source of a text segment into its text: backslash escapes and
// character references are resolved in one pass (as goldmark's renderer does), so "\&amp;"
// is the text "&amp;". Unknown references ("&foo;") stay as written.
func decodeText(src []byte) string {
	return textEscape.ReplaceAllStringFunc(string(src), func(m string) string {
		if m[0] == '\\' {
			return m[1:]
		}
		return html.UnescapeString(m)
	})
}

// cachedAttachment matches files downloaded by asana-tui (".../attachments/<gid>-name").
var cachedAttachment = regexp.MustCompile(`(?:^|/)attachments/(\d+)-[^/]*$`)

// ImageGID returns the attachment gid an editable image link refers to.
func ImageGID(dest string) (string, bool) {
	if strings.HasPrefix(dest, mentionScheme) {
		return "", false
	}
	if gid, ok := strings.CutPrefix(dest, assetScheme); ok {
		return gid, true
	}
	if m := cachedAttachment.FindStringSubmatch(strings.TrimPrefix(dest, "file://")); m != nil {
		return m[1], true
	}
	return "", false
}
