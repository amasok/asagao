package richtext

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/yuin/goldmark/util"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Emphasis (<strong>, <em>, <s>) is written as delimiters ("**", "*", "~~"), which Markdown
// reads as emphasis only where they open and close by its rules (#58). The HTML is first
// put in a form whose delimiters can be read back (normalizeMarks), and the delimiters that
// would still not be are set apart afterwards with an escaped space (separateTokens), which
// FromMarkdown reads as nothing.

// markDelim is the delimiter emphasis element n is written with, or "" if n is not one.
func markDelim(n *html.Node) string {
	if n.Type != html.ElementNode {
		return ""
	}
	switch n.DataAtom {
	case atom.Strong, atom.B:
		return "**"
	case atom.Em, atom.I:
		return "*"
	case atom.S, atom.Strike, atom.Del:
		return "~~"
	}
	return ""
}

// normalizeMarks puts emphasis in a form Markdown can write (#58):
//   - emphasis inside the same emphasis is the outer one ("<em>a<em>b</em></em>" would be
//     "*a*b**", and "<em><em>a</em></em>" bold);
//   - emphasis over a blank line is emphasis of each paragraph: a blank line ends the
//     paragraph, and with it the emphasis.
//
// None of these changes how the text looks.
func normalizeMarks(doc *html.Node) {
	flattenMarks(doc, map[string]bool{})
	splitMarksAtBlankLines(doc)
}

func flattenMarks(n *html.Node, active map[string]bool) {
	for ch := n.FirstChild; ch != nil; {
		d := markDelim(ch)
		switch {
		case d != "" && active[d]:
			// the same emphasis again: its children take its place, and are looked at next
			first := ch.FirstChild
			next := ch.NextSibling
			unwrap(ch)
			if first != nil {
				next = first
			}
			ch = next
			continue
		case d != "":
			active[d] = true
			flattenMarks(ch, active)
			active[d] = false
		case ch.Type == html.ElementNode && ch.DataAtom != atom.Pre:
			flattenMarks(ch, active)
		}
		ch = ch.NextSibling
	}
}

// unwrap replaces n with its children.
func unwrap(n *html.Node) {
	p := n.Parent
	for ch := n.FirstChild; ch != nil; ch = n.FirstChild {
		n.RemoveChild(ch)
		p.InsertBefore(ch, n)
	}
	p.RemoveChild(n)
}

// splitMarksAtBlankLines splits the outermost emphasis holding a blank line (line breaks,
// as text or <br>, with nothing but spaces and tabs between them) into emphasis of the
// parts around it; the blank line itself goes outside. Headings (one line) and code blocks
// are left alone.
func splitMarksAtBlankLines(n *html.Node) {
	for ch := n.FirstChild; ch != nil; {
		next := ch.NextSibling
		switch {
		case markDelim(ch) != "":
			splitMark(ch)
		case ch.Type == html.ElementNode && ch.DataAtom != atom.Pre &&
			ch.DataAtom != atom.H1 && ch.DataAtom != atom.H2 && ch.DataAtom != atom.H3:
			splitMarksAtBlankLines(ch)
		}
		ch = next
	}
}

// markLeaf is a piece of the content of an emphasis element: a text piece (a single line
// break, or text without one) or an element written as a whole, with the inline elements
// between it and the emphasis (outermost first).
type markLeaf struct {
	n    *html.Node
	path []*html.Node
}

func (l markLeaf) lineBreak() bool {
	return l.n.Type == html.TextNode && l.n.Data == "\n" ||
		l.n.Type == html.ElementNode && l.n.DataAtom == atom.Br
}

func (l markLeaf) blank() bool { return l.n.Type == html.TextNode && asciiBlank(l.n.Data) }

// markLeaves collects the leaves of n (see markLeaf). Elements that are written through
// their children (transparentAtoms) are looked into; others (links, code, images) are
// leaves.
func markLeaves(n *html.Node, path []*html.Node, out []markLeaf) []markLeaf {
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		switch {
		case ch.Type == html.TextNode:
			for _, piece := range splitLines(ch.Data) {
				out = append(out, markLeaf{&html.Node{Type: html.TextNode, Data: piece}, path})
			}
		case ch.Type == html.ElementNode && transparentAtoms[ch.DataAtom]:
			out = markLeaves(ch, append(path[:len(path):len(path)], ch), out)
		default:
			out = append(out, markLeaf{ch, path})
		}
	}
	return out
}

