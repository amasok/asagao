// Package richtext converts Asana rich text (html_notes / html_text) to Markdown.
//
// Asana's rich text is a small HTML subset: <body>, <strong>, <em>, <u>, <s>, <code>, <pre>,
// <a href>, <ul>/<ol>/<li>, <h1>, <h2>, <hr>, <blockquote>, <img> and plain newlines in text.
// Images are emitted as an ImageToken line so the renderer can put the picture (or a
// placeholder) exactly where it appeared.
package richtext

import (
	"fmt"
	"regexp"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// ImageToken marks where an image appeared; %s is the attachment gid.
const imageTokenFormat = "⟦img:%s⟧"

// ImageTokenRe matches image tokens in rendered output.
var ImageTokenRe = regexp.MustCompile(`⟦img:(\d+)⟧`)

func ImageToken(gid string) string { return fmt.Sprintf(imageTokenFormat, gid) }

var assetID = regexp.MustCompile(`asset_id=(\d+)`)

// ToMarkdown converts Asana rich text HTML to Markdown for display. Images become image
// tokens and mentions bold text. It returns "" if s is empty.
func ToMarkdown(s string) string { return convert(s, false, nil) }

// ToEditableMarkdown converts Asana rich text to Markdown for editing: images and mentions
// are kept as ![画像](asana:GID) and [@Name](asana:user:GID) so FromMarkdown can restore them.
func ToEditableMarkdown(s string) string { return convert(s, true, nil) }

// ToEditableMarkdownWith is ToEditableMarkdown with image links chosen by imageDest (e.g. a
// local file path so editor plugins can show the image). FromMarkdown recognises cached
// attachment paths (".../attachments/<gid>-name") as well as asana:<gid>.
func ToEditableMarkdownWith(s string, imageDest func(gid string) string) string {
	return convert(s, true, imageDest)
}

func convert(s string, editable bool, imageDest func(string) string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	doc, err := html.Parse(strings.NewReader(s))
	if err != nil {
		return s
	}
	c := &converter{editable: editable, imageDest: imageDest}
	c.walk(doc)
	return tidy(c.b.String())
}

// Images returns the gids of images in the rich text, in order.
func Images(s string) []string {
	var gids []string
	for _, m := range ImageTokenRe.FindAllStringSubmatch(ToMarkdown(s), -1) {
		gids = append(gids, m[1])
	}
	return gids
}

type converter struct {
	editable  bool
	imageDest func(gid string) string
	b         strings.Builder
	lists     []listState // nesting of ul/ol
	pre       int         // inside <pre>
	quote     int         // inside <blockquote>
}

type listState struct {
	ordered bool
	n       int
}

func (c *converter) write(s string) { c.b.WriteString(s) }

// newline starts a new line, keeping blockquote prefixes.
func (c *converter) newline() {
	c.write("\n")
	if c.quote > 0 {
		c.write(strings.Repeat("> ", c.quote))
	}
}

// block makes sure the next content starts a new paragraph.
func (c *converter) block() {
	s := c.b.String()
	if s == "" || strings.HasSuffix(s, "\n\n") {
		return
	}
	if strings.HasSuffix(s, "\n") {
		c.write("\n")
		return
	}
	c.write("\n\n")
}

func (c *converter) children(n *html.Node) {
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		c.walk(ch)
	}
}

func (c *converter) walk(n *html.Node) {
	switch n.Type {
	case html.TextNode:
		c.text(n)
		return
	case html.ElementNode:
	default:
		c.children(n)
		return
	}
	switch n.DataAtom {
	case atom.Strong, atom.B:
		c.wrap(n, "**")
	case atom.Em, atom.I:
		c.wrap(n, "*")
	case atom.S, atom.Strike, atom.Del:
		c.wrap(n, "~~")
	case atom.U:
		c.children(n) // Markdown has no underline
	case atom.Code:
		if c.pre > 0 {
			c.children(n)
		} else {
			c.write("`" + textOf(n) + "`")
		}
	case atom.Pre:
		c.block()
		c.pre++
		c.write("```\n" + strings.TrimRight(textOf(n), "\n") + "\n```")
		c.pre--
		c.block()
	case atom.A:
		c.link(n)
	case atom.H1, atom.H2, atom.H3:
		c.block()
		c.write(strings.Repeat("#", int(n.DataAtom-atom.H1)/int(atom.H2-atom.H1)+1) + " ")
		c.write(strings.TrimSpace(strings.ReplaceAll(textOf(n), "\n", " ")))
		c.block()
	case atom.Hr:
		c.block()
		c.write("---")
		c.block()
	case atom.Br:
		c.write("  ")
		c.newline()
	case atom.Blockquote:
		c.block()
		c.quote++
		c.write(strings.Repeat("> ", c.quote))
		c.children(n)
		c.quote--
		c.block()
	case atom.Ul, atom.Ol:
		if len(c.lists) == 0 {
			c.block()
		}
		c.lists = append(c.lists, listState{ordered: n.DataAtom == atom.Ol})
		c.children(n)
		c.lists = c.lists[:len(c.lists)-1]
		if len(c.lists) == 0 {
			c.block()
		}
	case atom.Li:
		c.listItem(n)
	case atom.Img:
		c.image(n)
	default: // html, head, body, div, p, span, …
		if n.DataAtom == atom.P || n.DataAtom == atom.Div {
			c.block()
			c.children(n)
			c.block()
			return
		}
		c.children(n)
	}
}

