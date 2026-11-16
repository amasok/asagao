// Package richtext converts Asana rich text (html_notes / html_text) to Markdown.
//
// Asana's rich text is a small HTML subset: <body>, <strong>, <em>, <u>, <s>, <code>, <pre>,
// <a href>, <ul>/<ol>/<li>, <h1>, <h2>, <h3>, <hr>, <blockquote>, <img> and plain newlines in text.
// Images are emitted as an ImageToken line so the renderer can put the picture (or a
// placeholder) exactly where it appeared.
package richtext

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"unicode"

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
	// "<a data-asana-gid="1"/>" (a mention as FromMarkdown writes it, kept in the cache until
	// the next sync) is not self-closing in HTML: the text after it would become its text
	s = selfClosingA.ReplaceAllString(s, "<a$1></a>")
	doc, err := html.Parse(strings.NewReader(s))
	if err != nil {
		return s
	}
	moveEdgeSpaceOutOfMarks(doc)
	c := &converter{editable: editable, imageDest: imageDest}
	c.walk(doc)
	c.keepTrailingSpace(blockEnds)
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
	b         bytes.Buffer
	lists     []listState // nesting of ul/ol
	pre       int         // inside <pre>
	quote     int         // inside <blockquote>
	// lineStart is where the text of the current Markdown line starts in b: after the
	// newline and the structure written at the start of the line (blockquote prefixes, list
	// item markers). Keeping it as state makes atLineStart constant time (#23).
	lineStart int
}

type listState struct {
	ordered bool
	n       int
}

func (c *converter) write(s string) {
	c.b.WriteString(s)
	if strings.HasSuffix(s, "\n") {
		c.markLineStart()
	}
}

// markLineStart records that what has been written so far is line structure: the next
// text starts the line.
func (c *converter) markLineStart() { c.lineStart = c.b.Len() }

// endsWith reports whether the output ends with suffix.
func (c *converter) endsWith(suffix string) bool {
	return bytes.HasSuffix(c.b.Bytes(), []byte(suffix))
}

// newline starts a new line, keeping blockquote prefixes.
func (c *converter) newline() {
	c.write("\n")
	if c.quote > 0 {
		c.write(strings.Repeat("> ", c.quote))
		c.markLineStart()
	}
}

// keepTrailingSpace is called before a line break: Markdown drops spaces and tabs at the end
// of a line (and reads two spaces as a line break), so the last one of the text written on
// this line becomes a character reference, which Markdown keeps as text (#45). Lines with
// no text other than whitespace are left alone, and so is a line break in the text that
// ends a block (when follows reports that no text comes after it): there the break and the
// spaces both go, as Asana trims them, and keeping only the spaces would change the text
// once and then again on the next round trip. Where the block itself ends (blockEnds), the
// spaces are the end of its text and are kept (#48).
func (c *converter) keepTrailingSpace(follows func() bool) {
	if c.pre > 0 {
		return
	}
	b := c.b.Bytes()
	n := len(b)
	if n == c.lineStart || (b[n-1] != ' ' && b[n-1] != '\t') {
		return
	}
	if len(bytes.TrimRight(b[c.lineStart:], " \t")) == 0 || !follows() {
		return
	}
	ch := b[n-1]
	c.b.Truncate(n - 1)
	c.write(indentRef[ch])
}

// blockEnds is keepTrailingSpace's follows where a block ends right after the text.
func blockEnds() bool { return true }

// block makes sure the next content starts a new paragraph: after a blank line, on a line
// that starts with the blockquote prefixes of the current depth. Inside a quote the blank
// line is a quote line too (">"), so the quote goes on; when a quote ends, the blank line
// is at the depth outside it, which ends the quote (#51).
func (c *converter) block() {
	if !c.lineBlank(c.lineStartOf(c.b.Len())) {
		c.keepTrailingSpace(blockEnds)
		c.write("\n")
	}
	cur := c.lineStartOf(c.b.Len())
	if cur == 0 {
		c.setPrefix()
		return
	}
	// The blank line goes after the line above, at the depth of both it and the next line;
	// if the line above is already blank, it is rewritten so that quotes that have ended end.
	prev := c.lineStartOf(cur - 1)
	d := min(quoteDepth(c.b.Bytes()[prev:cur-1]), c.quote)
	if c.lineBlank(prev) {
		cur = prev
	}
	c.b.Truncate(cur)
	c.write(blankQuoteLine(d) + "\n")
	c.setPrefix()
}

