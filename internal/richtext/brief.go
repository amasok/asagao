package richtext

import (
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// A project brief's rich text (#60) has, beyond what a task description has, tables
// (<table>, <tr>, <td> with colspan, rowspan and data-cell-widths) and embeds (<object
// type="application/vnd.asana.external_media" ...>, milestones, goals). Markdown cannot
// write them without losing something, so in editable Markdown each one at the top of the
// brief is kept as Asana gave it, in a fenced block of its own:
//
//	```asana-html
//	<table><tr><td>a</td><td>b</td></tr></table>
//	```
//
// ProjectBrief writes such a block back as the HTML in it, unchanged, so a brief edited
// here keeps them; the text of a table can be edited in place. Everything else is read and
// written as a task description is (headings, rules and images included).

// BriefFence is the info string of a fenced block that holds a brief's HTML as it is.
const BriefFence = "asana-html"

// briefBlockTags are the elements kept as HTML (at the top of the brief).
var briefBlockTags = map[string]bool{"table": true, "object": true}

// briefTags are the elements a block written or changed here may hold, with the
// attributes each may have besides data-* ones (Asana's rich text for project briefs).
// Anything else is refused before it is queued: Asana would reject it with 400. A block as
// Asana gave it is not checked (BriefFromMarkdown).
var briefTags = map[string]map[string]bool{
	"table":  {},
	"tr":     {},
	"td":     {"colspan": true, "rowspan": true, "data-cell-widths": true, "width": true},
	"object": {"type": true, "data-asana-gid": true, "data-asana-type": true, "data": true},
	"a": {"href": true, "data-asana-gid": true, "data-asana-type": true, "data-asana-accessible": true,
		"data-asana-dynamic": true, "data-asana-project": true, "data-asana-tag": true},
	"img":    {"data-asana-gid": true, "alt": true}, // an image attached to the brief (no src: 400)
	"strong": {}, "em": {}, "b": {}, "i": {}, "u": {}, "s": {}, "code": {}, "br": {}, "hr": {},
	"ul": {}, "ol": {}, "li": {}, "blockquote": {}, "pre": {}, "h1": {}, "h2": {},
}

// The placeholder of a kept block while the rest is converted: private-use characters
// that no converter escapes and nobody types.
const (
	blockMarkOpen  = ""
	blockMarkClose = ""
)

func blockMark(i int) string { return blockMarkOpen + strconv.Itoa(i) + blockMarkClose }

var blockMarkRe = regexp.MustCompile("\n*" + blockMarkOpen + `(\d+)` + blockMarkClose + "\n*")

// BriefToEditableMarkdown converts a brief's html_text to Markdown for editing: as
// ToEditableMarkdownWith (imageDest may be nil), with its top-level tables and embeds kept
// as asana-html blocks.
func BriefToEditableMarkdown(htmlText string, imageDest func(gid string) string) string {
	rest, blocks := cutBriefBlocks(htmlText)
	if len(blocks) == 0 {
		return ToEditableMarkdownWith(htmlText, imageDest)
	}
	md := ToEditableMarkdownWith(rest, imageDest)
	var out []string
	for _, line := range strings.Split(md, "\n") {
		if !strings.Contains(line, blockMarkOpen) {
			out = append(out, line)
			continue
		}
		// a placeholder line (with the hard break the converter may end it with); text
		// beside it, if any, goes on lines of its own
		t := strings.TrimRight(line, " \\")
		last := 0
		for _, loc := range briefMarkRe.FindAllStringSubmatchIndex(t, -1) {
			if before := strings.TrimSpace(t[last:loc[0]]); before != "" {
				out = append(out, before)
			}
			n, _ := strconv.Atoi(t[loc[2]:loc[3]])
			out = append(out, fenceBlock(blocks[n])...)
			last = loc[1]
		}
		if after := strings.TrimSpace(t[last:]); after != "" {
			out = append(out, after)
		}
	}
	return strings.Trim(collapseBlankLines(strings.Join(out, "\n")), "\n")
}

var briefMarkRe = regexp.MustCompile(blockMarkOpen + `(\d+)` + blockMarkClose)

// collapseBlankLines makes runs of blank lines one (where a block was put between lines).
func collapseBlankLines(s string) string {
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return s
}

// fenceBlock writes raw HTML as an asana-html block, with a fence longer than any run of
// backticks in it.
func fenceBlock(raw string) []string {
	n := 3
	for run, i := 0, 0; i < len(raw); i++ {
		if raw[i] == '`' {
			run++
			if run >= n {
				n = run + 1
			}
		} else {
			run = 0
		}
	}
	fence := strings.Repeat("`", n)
	return []string{"", fence + BriefFence, raw, fence, ""}
}

// cutBriefBlocks returns htmlText with each top-level table and embed (a child of <body>)
// replaced by a placeholder line, and the HTML of each, as it was written. htmlText that
// is not well-formed is returned as it is, with none.
func cutBriefBlocks(htmlText string) (string, []string) {
	if !strings.Contains(htmlText, "<table") && !strings.Contains(htmlText, "<object") {
		return htmlText, nil
	}
	d := xml.NewDecoder(strings.NewReader(htmlText))
	d.Strict = false
	d.Entity = xml.HTMLEntity
	type span struct{ from, to int64 }
	var spans []span
	depth, inBody := 0, false
	var start int64 = -1
	blockDepth := 0
	for {
		at := d.InputOffset()
		tok, err := d.RawToken()
		if err == io.EOF {
			break
		}
		if err != nil {
			return htmlText, nil
		}
		switch t := tok.(type) {
		case xml.StartElement:
			name := strings.ToLower(t.Name.Local)
			if voidTags[name] { // no depth: HTML may not close it (<br>)
				continue
			}
			depth++
			if depth == 1 && name == "body" {
				inBody = true
			}
			top := depth == 2 && inBody || depth == 1 && !inBody && name != "body"
			if start < 0 && top && briefBlockTags[name] {
				start, blockDepth = at, depth
			}
		case xml.EndElement:
			if voidTags[strings.ToLower(t.Name.Local)] {
				continue
			}
			if start >= 0 && depth == blockDepth {
				spans = append(spans, span{start, d.InputOffset()})
				start = -1
			}
			depth--
		}
	}
	if start >= 0 || len(spans) == 0 {
		return htmlText, nil
	}
	var b strings.Builder
	blocks := make([]string, len(spans))
	last := int64(0)
	for i, s := range spans {
		blocks[i] = htmlText[s.from:s.to]
		b.WriteString(htmlText[last:s.from])
		b.WriteString("\n" + blockMark(i) + "\n")
		last = s.to
	}
	b.WriteString(htmlText[last:])
	return b.String(), blocks
}

// BriefFromMarkdown converts editable Markdown to a brief's html_text and its plain text:
// as a task description, with each asana-html block written as the HTML in it. original is
// the brief's html_text the Markdown was made from ("" for a new brief): a block as it was
// there is Asana's own and goes back unchecked. Any other block that Asana would refuse
// (not well-formed, or with an element or attribute a brief does not take) is an error, so
// it is not queued.
func BriefFromMarkdown(md, original string) (htmlText, plain string, err error) {
	rest, blocks := cutFences(normalizeNewlines(md))
	_, kept := cutBriefBlocks(original)
	asGiven := map[string]bool{}
	for _, b := range kept {
		asGiven[strings.TrimSpace(b)] = true
	}
	for i, raw := range blocks {
		if asGiven[strings.TrimSpace(raw)] {
			continue
		}
		// an image as Asana reads it back (src, sizes, style) is checked as it is written
		if err := checkBriefHTML(writeImagesWithGID(raw)); err != nil {
			return "", "", fmt.Errorf("%d 番目の %s ブロック: %w", i+1, BriefFence, err)
		}
	}
	htmlText = TaskNotes.FromMarkdown(rest)
	plain = TaskNotes.PlainFromMarkdown(rest)
	if len(blocks) == 0 {
		return htmlText, plain, nil
	}
	htmlText = putBlocks(htmlText, blocks, writeImages, true)
	htmlText = textBeforeBlock.ReplaceAllString(htmlText, "$1\n$2")
	plain = putBlocks(plain, blocks, briefPlain, false)
	return htmlText, plain, nil
}

// putBlocks writes each block (by write) where its placeholder is in s. In html, a block
// follows text on a line of its own ("text\n<table>") and what follows it starts right
// after it, as Asana writes blocks; in plain text it is a line of its own.
func putBlocks(s string, blocks []string, write func(string) string, html bool) string {
	return blockMarkRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := blockMarkRe.FindStringSubmatch(m)
		n, _ := strconv.Atoi(sub[1])
		if n >= len(blocks) {
			return m
		}
		if html {
			return write(blocks[n])
		}
		return "\n" + write(blocks[n]) + "\n"
	})
}