func (c *converter) text(n *html.Node) {
	t := n.Data
	if c.pre > 0 {
		c.write(t)
		return
	}
	if len(c.lists) > 0 && strings.TrimSpace(t) == "" {
		return // whitespace between list tags
	}
	t = escape(t)
	// Asana keeps line breaks as plain newlines: make them Markdown line breaks.
	lines := strings.Split(t, "\n")
	for i, l := range lines {
		if i > 0 {
			// a line break inside a paragraph needs a hard break ("  "); blank lines don't
			if strings.TrimSpace(lines[i-1]) != "" && l != "" {
				c.write("  ")
			}
			c.newline()
		}
		c.write(l)
	}
}

func (c *converter) wrap(n *html.Node, mark string) {
	inner := textOf(n)
	if strings.TrimSpace(inner) == "" {
		c.children(n)
		return
	}
	c.write(mark)
	c.children(n)
	c.write(mark)
}

func (c *converter) link(n *html.Node) {
	href := attr(n, "href")
	text := strings.TrimSpace(textOf(n))
	switch {
	case c.editable && attr(n, "data-asana-type") == "user" && attr(n, "data-asana-gid") != "":
		c.write("[" + escape(text) + "](" + mentionScheme + attr(n, "data-asana-gid") + ")")
	case attr(n, "data-asana-type") == "user" || strings.HasPrefix(text, "@"):
		c.write("**" + escape(text) + "**") // mention
	case href == "" || text == href:
		if href == "" {
			href = text
		}
		c.write("<" + href + ">")
	default:
		c.write("[" + escape(text) + "](" + href + ")")
	}
}

func (c *converter) listItem(n *html.Node) {
	depth := len(c.lists)
	if depth == 0 {
		c.lists = append(c.lists, listState{})
		depth = 1
	}
	l := &c.lists[depth-1]
	l.n++
	if s := c.b.String(); s != "" && !strings.HasSuffix(s, "\n") {
		c.newline()
	}
	c.write(strings.Repeat("   ", depth-1))
	if l.ordered {
		c.write(fmt.Sprintf("%d. ", l.n))
	} else {
		c.write("- ")
	}
	c.children(n)
}

func (c *converter) image(n *html.Node) {
	gid := attr(n, "data-asana-gid")
	if gid == "" {
		if m := assetID.FindStringSubmatch(attr(n, "src")); m != nil {
			gid = m[1]
		}
	}
	if gid == "" {
		if alt := attr(n, "alt"); alt != "" {
			c.write("[画像: " + escape(alt) + "]")
		}
		return
	}
	c.block()
	if c.editable {
		c.write("![画像](" + EditableImageDest(gid, c.imageDest) + ")")
	} else {
		c.write(ImageToken(gid))
	}
	c.block()
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func textOf(n *html.Node) string {
	var b strings.Builder
	var rec func(*html.Node)
	rec = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			rec(ch)
		}
	}
	rec(n)
	return b.String()
}

var mdSpecial = strings.NewReplacer(`\`, `\\`, "*", `\*`, "_", `\_`, "`", "\\`", "[", `\[`, "]", `\]`, "<", `\<`)

func escape(s string) string {
	s = mdSpecial.Replace(s)
	// "#", "-", "+", "1." at the start of a line would become headings or lists.
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		trimmed := strings.TrimLeft(l, " ")
		if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "+ ") || strings.HasPrefix(trimmed, ">") {
			lines[i] = strings.Repeat(" ", len(l)-len(trimmed)) + `\` + trimmed
		}
	}
	return strings.Join(lines, "\n")
}

var manyBlank = regexp.MustCompile(`\n{3,}`)

func tidy(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) == "" {
			lines[i] = ""
		}
	}
	return strings.TrimSpace(manyBlank.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}

// FromPlain converts plain text (which may contain image tokens) to Markdown, keeping line
// breaks and escaping Markdown syntax.
func FromPlain(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return ToMarkdown("<body>" + html.EscapeString(text) + "</body>")
}

// EditableImageDest is the link target of an image in editable Markdown.
func EditableImageDest(gid string, imageDest func(string) string) string {
	if imageDest != nil {
		if d := imageDest(gid); d != "" {
			return d
		}
	}
	return assetScheme + gid
}
