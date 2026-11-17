package richtext

import (
	"bytes"
	"html"
	"regexp"
	"strconv"
	"strings"

	"github.com/yuin/goldmark"
	gast "github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// Editable Markdown keeps Asana-specific references in a form that survives editing:
//
//	![画像](asana:1220507679870013)   an image attached to the task
//	[@Hanako](asana:user:123)          a mention
const (
	assetScheme   = "asana:"
	mentionScheme = "asana:user:"
)

var md = goldmark.New(
	goldmark.WithExtensions(extension.Strikethrough, extension.Linkify),
	goldmark.WithParserOptions(parser.WithBlockParsers(
		// ahead of the default thematic break parser (200), which it wraps
		util.Prioritized(ruleSource{parser.NewThematicBreakParser()}, 199)),
		parser.WithInlineParsers(util.Prioritized(escapedSpaceParser{}, 100),
			util.Prioritized(spaceRefParser{}, 100))),
)

// escapedSpaceParser reads "\ " (an escaped space) right next to a delimiter as nothing,
// as goldmark's CJK extension renders it: ToEditableMarkdown writes it there where emphasis
// would not open or close without a space (#58), after closing delimiters ("*", "~", "`",
// a link's ")") or before opening ones ("*", "~", "`", "[", "![", and "&" of a reference
// after a URL). It is an inline node of its own, so what follows it is read as what
// follows the delimiter is (Linkify starts a link there, as it does after "**"), whether or
// not the space is there. Elsewhere "\ " is text as typed ("cd My\ Documents").
type escapedSpaceParser struct{}

func (escapedSpaceParser) Trigger() []byte { return []byte{'\\'} }

func (escapedSpaceParser) Parse(_ gast.Node, block text.Reader, _ parser.Context) gast.Node {
	line, _ := block.PeekLine()
	if len(line) < 2 || line[1] != ' ' {
		return nil
	}
	if b := block.PrecendingCharacter(); !strings.ContainsRune("*~`)", b) &&
		(len(line) < 3 || strings.IndexByte("*~`[!&", line[2]) < 0) {
		return nil
	}
	block.Advance(2)
	return gast.NewString(nil)
}

// spaceRefParser reads "&#32;" and "&#9;" (indentation and spaces at the end of a line,
// as ToEditableMarkdown and keepIndentation write them) as an inline node of their own, so
// that what follows them is read as what follows a space is: Linkify starts a link there
// (#77; "&#32;www.x.jp" was text, " www.x.jp" a link).
type spaceRefParser struct{}

func (spaceRefParser) Trigger() []byte { return []byte{'&'} }

func (spaceRefParser) Parse(_ gast.Node, block text.Reader, _ parser.Context) gast.Node {
	line, _ := block.PeekLine()
	for _, r := range spaceRefs {
		if bytes.HasPrefix(line, r.ref) {
			block.Advance(len(r.ref))
			return gast.NewString(r.text)
		}
	}
	return nil
}

var spaceRefs = []struct{ ref, text []byte }{{[]byte("&#32;"), []byte(" ")}, {[]byte("&#9;"), []byte("\t")}}

// ruleAttr is the attribute a rule (thematic break) keeps its line in, as written ("***").
var ruleAttr = []byte("asagao-rule")

// ruleSource is a thematic break parser that keeps the line of the rule (see ruleText).
type ruleSource struct{ parser.BlockParser }

func (p ruleSource) Open(parent gast.Node, reader text.Reader, pc parser.Context) (gast.Node, parser.State) {
	line, _ := reader.PeekLine()
	src := strings.TrimSpace(string(line))
	n, st := p.BlockParser.Open(parent, reader, pc)
	if n != nil {
		n.SetAttribute(ruleAttr, src)
	}
	return n, st
}

// fromMarkdown converts Markdown to Asana rich text for target t (the HTML subset accepted
// in html_notes / html_text), wrapped in <body>.
func fromMarkdown(src string, t Target) string {
	// (Markdown written as text, see asText, is not converted again as an edit would read
	// it: a link in it would be found then, but a URL there can be cut short (#77), and the
	// user did not type a link. Text stays as typed.)
	b := []byte(keepIndentation(normalizeNewlines(src)))
	doc := md.Parser().Parse(text.NewReader(b))
	w := &htmlWriter{src: b, target: t}
	w.blocks(doc)
	return "<body>" + strings.TrimRight(w.b.String(), "\n") + "</body>"
}

// plainFromMarkdown returns the text of Markdown without markup, as fromMarkdown writes it
// for target t (for Asana's plain notes).
func plainFromMarkdown(src string, t Target) string {
	b := []byte(keepIndentation(normalizeNewlines(src)))
	doc := md.Parser().Parse(text.NewReader(b))
	w := &htmlWriter{src: b, plain: true, target: t}
	w.blocks(doc)
	// keep the indentation of the first line (see keepIndentation)
	return strings.TrimLeft(strings.TrimRight(w.b.String(), " \t\n"), "\n")
}

// normalizeNewlines makes line breaks typed as CRLF or CR (an editor on Windows, a paste)
// "\n", as Asana keeps them: Markdown reads them as line breaks too, but code and raw HTML
// would keep the "\r" as text (#72).
func normalizeNewlines(s string) string {
	return strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(s)
}

type htmlWriter struct {
	b      strings.Builder
	src    []byte
	plain  bool
	target Target
	bold   int  // open <strong>s written for a heading (see heading): no <strong> inside
	quote  int  // open <blockquote>s: a list here is written as text (see asText)
	item   int  // open <li>s: a quote, heading or code block here is written as text
	text   bool // writing Markdown blocks as text (see markdownText)
	// tail is what comes after a nested list in the item holding it, which goes into the
	// list's last item (see itemBlocks)
	tail   []gast.Node
	inHead bool // writing the text of a heading element: one line (#64)
}

// sub returns a writer for a part of the output that is put together afterwards.
func (w *htmlWriter) sub() *htmlWriter {
	return &htmlWriter{src: w.src, plain: w.plain, target: w.target, bold: w.bold, quote: w.quote, item: w.item, text: true}
}

// esc is s as it is written to the output (escaped, unless the output is plain text).
func (w *htmlWriter) esc(s string) string {
	if w.plain {
		return s
	}
	return html.EscapeString(s)
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

// asText reports whether block n, a child at the writer's position, is written as text
// rather than as an element: Asana's rich text does not let lists nest inside a quote, nor
// a quote, heading or code block inside a list item, and rejects such html_text with 400
// (#60). The Markdown for it is kept as the text of the quote or item instead, so that what
// the user wrote stays, around it the quote or the list (its items and numbers) stay as
// they are, and what is sent is accepted.
func (w *htmlWriter) asText(n gast.Node) bool {
	switch n.Kind() {
	case gast.KindList:
		return w.quote > 0
	case gast.KindBlockquote, gast.KindHeading, gast.KindFencedCodeBlock, gast.KindCodeBlock,
		gast.KindThematicBreak:
		// (a rule too: the documentation does not say an item takes <hr/>, and editable
		// Markdown would write it after the list, #80)
		return w.item > 0
	}
	return false
}

// demoted returns block n written as text (see asText and markdownText).
func (w *htmlWriter) demoted(n gast.Node) string {
	sub := w.sub()
	sub.markdownText(n)
	out := sub.b.String()
	if w.quote > 0 {
		// a quote keeps one blank line between its lines, not more
		out = blankLines.ReplaceAllString(out, "\n\n")
	}
	return out
}

// elementBlock reports whether n is written as a block element (which ends its line).
func (w *htmlWriter) elementBlock(n gast.Node) bool { return w.isBlockElement(n) && !w.asText(n) }

// isBlockElement reports whether n is a block element in the target. A heading or a rule is
// one only where Asana accepts <h1>, <h2> and <hr/>; elsewhere it is written as a paragraph.
func (w *htmlWriter) isBlockElement(n gast.Node) bool {
	switch n.Kind() {
	case gast.KindList, gast.KindFencedCodeBlock, gast.KindCodeBlock, gast.KindBlockquote:
		return true
	case gast.KindHeading, gast.KindThematicBreak:
		return w.target.layout()
	}
	return false
}

// layoutBlock reports whether n is a heading or a rule.
func layoutBlock(n gast.Node) bool {
	return n.Kind() == gast.KindHeading || n.Kind() == gast.KindThematicBreak
}

// blocks writes the block children of n. Paragraphs are separated by a blank line, as
// Asana stores them; block elements start on their own line.
func (w *htmlWriter) blocks(n gast.Node) {
	var prev gast.Node
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		if n.Kind() == gast.KindBlockquote && w.blankText(c) {
			continue // "> &#32;": an empty quote
		}
		if w.emptyHeading(c) {
			continue // "#" where it is a paragraph: nothing to write, no blank lines either
		}
		if c.Kind() == gast.KindLinkReferenceDefinition {
			continue // "[ref]: url" writes nothing, so no line break before it either (#66)
		}
		if prev != nil {
			switch {
			case w.elementBlock(prev):
				// a block element already ends its line
			case w.isBlockElement(prev), w.isBlockElement(c):
				w.b.WriteString("\n")
			case (layoutBlock(prev) || layoutBlock(c)) && !c.HasBlankPreviousLines():
				// a heading or a rule written as a paragraph (see Target) is apart from
				// the text around it as written: a blank line only where there was one
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
	if w.asText(n) {
		w.b.WriteString(w.demoted(n))
		return
	}
	switch n := n.(type) {
	case *gast.Paragraph, *gast.TextBlock:
		w.inlines(n)
	case *gast.Heading:
		if !w.target.layout() {
			w.heading(n)
			return
		}
		t := "h2"
		if n.Level == 1 {
			t = "h1"
		}
		w.tag("<" + t + ">")
		w.inHead = true
		w.inlines(n)
		w.inHead = false
		w.tag("</" + t + ">")
		if w.plain {
			w.b.WriteString("\n")
		}
	case *gast.ThematicBreak:
		if !w.target.layout() {
			w.textOut(w.ruleText(n))
			return
		}
		w.tag("<hr/>")
	case *gast.FencedCodeBlock, *gast.CodeBlock:
		open := "<pre>"
		if f, ok := n.(*gast.FencedCodeBlock); ok {
			// the language, as Asana takes it ("You can specify a language with
			// data-language", https://developers.asana.com/docs/rich-text, #65)
			if lang := codeLanguage(string(f.Language(w.src))); lang != "" {
				open = `<pre data-language="` + lang + `">`
			}
		}
		w.tag(open)
		w.textOut(w.codeText(n))
		w.tag("</pre>")
		if w.plain {
			w.b.WriteString("\n")
		}
	case *gast.Blockquote:
		w.tag("<blockquote>")
		w.quote++
		w.blocks(n)
		w.quote--
		w.tag("</blockquote>")
		if w.plain && !strings.HasSuffix(w.b.String(), "\n") {
			w.b.WriteString("\n") // ends its line, as </blockquote> does (#72)
		}
	case *gast.List:
		t := "ul"
		if n.IsOrdered() {
			t = "ol"
		}
		tail := w.tail
		w.tail = nil
		w.tag("<" + t + ">")
		w.item++
		for li := n.FirstChild(); li != nil; li = li.NextSibling() {
			w.tag("<li>")
			var cs []gast.Node
			for c := li.FirstChild(); c != nil; c = c.NextSibling() {
				cs = append(cs, c)
			}
			if li.NextSibling() == nil {
				cs = append(cs, tail...)
			}
			w.itemBlocks(cs)
			w.tag("</li>")
			if w.plain {
				w.b.WriteString("\n")
			}
		}
		w.item--
		w.tag("</" + t + ">")
	case *gast.HTMLBlock:
		// its lines as text, without the line break of the last one: a block ends where the
		// next starts (as a paragraph does), and a quote or an item would keep it once (#72)
		var raw strings.Builder
		lines := n.Lines()
		for i := 0; i < lines.Len(); i++ {
			seg := lines.At(i)
			raw.Write(seg.Value(w.src))
		}
		w.textOut(strings.TrimSuffix(raw.String(), "\n"))
	default:
		w.blocks(n)
	}
}

// itemBlocks writes the blocks of a list item. What comes after a nested list in it goes
// into the list's last item, as a new line (#59): editable Markdown cannot write text after
// a nested list (Markdown reads it as the last item going on), so that is where an edit
// puts it, and sending it so makes the edit give back what was sent.
func (w *htmlWriter) itemBlocks(cs []gast.Node) {
	first := true
	for i := 0; i < len(cs); i++ {
		c := cs[i]
		if w.blankText(c) || c.Kind() == gast.KindLinkReferenceDefinition {
			continue // "- &#32;": an empty item (#52); "[ref]: url" writes nothing (#66)
		}
		if w.asText(c) {
			out := w.demoted(c)
			if out == "" {
				continue // an empty code block: nothing to keep
			}
			if !first {
				w.b.WriteString("\n")
			}
			w.b.WriteString(out)
			first = false
			continue
		}
		if !first && !w.elementBlock(c) {
			w.b.WriteString("\n")
		} else if !first && w.plain && !strings.HasSuffix(w.b.String(), "\n") {
			w.b.WriteString("\n") // a nested list starts its line: no tag ends the text before it
		}
		if c.Kind() == gast.KindList {
			j := i + 1
			for j < len(cs) && cs[j].Kind() != gast.KindList {
				j++
			}
			w.tail = cs[i+1 : j]
			w.block(c)
			w.tail = nil
			i = j - 1
		} else {
			w.block(c)
		}
		first = false
	}
}

// markdownText writes block n as the Markdown it stands for, as text: list markers ("- ",
// "1. ", with the numbers as typed), quote markers ("> "), headings ("# h", or "h" over
// "---") and rules are text, as typed, and the blocks inside are text too (inline markup
// stays). A code block becomes inline code, line by line. Quote markers are written as "> "
// and nested lists are indented by their parent's marker, whatever the spacing typed.
func (w *htmlWriter) markdownText(n gast.Node) {
	switch n := n.(type) {
	case *gast.Heading:
		w.headingText(n)
	case *gast.ThematicBreak:
		w.textOut(w.ruleText(n))
	case *gast.FencedCodeBlock, *gast.CodeBlock:
		// Each line as inline code, which a quote or an item takes: the code stays code (and
		// is not read as Markdown or links on the next trip); the fences go with <pre>.
		// Blank lines around the code go: an item or a quote does not keep them there.
		for i, l := range strings.Split(strings.Trim(w.codeText(n), "\n"), "\n") {
			if i > 0 {
				w.b.WriteString("\n")
			}
			if l != "" {
				w.tag("<code>")
				w.textOut(l)
				w.tag("</code>")
			}
		}
	case *gast.Blockquote:
		sub := w.sub()
		sub.textBlocks(n, "")
		w.b.WriteString(w.prefixLines(sub.b.String(), "> ", "> "))
	case *gast.List:
		num := n.Start
		for li := n.FirstChild(); li != nil; li = li.NextSibling() {
			if li != n.FirstChild() {
				w.b.WriteString("\n")
				if !n.IsTight {
					w.b.WriteString("\n")
				}
			}
			marker := string(n.Marker) + " "
			if n.IsOrdered() {
				// the number as typed ("1." on every item stays so, #66); else as shown
				if m := orderedMarker.FindStringSubmatch(w.lineAt(li.Pos())); m != nil {
					marker = m[1] + m[2] + " "
				} else {
					marker = strconv.Itoa(num) + string(n.Marker) + " "
				}
				num++
			}
			sub := w.sub()
			sub.textBlocks(li, strings.Repeat(" ", len(marker)))
			w.b.WriteString(w.prefixLines(sub.b.String(), marker, ""))
		}
	default:
		w.block(n)
	}
}

// headingText writes heading n as text, as it is typed (#66): "## h ##", or "h" over a line
// of "---" (in a list item, Markdown reads "- a" over "  ---" as a heading, not as an item
// and a rule). An empty heading is its marker alone, not "# " (#72).
func (w *htmlWriter) headingText(n *gast.Heading) {
	lines := n.Lines()
	if lines.Len() == 0 {
		w.textOut(strings.Repeat("#", n.Level))
		return
	}
	first, last := lines.At(0), lines.At(lines.Len()-1)
	before := string(w.src[lineStart(w.src, first.Start):first.Start])
	if open := atxOpen.FindString(before); open != "" {
		w.textOut(open)
		w.inlines(n)
		w.textOut(strings.TrimRight(w.lineAt(last.Stop), " \t")) // a closing "##"
		return
	}
	w.inlines(n)
	// the underline is the line after the text
	next := last.Stop
	if next == 0 || w.src[next-1] != '\n' {
		if i := strings.IndexByte(string(w.src[next:]), '\n'); i >= 0 {
			next += i + 1
		} else {
			next = len(w.src)
		}
	}
	_, under := cutQuotePrefix(w.lineAt(next))
	w.b.WriteString("\n")
	w.textOut(strings.TrimSpace(under))
}

// atxOpen matches the opening "#"s of an ATX heading, at the end of what comes before its
// text on the line.
var atxOpen = regexp.MustCompile(`#{1,6}[ \t]*$`)

// lineStart returns where the line holding position i starts.
func lineStart(src []byte, i int) int {
	return strings.LastIndexByte(string(src[:i]), '\n') + 1
}

// lineAt returns the source from position i to the end of its line.
func (w *htmlWriter) lineAt(i int) string {
	if i < 0 || i > len(w.src) {
		return ""
	}
	rest := string(w.src[i:])
	if j := strings.IndexByte(rest, '\n'); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

// heading writes heading n where Asana accepts no <h1>/<h2> (see Target): as a bold
// paragraph, so its words stay and still stand out. Bold inside it is not marked again
// (<strong> in <strong>); an empty heading writes nothing.
func (w *htmlWriter) heading(n *gast.Heading) {
	in := w.sub()
	in.text = w.text
	in.bold++
	in.inlines(n)
	if in.b.Len() == 0 {
		return // (blocks skips it, see emptyHeading)
	}
	// Spaces around the text (an indented setext heading's) go outside: Markdown does not
	// read "** a**" as bold, so the editor writes them outside, and so does this.
	s := in.b.String()
	rest := strings.TrimLeft(s, " \t")
	body := strings.TrimRight(rest, " \t")
	if body == "" {
		w.b.WriteString(s)
		return
	}
	w.b.WriteString(s[:len(s)-len(rest)])
	w.tag("<strong>")
	w.b.WriteString(body)
	w.tag("</strong>")
	w.b.WriteString(rest[len(body):])
}

// emptyHeading reports whether n is a heading without text that is written as a paragraph
// (see heading), and so writes nothing.
func (w *htmlWriter) emptyHeading(n gast.Node) bool {
	if n.Kind() != gast.KindHeading || w.target.layout() || w.asText(n) {
		return false
	}
	in := w.sub()
	in.inlines(n)
	return in.b.Len() == 0
}

// ruleText is rule n as text, where it is not an element: the line as written ("***",
// "- - -"). (For a task's notes, only a rule in a quote's list or a list item's quote is
// written as text, as typed too since #66.)
func (w *htmlWriter) ruleText(n gast.Node) string {
	if v, ok := n.Attribute(ruleAttr); ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return "---"
}

// textBlocks writes the blocks inside n as text (see markdownText), each on its own line;
// paragraphs (of a loose list item) are apart by a blank line, as they are written. The
// lines of a list inside are indented by indent (the width of the item's marker), so that
// it shows as nested; other lines are not indented, so no character is added in front of
// their text.
func (w *htmlWriter) textBlocks(n gast.Node, indent string) {
	var prev gast.Node
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		if w.blankText(c) || c.Kind() == gast.KindLinkReferenceDefinition {
			continue // "- &#32;", "> &#32;": empty; "[ref]: url" writes nothing (#66)
		}
		if prev != nil {
			if prev.Kind() == gast.KindParagraph && c.Kind() == gast.KindParagraph {
				w.b.WriteString("\n\n")
			} else {
				w.b.WriteString("\n")
			}
		}
		if c.Kind() == gast.KindList && indent != "" {
			sub := w.sub()
			sub.markdownText(c)
			w.b.WriteString(w.prefixLines(sub.b.String(), indent, indent))
		} else {
			w.markdownText(c)
		}
		prev = c
	}
}

// prefixLines puts first before the first line of s (written output) and rest before the
// others; blank lines get them without trailing spaces.
func (w *htmlWriter) prefixLines(s, first, rest string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		p := rest
		if i == 0 {
			p = first
		}
		if l == "" {
			p = strings.TrimRight(p, " ")
		}
		lines[i] = w.esc(p) + l
	}
	return strings.Join(lines, "\n")
}

// codeText is the text of a code block, without its last line break.
func (w *htmlWriter) codeText(n gast.Node) string {
	lines := n.Lines()
	var code strings.Builder
	for i := 0; i < lines.Len(); i++ {
		seg := lines.At(i)
		code.Write(seg.Value(w.src))
	}
	return strings.TrimRight(code.String(), "\n")
}

// blankText reports whether n is a paragraph of nothing but whitespace (as ToEditableMarkdown
// writes an empty list item or quote: "- &#32;", "> &#32;").
func (w *htmlWriter) blankText(n gast.Node) bool {
	switch n.(type) {
	case *gast.Paragraph, *gast.TextBlock:
	default:
		return false
	}
	sub := &htmlWriter{src: w.src, plain: w.plain, target: w.target}
	sub.inlines(n)
	return asciiBlank(sub.b.String()) // "　" (U+3000) or NBSP alone is text, not a filler
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
			w.b.WriteString(w.lineBreak())
		}
	case *gast.String:
		w.textOut(string(n.Value))
	case *gast.CodeSpan:
		// Code over a line break is code of each line, with the line break as text between
		// them, as ToEditableMarkdown writes it (see splitCodeLines, #65). Blank lines at its
		// start and end go (a line break there would end a block, which an edit drops);
		// blank lines between are text.
		var code strings.Builder
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			if t, ok := c.(*gast.Text); ok {
				code.Write(t.Segment.Value(w.src))
			}
		}
		lines := strings.Split(code.String(), "\n")
		if len(lines) > 1 {
			for len(lines) > 0 && asciiBlank(lines[0]) {
				lines = lines[1:]
			}
			for len(lines) > 0 && asciiBlank(lines[len(lines)-1]) {
				lines = lines[:len(lines)-1]
			}
			if len(lines) == 0 {
				lines = []string{" "} // only line breaks: a space, as Markdown reads it
			}
		}
		for i, l := range lines {
			if i > 0 {
				w.b.WriteString(w.lineBreak())
			}
			if len(lines) > 1 && asciiBlank(l) {
				w.textOut(l)
				continue
			}
			w.tag("<code>")
			w.textOut(l)
			w.tag("</code>")
		}
	case *gast.Emphasis:
		t := "em"
		if n.Level >= 2 {
			t = "strong"
		}
		w.mark(n, t, strings.Repeat("*", n.Level))
	case *east.Strikethrough:
		w.mark(n, "s", "~~")
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
		// An email address ("a@x.jp", or <a@x.jp>) links to mailto: (as goldmark's renderer
		// writes it); the text is the address as written (for "www.x.jp", without the
		// "http://" the link gets).
		u := string(n.URL(w.src))
		if n.AutoLinkType == gast.AutoLinkEmail && !strings.HasPrefix(strings.ToLower(u), "mailto:") {
			u = "mailto:" + u
		}
		w.tag(`<a href="` + html.EscapeString(u) + `">`)
		w.textOut(string(n.Label(w.src)))
		w.tag("</a>")
	case *gast.Image:
		dest := string(n.Destination)
		if gid, ok := ImageGID(dest); ok {
			if !w.target.layout() {
				// No <img> here (see Target): the image stays as the Markdown that refers
				// to it, by gid (a cached file's path is local to this machine).
				w.textOut("![")
				w.inlines(n)
				w.textOut("](" + assetScheme + gid)
				if len(n.Title) > 0 {
					w.textOut(` "` + string(n.Title) + `"`)
				}
				w.textOut(")")
				return
			}
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

// lineBreak is a line break in text as it is written: a space in a heading, which is one
// line (a setext heading's lines are, as ToEditableMarkdown writes it back, #64).
func (w *htmlWriter) lineBreak() string {
	if w.inHead {
		return " "
	}
	return "\n"
}

// blankLine matches a blank line inside text, blankLines a run of them.
var (
	blankLine  = regexp.MustCompile(`\n[ \t]*\n`)
	blankLines = regexp.MustCompile(`\n(?:[ \t]*\n){2,}`)
)

// mark writes inline n as the element t. Written as text (see markdownText), a mark that
// spans a blank line (which a list item keeps, as "\" lines) is written as its delimiters
// instead: outside an item a blank line ends the paragraph, so the mark would not read
// back, and Markdown reads such delimiters as text.
func (w *htmlWriter) mark(n gast.Node, t, delim string) {
	if t == "strong" && w.bold > 0 {
		w.inlines(n) // already bold (a heading written as a bold paragraph)
		return
	}
	if !w.text {
		w.tag("<" + t + ">")
		w.inlines(n)
		w.tag("</" + t + ">")
		return
	}
	in := w.sub()
	in.inlines(n)
	if blankLine.MatchString(in.b.String()) {
		w.textOut(delim)
		w.b.WriteString(in.b.String())
		w.textOut(delim)
		return
	}
	w.tag("<" + t + ">")
	w.b.WriteString(in.b.String())
	w.tag("</" + t + ">")
}

// textEscape matches a backslash escape ("\*") or a character reference ("&amp;", "&#32;").
var textEscape = regexp.MustCompile(`\\[!-/:-@\[-` + "`" + `{-~]|&(?:#[0-9]{1,7}|#[xX][0-9a-fA-F]{1,6}|[A-Za-z][A-Za-z0-9]{0,31});`)

// decodeText turns the raw source of a text segment into its text: backslash escapes and
// character references are resolved in one pass (as goldmark's renderer does), so "\&amp;"
// is the text "&amp;". Unknown references ("&foo;") stay as written. (An escaped space is
// not text, see escapedSpaceParser.)
func decodeText(src []byte) string {
	return textEscape.ReplaceAllStringFunc(string(src), func(m string) string {
		if m[0] == '\\' {
			return m[1:]
		}
		return html.UnescapeString(m)
	})
}

// cachedAttachment matches files downloaded by asagao (".../attachments/<gid>-name").
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

// keepIndentation makes the indentation of text lines part of the text. Markdown drops the
// indentation of a paragraph line (or reads 4 spaces as a code block), but Asana keeps it,
// and editable Markdown writes it as it is (ToEditableMarkdown). The first indentation
// character of such a line becomes a character reference ("&#32;" / "&#9;"), which Markdown
// keeps as text. Lines that are Markdown structure stay as they are: list items and the
// lines that continue them, quotes, headings, fences and fenced code, rules.
func keepIndentation(src string) string {
	lines := strings.Split(src, "\n")
	var fence string // the fence of the open code block, "" outside one
	inItem := false  // in the paragraph of a list item (where indentation is list structure)
	for i, l := range lines {
		prefix, rest := cutQuotePrefix(l)
		trimmed := strings.TrimLeft(rest, " \t")
		if fence != "" {
			if closesFence(trimmed, fence) {
				fence = ""
			}
			continue
		}
		if strings.TrimSpace(trimmed) == "" {
			inItem = false
			continue
		}
		if f := openingFence(trimmed); f != "" {
			fence = f
			continue
		}
		if listItemStart(trimmed) {
			inItem = true
			continue
		}
		indent := rest[:len(rest)-len(trimmed)]
		if indent == "" || inItem || blockStart(trimmed) {
			continue
		}
		lines[i] = prefix + indentRef[indent[0]] + indent[1:] + trimmed
	}
	return strings.Join(lines, "\n")
}

// cutQuotePrefix splits a line into its blockquote markers ("> > ") and the rest.
func cutQuotePrefix(l string) (prefix, rest string) {
	rest = l
	for {
		t := strings.TrimLeft(rest, " ")
		if len(rest)-len(t) > 3 || !strings.HasPrefix(t, ">") {
			break
		}
		t = t[1:]
		if strings.HasPrefix(t, " ") {
			t = t[1:]
		}
		rest = t
	}
	return l[:len(l)-len(rest)], rest
}

var fenceRe = regexp.MustCompile("^(`{3,}|~{3,})")

// openingFence returns the fence a line opens a fenced code block with, or "".
func openingFence(line string) string {
	f := fenceRe.FindString(line)
	if f != "" && f[0] == '`' && strings.Contains(line[len(f):], "`") {
		return "" // a backtick fence's info string has no backticks: this is inline code
	}
	return f
}

// closesFence reports whether line closes a code block opened with fence.
func closesFence(line, fence string) bool {
	f := fenceRe.FindString(line)
	return f != "" && f[0] == fence[0] && len(f) >= len(fence) && strings.TrimSpace(line[len(f):]) == ""
}

// listItemStart reports whether line (without indentation) starts a list item.
func listItemStart(line string) bool {
	return listBullet(line, '-') || listBullet(line, '+') || listBullet(line, '*') ||
		orderedMarker.MatchString(line)
}

// blockStart reports whether line (without indentation) starts a Markdown block other than
// a paragraph: a quote, an ATX heading, a rule or a setext underline. (Editable Markdown
// escapes these at the start of text lines, so text never looks like them.)
func blockStart(line string) bool {
	return strings.HasPrefix(line, ">") || strings.HasPrefix(line, "#") ||
		onlyOf(line, '-') || onlyOf(line, '=') || onlyOf(line, '*') || onlyOf(line, '_')
}