// splitLines splits s into its line breaks ("\n") and the text between them.
func splitLines(s string) []string {
	var out []string
	for s != "" {
		i := strings.IndexByte(s, '\n')
		switch {
		case i < 0:
			out, s = append(out, s), ""
		case i == 0:
			out, s = append(out, "\n"), s[1:]
		default:
			out, s = append(out, s[:i]), s[i:]
		}
	}
	return out
}

// blankLineEnd returns the end of the blank line starting at leaves[i] (a line break): the
// index after the last line break of a run of two or more with only blank text between, or
// i if there is no blank line there.
func blankLineEnd(leaves []markLeaf, i int) int {
	end, breaks := i, 0
	for j := i; j < len(leaves); j++ {
		switch {
		case leaves[j].lineBreak():
			breaks++
			if breaks >= 2 {
				end = j + 1
			}
		case leaves[j].blank():
		default:
			return end
		}
	}
	return end
}

func splitMark(m *html.Node) {
	leaves := markLeaves(m, nil, nil)
	hasBlank := false
	for i, l := range leaves {
		if l.lineBreak() && blankLineEnd(leaves, i) > i {
			hasBlank = true
			break
		}
	}
	if !hasBlank {
		return
	}
	p := m.Parent
	var seg []markLeaf
	flush := func() {
		if len(seg) == 0 {
			return
		}
		root := shallowClone(m)
		var chain []*html.Node // clones of the path of the last leaf
		var orig []*html.Node
		for _, l := range seg {
			k := 0
			for k < len(l.path) && k < len(orig) && orig[k] == l.path[k] {
				k++
			}
			chain, orig = chain[:k], orig[:k]
			for ; k < len(l.path); k++ {
				c := shallowClone(l.path[k])
				parentOf(root, chain).AppendChild(c)
				chain, orig = append(chain, c), append(orig, l.path[k])
			}
			detach(l.n)
			parentOf(root, chain).AppendChild(l.n)
		}
		p.InsertBefore(root, m)
		seg = nil
	}
	for i := 0; i < len(leaves); {
		if leaves[i].lineBreak() {
			if end := blankLineEnd(leaves, i); end > i {
				flush()
				for _, l := range leaves[i:end] {
					detach(l.n)
					p.InsertBefore(l.n, m)
				}
				i = end
				continue
			}
		}
		seg = append(seg, leaves[i])
		i++
	}
	flush()
	p.RemoveChild(m)
}

func parentOf(root *html.Node, chain []*html.Node) *html.Node {
	if len(chain) == 0 {
		return root
	}
	return chain[len(chain)-1]
}

func shallowClone(n *html.Node) *html.Node {
	return &html.Node{Type: n.Type, DataAtom: n.DataAtom, Data: n.Data, Namespace: n.Namespace,
		Attr: append([]html.Attribute(nil), n.Attr...)}
}

func detach(n *html.Node) {
	if n.Parent != nil {
		n.Parent.RemoveChild(n)
	}
}

// inlineToken is a delimiter written to the Markdown output: an emphasis delimiter, the
// fence of a code span, or the start ("[", "![") or the end (")") of a link, at
// out[pos:pos+len(s)].
type inlineToken struct {
	pos  int
	s    string
	open bool
	mark bool // emphasis, not a code fence or a link
}

func (t inlineToken) end() int { return t.pos + len(t.s) }

// emailLocal reports whether ch can be in the local part of an email address, as goldmark's
// Linkify reads it (util.FindEmailIndex).
func emailLocal(ch byte) bool {
	return ch < utf8.RuneSelf && (util.IsAlphaNumeric(ch) || strings.IndexByte(".!#$%&'*+/=?^_`{|}~-", ch) >= 0)
}