// lineStartOf is where the line holding position i of the output starts.
func (c *converter) lineStartOf(i int) int {
	return bytes.LastIndexByte(c.b.Bytes()[:i], '\n') + 1
}

// lineBlank reports whether the line starting at i (up to the next newline or the end of
// the output) holds nothing but blockquote prefixes and whitespace.
func (c *converter) lineBlank(i int) bool {
	l := c.b.Bytes()[i:]
	if j := bytes.IndexByte(l, '\n'); j >= 0 {
		l = l[:j]
	}
	return len(bytes.Trim(l, "> \t")) == 0
}

// setPrefix starts the current line, which holds only blockquote prefixes and whitespace,
// again with the prefixes of the current quote depth.
func (c *converter) setPrefix() {
	c.b.Truncate(c.lineStartOf(c.b.Len()))
	c.write(strings.Repeat("> ", c.quote))
	c.markLineStart()
}

// quoteDepth counts the blockquote prefixes at the start of a line.
func quoteDepth(l []byte) int {
	d := 0
	for {
		l = bytes.TrimLeft(l, " ")
		if len(l) == 0 || l[0] != '>' {
			return d
		}
		d++
		l = l[1:]
	}
}

// blankQuoteLine is a blank line inside d blockquotes.
func blankQuoteLine(d int) string { return strings.TrimSuffix(strings.Repeat("> ", d), " ") }

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
			c.write(codeSpan(textOf(n)))
		}
	case atom.Pre:
		c.block()
		c.pre++
		c.codeBlock(strings.TrimRight(textOf(n), "\n"))
		c.pre--
		c.block()
	case atom.A:
		c.link(n)
	case atom.H1, atom.H2, atom.H3:
		c.block()
		c.write(strings.Repeat("#", headingLevel(n.DataAtom)) + " ")
		c.write(escape(strings.TrimSpace(strings.ReplaceAll(textOf(n), "\n", " ")), false))
		c.block()
	case atom.Hr:
		c.block()
		c.write("---")
		c.block()
	case atom.Br:
		c.keepTrailingSpace(func() bool { return c.contentFollows(n, true) })
		if !c.keepBlankLineInItem(func() bool { return c.contentFollows(n, false) }) {
			c.write("  ")
		}
		c.newline()
	case atom.Blockquote:
		c.block()
		c.quote++
		c.setPrefix()
		start := c.b.Len()
		c.children(n)
		if len(bytes.Trim(c.b.Bytes()[start:], "> \t\n")) == 0 {
			// nothing in the quote: a quote line alone would be taken for a blank line and
			// dropped, so write a space as a character reference (read back as an empty quote)
			c.b.Truncate(start)
			c.setPrefix()
			c.write("&#32;")
		}
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
	inList := len(c.lists) > 0
	if inList && strings.TrimSpace(t) == "" && betweenListTags(n) {
		return // whitespace between list tags
	}
	// Editable Markdown shows indentation as it is (FromMarkdown keeps it); inside list items
	// it stays a character reference, as indentation there is list structure.
	rawIndent := c.editable && !inList
	t = escapeText(t, c.atLineStart(), rawIndent)
	// Asana keeps line breaks as plain newlines: make them Markdown line breaks.
	lines := strings.Split(t, "\n")
	last := len(lines) - 1
	for i, l := range lines {
		if i > 0 {
			c.keepTrailingSpace(func() bool { return nonBlank(lines[i:]) || c.contentFollows(n, true) })
			switch {
			case c.keepBlankLineInItem(func() bool { return nonBlank(lines[i:]) || c.contentFollows(n, false) }):
			case strings.TrimSpace(lines[i-1]) != "" && l != "":
				// a line break inside a paragraph needs a hard break ("  "); blank lines don't
				c.write("  ")
			}
			c.newline()
		}
		if i == last && !rawIndent && l != "" && strings.TrimSpace(l) == "" && c.atLineStart() &&
			c.lineContentFollows(n) {
			// whitespace before inline content at the start of a line (in a list item, or
			// for display): Markdown would drop it as indentation
			l = indentRef[l[0]] + l[1:]
		}
		c.write(l)
	}
}