// textBeforeBlock is text right before a kept block, where Asana writes a line break.
var textBeforeBlock = regexp.MustCompile(`([^>\n])(<(?:table|object)[\s>/])`)

// cutFences returns md with each top-level asana-html block replaced by a placeholder
// paragraph, and the text of each block. Other fenced blocks are left as they are (an
// asana-html fence inside one is code). A top-level Markdown table (GFM: a header row, a
// delimiter row, rows; after a blank line) becomes a block of its <table>.
func cutFences(md string) (string, []string) {
	lines := strings.Split(md, "\n")
	var out []string
	var blocks []string
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if rows := tableAt(lines, i, len(out) == 0 || strings.TrimSpace(out[len(out)-1]) == ""); rows > 0 {
			// a Markdown table: Asana takes no Markdown, but a brief takes <table>
			blocks = append(blocks, tableHTML(lines[i:i+rows]))
			out = append(out, "", blockMark(len(blocks)-1), "")
			i += rows - 1
			continue
		}
		ch, n, info := fenceOpen(line)
		if n == 0 {
			out = append(out, line)
			continue
		}
		j := i + 1
		for ; j < len(lines); j++ {
			if fenceCloses(lines[j], ch, n) {
				break
			}
		}
		end := min(j, len(lines))
		if strings.TrimSpace(info) != BriefFence || line != strings.TrimLeft(line, " \t") {
			out = append(out, lines[i:min(j+1, len(lines))]...)
		} else {
			blocks = append(blocks, strings.Join(lines[i+1:end], "\n"))
			out = append(out, "", blockMark(len(blocks)-1), "")
		}
		i = j
	}
	return strings.Join(out, "\n"), blocks
}