// separateTokens sets delimiters apart with sep (an escaped space, which FromMarkdown reads
// as nothing) where Markdown would not read them as written (#58):
//   - a closing delimiter right before an opening one of the same character would be one
//     run of them ("**a****b**", and "`a“b`" for code);
//   - a run of opening delimiters opens only if it is not followed by punctuation, or
//     follows whitespace or punctuation ("x**[l](u)**", "は**「重要」**"), and a run of
//     closing ones closes only if it does not follow punctuation, or is followed by
//     whitespace or punctuation: a space goes on the outer side where neither holds;
//   - inside emphasis written with "*", a run of opening "*"s that could also close would
//     close it ("***`c`*[l](u)*x**": the third "*" closes the "**"), unless the rule of 3
//     keeps them apart: a space goes before it;
//   - Linkify reads "*", "~" and "`" as part of an email address ("z**a@x.jp**" would be a
//     link to "z**a@x.jp"), and "~", "`" and the "[" or "![" of a link as part of a URL
//     ("https://x.jp/a~~b"): where delimiters are between such characters, a space goes
//     outside them (#77).
//
// "~~" right after a "~" is not read as a delimiter (goldmark's strikethrough), so an
// escaped "\~" there becomes "&#126;". tokens are in output order.
func separateTokens(out string, tokens []inlineToken, sep string) string {
	s := &separator{out: out, sep: sep}
	for i := 0; i < len(tokens); {
		j := i + 1
		for j < len(tokens) && tokens[j].pos == tokens[j-1].end() {
			j++
		}
		next := len(out)
		if j < len(tokens) {
			next = tokens[j].pos
		}
		s.group(tokens[i:j], next)
		s.prevEnd = tokens[j-1].end()
		i = j
	}
	if len(s.edits) == 0 {
		return out
	}
	var b strings.Builder
	last := 0
	for _, e := range s.edits { // in order (see group)
		if e.pos < last {
			continue // the same edit again
		}
		b.WriteString(out[last:e.pos])
		b.WriteString(e.ins)
		last = e.pos + e.del
	}
	b.WriteString(out[last:])
	return b.String()
}

// textEdit replaces out[pos:pos+del] with ins.
type textEdit struct {
	pos, del int
	ins      string
}

type separator struct {
	out, sep string
	edits    []textEdit
	prevEnd  int       // where the group before ends
	stars    []starRun // runs of "*" with emphasis open here, innermost last
}

// starRun is a run of opening "*"s: its length, and how many of its emphasis are open.
type starRun struct{ length, open int }

// closesOpen reports whether a run of length n that can close would close emphasis open
// here: CommonMark matches it with an open run unless the lengths add up to a multiple of
// 3 (and are not both multiples of 3), the "rule of 3".
func (s *separator) closesOpen(n int) bool {
	for _, r := range s.stars {
		if (r.length+n)%3 != 0 || r.length%3 == 0 && n%3 == 0 {
			return true
		}
	}
	return false
}