// betweenListTags reports whether whitespace-only text n is layout between list tags: a
// child of <ul>/<ol>, or a child of <li> with no inline content before or after it (an
// item of only whitespace, or next to a nested list). Whitespace next to inline content in
// an item is text: between inline elements ("<a>a</a> <a>b</a>", #51), before them, or at
// the end of the item's text (#48).
func betweenListTags(n *html.Node) bool {
	p := n.Parent
	if p == nil || p.Type != html.ElementNode {
		return false
	}
	switch p.DataAtom {
	case atom.Ul, atom.Ol:
		return true
	case atom.Li:
		isBlock := func(s *html.Node) bool {
			return s == nil || s.Type == html.ElementNode && blockAtoms[s.DataAtom]
		}
		return isBlock(n.PrevSibling) && isBlock(n.NextSibling)
	}
	return false
}

// keepBlankLineInItem is called before a line break in a list item: if the line is blank and
// text of the item follows (inlineFollows), the blank line would end the item and the rest
// would leave the list. A line of only a backslash is a line break instead (a hard break at
// its end), so the item goes on (#51). It reports whether it wrote one.
func (c *converter) keepBlankLineInItem(inlineFollows func() bool) bool {
	if len(c.lists) == 0 || c.pre > 0 || len(bytes.TrimLeft(c.b.Bytes()[c.lineStart:], " \t")) > 0 ||
		!inlineFollows() {
		return false
	}
	c.b.Truncate(c.lineStart)
	c.write(`\`)
	return true
}

// atLineStart reports whether the next text starts a line of Markdown: at the start of the
// output, after a newline, blockquote prefixes or a list item marker.
func (c *converter) atLineStart() bool { return c.b.Len() == c.lineStart }

// contentFollows reports whether something comes after n in the same block: text in a
// following sibling (of n, or of the inline elements around it), or, with blocks, a block
// element, before the block ends. Line breaks are skipped; images and mentions count as
// text. Only when the block ends after n are the spaces before a line break at n dropped
// (see keepTrailingSpace); a line break before a nested block is where FromMarkdown
// separates the text from the block, so the spaces there are the end of the text and are
// kept (#48).
func (c *converter) contentFollows(n *html.Node, blocks bool) bool {
	for ; n != nil; n = n.Parent {
		for sib := n.NextSibling; sib != nil; sib = sib.NextSibling {
			switch {
			case sib.Type == html.TextNode:
				if strings.TrimSpace(sib.Data) != "" {
					return true
				}
			case sib.Type != html.ElementNode, sib.DataAtom == atom.Br:
			case blockAtoms[sib.DataAtom]:
				return blocks
			case sib.DataAtom == atom.Img:
				return c.editable || blocks // displayed as a block of its own
			case strings.TrimSpace(textOf(sib)) != "" || attr(sib, "data-asana-gid") != "" || hasAsanaRef(sib):
				return true
			}
		}
		if p := n.Parent; p == nil || p.Type != html.ElementNode || blockAtoms[p.DataAtom] {
			return false
		}
	}
	return false
}

// lineContentFollows reports whether inline content comes after n on the same line: before
// a line break, a newline in text, or a block.
func (c *converter) lineContentFollows(n *html.Node) bool {
	for ; n != nil; n = n.Parent {
		for sib := n.NextSibling; sib != nil; sib = sib.NextSibling {
			switch {
			case sib.Type == html.TextNode:
				line, _, nl := strings.Cut(sib.Data, "\n")
				if strings.TrimSpace(line) != "" {
					return true
				}
				if nl {
					return false
				}
			case sib.Type != html.ElementNode:
			case sib.DataAtom == atom.Br, blockAtoms[sib.DataAtom]:
				return false
			case sib.DataAtom == atom.Img:
				return c.editable
			default:
				// an inline element: what it writes first
				t := textOf(sib)
				if line, _, nl := strings.Cut(t, "\n"); strings.TrimSpace(line) != "" ||
					attr(sib, "data-asana-gid") != "" || hasAsanaRef(sib) {
					return true
				} else if nl {
					return false
				}
			}
		}
		if p := n.Parent; p == nil || p.Type != html.ElementNode || blockAtoms[p.DataAtom] {
			return false
		}
	}
	return false
}

// blockAtoms are the elements that start a block of their own (see walk).
var blockAtoms = map[atom.Atom]bool{
	atom.Html: true, atom.Body: true, atom.P: true, atom.Div: true, atom.Pre: true,
	atom.H1: true, atom.H2: true, atom.H3: true, atom.Hr: true, atom.Blockquote: true,
	atom.Ul: true, atom.Ol: true, atom.Li: true,
}

// nonBlank reports whether one of lines has something other than spaces and tabs.
func nonBlank(lines []string) bool {
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			return true
		}
	}
	return false
}

func (c *converter) wrap(n *html.Node, mark string) {
	if strings.TrimSpace(textOf(n)) == "" && !hasAsanaRef(n) {
		c.children(n)
		return
	}
	c.write(mark)
	c.children(n)
	c.write(mark)
}

// markAtoms are the elements written as emphasis marks ("**", "*", "~~", see walk).
var markAtoms = map[atom.Atom]bool{
	atom.Strong: true, atom.B: true, atom.Em: true, atom.I: true,
	atom.S: true, atom.Strike: true, atom.Del: true,
}

// moveEdgeSpaceOutOfMarks moves whitespace (and line breaks) at the start and the end of
// emphasis out of it: "<strong>a </strong>b" becomes "<strong>a</strong> b". Markdown does
// not close "**" right after a space nor open it right before one, so "**a **b" would be
// read back as text with the marks in it (#49). Where the space is, before or after the
// mark, looks the same.
func moveEdgeSpaceOutOfMarks(n *html.Node) {
	for ch := n.FirstChild; ch != nil; {
		next := ch.NextSibling
		moveEdgeSpaceOutOfMarks(ch) // inner marks first: their spaces then are at our edges
		if ch.Type == html.ElementNode && markAtoms[ch.DataAtom] && ch.Parent != nil {
			for _, s := range edgeSpace(ch, true) {
				ch.Parent.InsertBefore(s, ch)
			}
			after := edgeSpace(ch, false)
			for i := len(after) - 1; i >= 0; i-- {
				ch.Parent.InsertBefore(after[i], next)
			}
		}
		ch = next
	}
}

// edgeSpace removes the whitespace text and line breaks at the start (or the end) of n,
// looking into elements that are written only as their children (<u>, <span>, marks), and
// returns them in document order.
func edgeSpace(n *html.Node, start bool) []*html.Node {
	var out []*html.Node
	for {
		ch := n.FirstChild
		if !start {
			ch = n.LastChild
		}
		switch {
		case ch == nil:
			return out
		case ch.Type == html.ElementNode && ch.DataAtom == atom.Br:
			n.RemoveChild(ch)
			out = appendEdge(out, ch, start)
			continue
		case ch.Type == html.ElementNode && transparentAtoms[ch.DataAtom]:
			inner := edgeSpace(ch, start)
			if start {
				out = append(out, inner...)
			} else {
				out = append(inner, out...)
			}
			if ch.FirstChild != nil {
				return out
			}
			n.RemoveChild(ch) // nothing left in it
			continue
		case ch.Type != html.TextNode:
			return out
		}
		var sp string
		if start {
			rest := strings.TrimLeftFunc(ch.Data, unicode.IsSpace)
			sp, ch.Data = ch.Data[:len(ch.Data)-len(rest)], rest
		} else {
			rest := strings.TrimRightFunc(ch.Data, unicode.IsSpace)
			sp, ch.Data = ch.Data[len(rest):], rest
		}
		if sp != "" {
			out = appendEdge(out, &html.Node{Type: html.TextNode, Data: sp}, start)
		}
		if ch.Data != "" {
			return out
		}
		n.RemoveChild(ch)
	}
}

// appendEdge adds a node taken from the start (appended) or the end (prepended) of an element.
func appendEdge(out []*html.Node, n *html.Node, start bool) []*html.Node {
	if start {
		return append(out, n)
	}
	return append([]*html.Node{n}, out...)
}

// transparentAtoms are inline elements written only as their children.
var transparentAtoms = map[atom.Atom]bool{
	atom.Strong: true, atom.B: true, atom.Em: true, atom.I: true,
	atom.S: true, atom.Strike: true, atom.Del: true, atom.U: true, atom.Span: true, atom.Font: true,
}

// selfClosingA matches an <a> tag written as self-closing ("<a data-asana-gid="1"/>").
var selfClosingA = regexp.MustCompile(`<a(\s[^<>]*?)\s*/>`)

func (c *converter) link(n *html.Node) {
	href := attr(n, "href")
	text := strings.TrimSpace(textOf(n))
	gid, typ := attr(n, "data-asana-gid"), attr(n, "data-asana-type")
	// A mention as Asana returns it has data-asana-type="user" and the name; as FromMarkdown
	// writes it, only the gid.
	mention := gid != "" && (typ == "user" || (typ == "" && href == ""))
	if mention && text == "" {
		text = "@" + gid // the name is not known until the next sync
	}
	switch {
	case c.editable && mention:
		c.write("[" + escape(text, false) + "](" + mentionScheme + gid + ")")
	case mention || typ == "user" || strings.HasPrefix(text, "@"):
		c.write("**" + escape(text, false) + "**") // mention
	case (href == "" || text == href) && autolinkSafe(text):
		c.write("<" + text + ">")
	case href == "":
		c.write(escape(text, false)) // not a link
	default:
		// "!" just before the link would make it an image (of an attachment, for a local path)
		if c.endsWith("!") {
			c.b.Truncate(c.b.Len() - 1)
			c.write(`\!`)
		}
		c.write("[" + escape(text, false) + "](" + linkDest(href) + ")")
	}
}

// autolinkSafe reports whether u can be written as an autolink (<u>): a URL with a scheme
// and nothing that would end the autolink early or make it raw HTML instead.
var autolinkRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]{1,31}:[^\x00-\x20<>\x7f]*$`)

func autolinkSafe(u string) bool { return autolinkRe.MatchString(u) }

// linkDest writes href as a Markdown link destination that reads back as the same href
// (FromMarkdown resolves backslash escapes and character references in it).
// Characters that would end the destination (")", ">", spaces, line breaks) or be read as
// syntax ("\\", "&", "<", "(") are escaped, so a crafted href cannot close the link and
// add Markdown of its own (e.g. a mention). Control characters are percent-encoded.
func linkDest(href string) string {
	var b strings.Builder
	b.WriteByte('<')
	for i := 0; i < len(href); i++ {
		switch ch := href[i]; {
		case ch < 0x20 || ch == 0x7f:
			fmt.Fprintf(&b, "%%%02X", ch)
		case strings.IndexByte(`\<>()&`+"`", ch) >= 0:
			b.WriteByte('\\')
			b.WriteByte(ch)
		default:
			b.WriteByte(ch)
		}
	}
	b.WriteByte('>')
	d := b.String()
	if strings.HasPrefix(d, "<"+assetScheme) {
		// an Asana link to "asana:..." is not an image or a mention: FromMarkdown checks the
		// destination as written, so "asana\:" keeps it a plain link
		d = "<asana\\:" + d[len("<"+assetScheme):]
	}
	if !strings.ContainsAny(href, " ") {
		// no spaces: the plain form reads better ("(" and ")" are escaped, so it stays closed)
		d = d[1 : len(d)-1]
	}
	return d
}

// codeSpan writes s as an inline code span. The backtick fence is longer than any run of
// backticks in s, so s cannot close it early; line breaks become spaces (a blank line
// would end the paragraph and leave the rest of s to be read as Markdown).
func codeSpan(s string) string {
	s = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(s)
	fence := strings.Repeat("`", longestRun(s, '`')+1)
	if strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") ||
		(strings.HasPrefix(s, " ") && strings.HasSuffix(s, " ") && strings.TrimSpace(s) != "") {
		s = " " + s + " " // Markdown strips one space on each side
	}
	return fence + s + fence
}

// codeBlock writes s as a fenced code block. The fence is longer than any run of backticks
// in s, so a line of "```" in s does not close it. Inside a blockquote every line gets the
// quote prefixes, so the code stays in the quote (#51).
func (c *converter) codeBlock(s string) {
	fence := strings.Repeat("`", max(3, longestRun(s, '`')+1))
	c.write(fence)
	for _, l := range strings.Split(s, "\n") {
		c.newline()
		c.write(l)
	}
	c.newline()
	c.write(fence)
}

// longestRun is the length of the longest run of ch in s.
func longestRun(s string, ch byte) int {
	longest, n := 0, 0
	for i := 0; i < len(s); i++ {
		if s[i] == ch {
			n++
			longest = max(longest, n)
		} else {
			n = 0
		}
	}
	return longest
}

func (c *converter) listItem(n *html.Node) {
	depth := len(c.lists)
	if depth == 0 {
		c.lists = append(c.lists, listState{})
		depth = 1
	}
	l := &c.lists[depth-1]
	l.n++
	if c.lineBlank(c.lineStartOf(c.b.Len())) {
		c.setPrefix()
	} else {
		c.keepTrailingSpace(blockEnds)
		c.newline()
	}
	c.write(strings.Repeat("   ", depth-1))
	if l.ordered {
		c.write(fmt.Sprintf("%d. ", l.n))
	} else {
		c.write("- ")
	}
	c.markLineStart()
	trimLeadingBreaks(n)
	ch := n.FirstChild
	if !itemHasText(n) {
		// An empty marker ("-", "1.") after a line of text would be read as a setext heading
		// underline, or as text: write a space as a character reference after it, which
		// FromMarkdown reads back as an empty item (#52). What comes before a nested list
		// here is whitespace, line breaks or empty elements, which Markdown cannot keep anyway.
		c.write("&#32;")
		for ch != nil && !(ch.Type == html.ElementNode && (ch.DataAtom == atom.Ul || ch.DataAtom == atom.Ol)) {
			ch = ch.NextSibling
		}
	}
	for ; ch != nil; ch = ch.NextSibling {
		c.walk(ch)
	}
}

// trimLeadingBreaks removes line breaks (and the whitespace before them) at the start of
// list item n. Markdown cannot keep them: a marker followed by a line break starts an item
// with a blank line, and the text on the next line would leave the list. Spaces after the
// last break stay (they are written as a character reference).
func trimLeadingBreaks(n *html.Node) {
	for breakBeforeContent(n) == 1 {
		removeFirstLeaf(n)
	}
}

// breakBeforeContent scans the start of n (through inline elements written as their
// children): 1 if a line break comes before any content, -1 if content comes first, 0 if n
// holds only whitespace.
func breakBeforeContent(n *html.Node) int {
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		switch {
		case ch.Type == html.TextNode:
			for _, r := range ch.Data {
				switch {
				case r == '\n' || r == '\r':
					return 1
				case !unicode.IsSpace(r):
					return -1
				}
			}
		case ch.Type != html.ElementNode:
		case ch.DataAtom == atom.Br:
			return 1
		case transparentAtoms[ch.DataAtom]:
			if r := breakBeforeContent(ch); r != 0 {
				return r
			}
		default:
			return -1
		}
	}
	return 0
}