// fenceOpen reads a fence opening line (indented by up to 3 spaces): its character, its
// length (0: not a fence) and its info string.
func fenceOpen(line string) (byte, int, string) {
	t := strings.TrimLeft(line, " ")
	if len(line)-len(t) > 3 || t == "" || (t[0] != '`' && t[0] != '~') {
		return 0, 0, ""
	}
	n := 0
	for n < len(t) && t[n] == t[0] {
		n++
	}
	if n < 3 || (t[0] == '`' && strings.Contains(t[n:], "`")) {
		return 0, 0, ""
	}
	return t[0], n, t[n:]
}

// fenceCloses reports whether line closes a fence of n ch characters.
func fenceCloses(line string, ch byte, n int) bool {
	t := strings.TrimLeft(line, " ")
	if len(line)-len(t) > 3 {
		return false
	}
	t = strings.TrimRight(t, " \t")
	return len(t) >= n && strings.Trim(t, string(ch)) == ""
}

// checkBriefHTML checks that raw is well-formed rich text of elements and attributes a
// brief takes: every element closed (an HTML void element such as <br> may be written
// without its end tag), each attribute one its element takes (or a data-* one).
func checkBriefHTML(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return errors.New("中身が空です")
	}
	if strings.Contains(raw, "<![CDATA[") {
		return errors.New("CDATA はプロジェクトブリーフで使えません")
	}
	d := xml.NewDecoder(strings.NewReader("<x>" + raw + "</x>"))
	d.Strict = false // HTML entities and void elements, as Asana writes them; tags are matched below
	d.Entity = xml.HTMLEntity
	var open []string
	for {
		tok, err := d.RawToken()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("HTML として読めません (%v)", err)
		}
		switch t := tok.(type) {
		case xml.Comment, xml.Directive, xml.ProcInst: // <!-- -->, <!DOCTYPE>, <?xml ?>
			return errors.New("コメント・<!...>・<?...?> はプロジェクトブリーフで使えません")
		case xml.StartElement:
			name := strings.ToLower(t.Name.Local)
			if len(open) == 0 && name == "x" && t.Name.Space == "" {
				open = append(open, name)
				continue
			}
			attrs, known := briefTags[name]
			if !known || t.Name.Space != "" {
				return fmt.Errorf("<%s> はプロジェクトブリーフで使えません", t.Name.Local)
			}
			for _, a := range t.Attr {
				an := strings.ToLower(a.Name.Local)
				if a.Name.Space != "" || !attrs[an] && !strings.HasPrefix(an, "data-") {
					return fmt.Errorf("<%s> の属性 %s は使えません", t.Name.Local, a.Name.Local)
				}
			}
			if name == "img" && !hasAttr(t, "data-asana-gid") {
				return errors.New("<img> には、ブリーフに添付した画像の data-asana-gid が要ります (Asana は src の画像を受け付けません)")
			}
			if !voidTags[name] {
				open = append(open, name)
			}
		case xml.EndElement:
			name := strings.ToLower(t.Name.Local)
			if voidTags[name] {
				continue // <br></br>, <img ...></img>, or the end of a self-closed one
			}
			if len(open) == 0 || open[len(open)-1] != name {
				return fmt.Errorf("</%s> の前に閉じていないタグがあります", t.Name.Local)
			}
			open = open[:len(open)-1]
		}
	}
	if len(open) != 0 {
		return fmt.Errorf("<%s> が閉じていません", open[len(open)-1])
	}
	return nil
}