// group adds the edits for g, delimiters written one right after the other (see
// separateTokens). The next group starts at next.
func (s *separator) group(g []inlineToken, next int) {
	out := s.out
	start, end := g[0].pos, g[len(g)-1].end()
	var at []int // where sep goes
	// runs of the same character, split where a closing delimiter meets an opening one
	runStart := 0
	for k := 1; k <= len(g); k++ {
		if k < len(g) {
			prev, cur := g[k-1], g[k]
			same := prev.s[len(prev.s)-1] == cur.s[0]
			if same && !(!prev.open && cur.open) {
				continue
			}
			if same {
				at = append(at, cur.pos) // a closing delimiter, then an opening one
			}
		}
		run := g[runStart:k]
		runStart = k
		if !run[0].mark {
			continue
		}
		rs, re := run[0].pos, run[len(run)-1].end()
		before, after := s.runeBefore(rs), runeAt(out, re)
		if rs > start && slices.Contains(at, rs) {
			before = ' '
		}
		star := run[0].s[0] == '*'
		if run[0].open {
			if !leftFlanking(before, after) ||
				star && rightFlanking(before, after) && s.closesOpen(re-rs) {
				at = append(at, rs)
			}
		} else if !rightFlanking(before, after) {
			at = append(at, re)
		}
		if star && run[0].open {
			s.stars = append(s.stars, starRun{length: re - rs, open: len(run)})
		} else if star {
			for n := len(run); n > 0 && len(s.stars) > 0; n-- {
				if top := &s.stars[len(s.stars)-1]; top.open > 1 {
					top.open--
				} else {
					s.stars = s.stars[:len(s.stars)-1]
				}
			}
		}
	}
	// Linkify reading the delimiters as part of an email address or a URL: the space goes
	// outside the emphasis and code, where the closing delimiters meet the opening ones (a
	// URL leaves out "~" at its end, so closing ones there are not read into it). Only the
	// text up to the next group is looked at: an address over several groups is cut at the
	// last of them, and what the next group gets does not change what this one does.
	outside := end
	for _, t := range g {
		if t.open {
			outside = t.pos
			break
		}
	}
	if start > 0 && (emailLocal(out[start-1]) && emailFollows(out[:next], end) ||
		strings.IndexByte("~`[!", out[start]) >= 0 && urlBefore(out[s.prevEnd:start]) &&
			(outside < end || !util.IsSpaceRune(runeAt(out, end)))) {
		at = append(at, outside)
	}
	slices.Sort(at)
	if g[0].s == "~~" && strings.HasSuffix(out[:start], `\~`) {
		ins := "&#126;"
		if urlBefore(out[s.prevEnd : start-2]) {
			ins = s.sep + ins // or Linkify would read the reference as part of the URL
		}
		s.edits = append(s.edits, textEdit{pos: start - 2, del: 2, ins: ins})
	}
	// ("~~" later in the group follows a delimiter: "~~" after "~~" is set apart above.)
	// Positions only grow from group to group, so edits stay in order.
	for _, p := range slices.Compact(at) {
		ins := s.sep
		if p < len(out) && out[p] == '#' {
			ins += `\` // "# a~~ #" would end a heading with a closing "#" (#64)
		}
		s.edits = append(s.edits, textEdit{pos: p, ins: ins})
	}
}

// runeBefore is the character Markdown sees before position i of the output: a line break
// at the start of a line, and ";" after the one space or tab a line starts with
// (FromMarkdown keeps that indentation as a character reference, see keepIndentation).
func (s *separator) runeBefore(i int) rune {
	// (only the spaces, tabs and ">"s right before i are looked at: a long line with many
	// delimiters stays linear)
	j := i
	for j > 0 && strings.IndexByte(" \t>", s.out[j-1]) >= 0 {
		j--
	}
	if j == 0 || s.out[j-1] == '\n' {
		if _, rest := cutQuotePrefix(s.out[j:i]); rest == "" {
			return '\n'
		} else if len(rest) == 1 {
			return ';'
		}
	}
	r, _ := utf8.DecodeLastRuneInString(s.out[:i])
	return r
}

// emailFollows reports whether the characters from i on are those of an email address's
// local part up to an "@" and a character of a domain.
func emailFollows(out string, i int) bool {
	for ; i < len(out) && emailLocal(out[i]); i++ {
	}
	return i+1 < len(out) && out[i] == '@' && util.IsAlphaNumeric(out[i+1])
}

// urlBefore reports whether text ends with a URL that Linkify would read on into what
// follows: a word (no whitespace) holding "://" or "www.".
func urlBefore(text string) bool {
	w := text
	if j := strings.LastIndexFunc(w, unicode.IsSpace); j >= 0 {
		w = w[j+1:]
	}
	return strings.Contains(w, "://") || strings.Contains(w, "www.")
}

// runeAt is the character at position i of out (a line break at the end).
func runeAt(out string, i int) rune {
	if i >= len(out) {
		return '\n'
	}
	r, _ := utf8.DecodeRuneInString(out[i:])
	return r
}

// leftFlanking and rightFlanking are CommonMark's rules for a delimiter run between before
// and after to open and to close emphasis (as goldmark's parser.ScanDelimiter reads "*" and
// "~").
func leftFlanking(before, after rune) bool {
	return !util.IsSpaceRune(after) &&
		(!util.IsPunctRune(after) || util.IsSpaceRune(before) || util.IsPunctRune(before))
}

func rightFlanking(before, after rune) bool {
	return !util.IsSpaceRune(before) &&
		(!util.IsPunctRune(before) || util.IsSpaceRune(after) || util.IsPunctRune(after))
}