// removeFirstLeaf removes the first piece of whitespace at the start of n: a line break, an
// empty element, or text up to and including its first newline.
func removeFirstLeaf(n *html.Node) {
	ch := n.FirstChild
	for ch != nil && ch.Type != html.TextNode && ch.Type != html.ElementNode {
		ch = ch.NextSibling
	}
	switch {
	case ch == nil:
	case ch.Type == html.TextNode:
		if i := strings.IndexAny(ch.Data, "\n\r"); i >= 0 && strings.TrimSpace(ch.Data[:i]) == "" {
			ch.Data = ch.Data[i+1:]
		} else {
			ch.Data = ""
		}
		if ch.Data == "" {
			n.RemoveChild(ch)
		}
	case transparentAtoms[ch.DataAtom] && breakBeforeContent(ch) != 0:
		removeFirstLeaf(ch)
	default: // <br>, or an element holding only whitespace
		n.RemoveChild(ch)
	}
}

// itemHasText reports whether list item n has something to write on its first line: text,
// an image or a mention before its first nested list.
func itemHasText(n *html.Node) bool {
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		switch {
		case ch.Type == html.TextNode:
			if strings.TrimSpace(ch.Data) != "" {
				return true
			}
		case ch.Type != html.ElementNode:
		case ch.DataAtom == atom.Ul || ch.DataAtom == atom.Ol:
			return false
		case ch.DataAtom == atom.Img || attr(ch, "data-asana-gid") != "" ||
			strings.TrimSpace(textOf(ch)) != "" || hasAsanaRef(ch):
			return true
		}
	}
	return false
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
			c.write("[画像: " + escape(alt, false) + "]")
		}
		return
	}
	if c.editable {
		// in place, as FromMarkdown reads it back (a paragraph break would be added text)
		c.write("![画像](" + EditableImageDest(gid, c.imageDest) + ")")
		return
	}
	c.block()
	c.write(ImageToken(gid))
	c.block()
}