// voidTags are the elements of a brief that HTML writes without an end tag.
var voidTags = map[string]bool{"br": true, "hr": true, "img": true}

// briefPlain is the text of a kept block, for the plain text of the brief: the cells of a
// table separated by tabs, a row per line; an embed's text (its link).
func briefPlain(raw string) string {
	d := xml.NewDecoder(strings.NewReader("<x>" + raw + "</x>"))
	d.Strict = false
	d.Entity = xml.HTMLEntity
	var b strings.Builder
	cell := 0
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "tr":
				if b.Len() > 0 {
					b.WriteString("\n")
				}
				cell = 0
			case "td":
				if cell > 0 {
					b.WriteString("\t")
				}
				cell++
			}
		case xml.CharData:
			b.Write(t)
		}
	}
	return strings.TrimSpace(b.String())
}

// tableDelimiter matches a GFM table's delimiter row ("| --- | :-: |").
var tableDelimiter = regexp.MustCompile(`^\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?\s*$`)

// tableAt returns how many lines from lines[i] are a Markdown table (0: none there). It
// starts a table only after a blank line (afterBlank), not indented, and its header and
// delimiter rows have as many cells.
func tableAt(lines []string, i int, afterBlank bool) int {
	if !afterBlank || i+1 >= len(lines) {
		return 0
	}
	head, delim := lines[i], strings.TrimRight(lines[i+1], " \t")
	if head == "" || head[0] == ' ' || head[0] == '\t' || !strings.Contains(head, "|") ||
		!tableDelimiter.MatchString(delim) ||
		!strings.Contains(delim, "-") || len(tableCells(head)) != len(tableCells(delim)) {
		return 0
	}
	n := 2
	for i+n < len(lines) {
		l := lines[i+n]
		if strings.TrimSpace(l) == "" || !strings.Contains(l, "|") || indentWidth(l) >= 4 {
			break
		}
		n++
	}
	return n
}