// hasAsanaRef reports whether n contains a mention or an image (which have no text when
// FromMarkdown wrote them).
func hasAsanaRef(n *html.Node) bool {
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		if ch.Type == html.ElementNode && attr(ch, "data-asana-gid") != "" || hasAsanaRef(ch) {
			return true
		}
	}
	return false
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

// headingLevel is 1 for <h1>, 2 for <h2> and 3 for <h3>. (Atoms are not consecutive, so
// the level cannot be computed from the atom values.)
func headingLevel(a atom.Atom) int {
	switch a {
	case atom.H2:
		return 2
	case atom.H3:
		return 3
	}
	return 1
}

// "~" is escaped too: goldmark's strikethrough accepts a single tilde as well as "~~".
var mdSpecial = strings.NewReplacer(`\`, `\\`, "*", `\*`, "_", `\_`, "`", "\\`", "[", `\[`, "]", `\]`, "<", `\<`, "~", `\~`)

// orderedMarker matches an ordered list marker at the start of a line ("1. ", "12) ").
var orderedMarker = regexp.MustCompile(`^([0-9]{1,9})([.)])(?:[ \t]|$)`)

// escape escapes Markdown syntax in text so that it reads back as the same text. lineStart
// tells whether the text begins at the start of a line (otherwise only the lines after its
// first newline are checked for block syntax and indentation).
func escape(s string, lineStart bool) string { return escapeText(s, lineStart, false) }

// escapeText is escape. With rawIndent, indentation is written as it is (for editable
// Markdown, where FromMarkdown keeps it, see keepIndentation); otherwise its first
// character is a character reference, so any Markdown reader keeps it.
func escapeText(s string, lineStart, rawIndent bool) string {
	s = mdSpecial.Replace(s)
	s = entityLike.ReplaceAllString(s, `\$0`) // "&copy;" in the text stays as written
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if i == 0 && !lineStart {
			continue
		}
		trimmed := strings.TrimLeft(l, " \t") // indentation: spaces and tabs
		indent := l[:len(l)-len(trimmed)]
		if indent != "" && trimmed != "" {
			// Indentation would be dropped (or make a code block at the start of a paragraph).
			// Writing its first character as a character reference keeps it exactly, and the
			// line no longer starts with block syntax.
			if rawIndent {
				lines[i] = indent + escapeBlock(trimmed)
			} else {
				lines[i] = indentRef[indent[0]] + indent[1:] + trimmed
			}
			continue
		}
		if trimmed != "" {
			lines[i] = escapeBlock(trimmed)
		}
	}
	return strings.Join(lines, "\n")
}

// escapeBlock escapes block syntax at the start of a line: "#", ">", "- ", "+ ", "1. ", "1) "
// would become headings, quotes or lists, and a line of only "-" or "=" would turn the line
// above into a heading. (Lines that do not start with block syntax are returned as they are.)
func escapeBlock(line string) string {
	switch {
	case strings.HasPrefix(line, "#"), strings.HasPrefix(line, ">"),
		listBullet(line, '-'), listBullet(line, '+'),
		onlyOf(line, '-'), onlyOf(line, '='):
		return `\` + line
	}
	if m := orderedMarker.FindStringSubmatchIndex(line); m != nil {
		return line[:m[4]] + `\` + line[m[4]:]
	}
	return line
}

// entityLike matches what Markdown would read as a character reference ("&amp;", "&#32;").
var entityLike = regexp.MustCompile(`&(?:#[0-9]{1,7}|#[xX][0-9a-fA-F]{1,6}|[A-Za-z][A-Za-z0-9]{0,31});`)

// indentRef writes the first indentation character as a character reference.
var indentRef = map[byte]string{' ': "&#32;", '\t': "&#9;"}

// listBullet reports whether line starts with a bullet list marker ("- ", or "-" alone).
func listBullet(line string, mark byte) bool {
	return len(line) > 0 && line[0] == mark && (len(line) == 1 || line[1] == ' ' || line[1] == '\t')
}

// onlyOf reports whether line consists of mark characters (and spaces or tabs) only.
func onlyOf(line string, mark rune) bool {
	if line == "" {
		return false
	}
	for _, r := range line {
		if r != mark && r != ' ' && r != '\t' {
			return false
		}
	}
	return true
}

var manyBlank = regexp.MustCompile(`\n{3,}`)

func tidy(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) == "" {
			lines[i] = ""
		}
	}
	// blank lines are empty now, so trimming newlines on the left keeps the indentation of
	// the first line (editable Markdown writes it as it is)
	s = manyBlank.ReplaceAllString(strings.Join(lines, "\n"), "\n\n")
	return strings.TrimRight(strings.TrimLeft(s, "\n"), " \t\n")
}

// FromPlain converts plain text (which may contain image tokens) to Markdown, keeping line
// breaks and escaping Markdown syntax.
func FromPlain(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return ToMarkdown("<body>" + html.EscapeString(text) + "</body>")
}

// FromPlainEditable is FromPlain for editing: indentation is written as it is (FromMarkdown
// keeps it) instead of as character references. Image tokens are kept.
func FromPlainEditable(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return ToEditableMarkdown("<body>" + html.EscapeString(text) + "</body>")
}

// EditableImageDest is the link target of an image in editable Markdown. Targets with
// spaces or brackets are written as <...> (CommonMark), otherwise they would end the link.
func EditableImageDest(gid string, imageDest func(string) string) string {
	if imageDest != nil {
		if d := imageDest(gid); d != "" {
			if strings.ContainsAny(d, " \t()<>") {
				return "<" + d + ">"
			}
			return d
		}
	}
	return assetScheme + gid
}