// tableCells splits a table row into its cells: around "|" not escaped ("\|"), without the
// outer pipes.
func tableCells(row string) []string {
	row = strings.TrimSpace(row)
	row = strings.TrimPrefix(row, "|")
	if strings.HasSuffix(row, "|") && !strings.HasSuffix(row, "\\|") {
		row = row[:len(row)-1]
	}
	var cells []string
	var b strings.Builder
	for i := 0; i < len(row); i++ {
		switch {
		case row[i] == '\\' && i+1 < len(row) && row[i+1] == '|':
			b.WriteByte('|')
			i++
		case row[i] == '|':
			cells = append(cells, strings.TrimSpace(b.String()))
			b.Reset()
		default:
			b.WriteByte(row[i])
		}
	}
	return append(cells, strings.TrimSpace(b.String()))
}

// tableHTML writes a Markdown table (its header row, delimiter row and rows) as a brief's
// <table>: Asana's rich text has no header cells, so the header is the first row. A cell's
// Markdown is converted as a line of text (bold, links...); one that would be a block (a
// heading, a list) is its text as written.
func tableHTML(rows []string) string {
	width := len(tableCells(rows[0]))
	var b strings.Builder
	b.WriteString("<table>")
	for k, row := range rows {
		if k == 1 {
			continue // the delimiter row
		}
		cells := tableCells(row)
		b.WriteString("<tr>")
		for c := 0; c < width; c++ {
			cell := ""
			if c < len(cells) {
				cell = cells[c]
			}
			b.WriteString("<td>" + cellHTML(cell) + "</td>")
		}
		b.WriteString("</tr>")
	}
	b.WriteString("</table>")
	return b.String()
}

// cellHTML is a table cell's Markdown as inline rich text.
func cellHTML(md string) string {
	h := strings.TrimSuffix(strings.TrimPrefix(TaskNotes.FromMarkdown(md), "<body>"), "</body>")
	for _, block := range []string{"<h1", "<h2", "<ul", "<ol", "<blockquote", "<pre", "<hr", "<img", "\n"} {
		if strings.Contains(h, block) {
			return html.EscapeString(md)
		}
	}
	return h
}

func hasAttr(t xml.StartElement, name string) bool {
	for _, a := range t.Attr {
		if strings.EqualFold(a.Name.Local, name) && a.Value != "" {
			return true
		}
	}
	return false
}

var (
	imgTag   = regexp.MustCompile(`(?i)<img\b[^>]*>`)
	imgEnd   = regexp.MustCompile(`(?i)</img\s*>`)
	imgGIDRe = regexp.MustCompile(`(?i)\bdata-asana-gid\s*=\s*(?:"([^"]*)"|'([^']*)')`)
)

// writeImagesWithGID is writeImages for the images that have a gid only: one without is
// left as it is (for checkBriefHTML to refuse).
func writeImagesWithGID(raw string) string {
	raw = imgEnd.ReplaceAllString(raw, "")
	return imgTag.ReplaceAllStringFunc(raw, func(tag string) string {
		if gid := imgGID(tag); gid != "" {
			return `<img data-asana-gid="` + html.EscapeString(gid) + `"/>`
		}
		return tag
	})
}

// writeImages writes the images of a block as Asana takes them: <img data-asana-gid="…"/>
// only. Asana reads an image back with src, sizes and a style, and refuses src when it is
// written (400 "XML is invalid", found on the real Asana, #60); an image without a gid
// (not an attachment) cannot be written, and is left out.
func writeImages(raw string) string {
	raw = imgEnd.ReplaceAllString(raw, "")
	return imgTag.ReplaceAllStringFunc(raw, func(tag string) string {
		if gid := imgGID(tag); gid != "" {
			return `<img data-asana-gid="` + html.EscapeString(gid) + `"/>`
		}
		return ""
	})
}

// imgGID is the data-asana-gid of an <img> tag ("" for none).
func imgGID(tag string) string {
	m := imgGIDRe.FindStringSubmatch(tag)
	if m == nil {
		return ""
	}
	return m[1] + m[2]
}
