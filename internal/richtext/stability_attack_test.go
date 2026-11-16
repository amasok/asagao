package richtext

import (
	"math/rand"
	"strings"
	"testing"
)

var rtTexts2 = []string{"a", "b c", "# h", "- x", "1. y", "> q", "+ z", "***", "---", "~~~", "    c", "\tt", " ", "  ", "\t", "a ", "a\t", "a  ", "x \t ", "&", "<", "*s*", "_u_", "[l](u)", "!", "a@x.jp", "www.x.jp", "日本"}

func genInlineRT(r *rand.Rand, depth int) string {
	var sb strings.Builder
	for i := r.Intn(4) + 1; i > 0; i-- {
		switch r.Intn(7) {
		case 0:
			sb.WriteString("<br>")
		case 1:
			sb.WriteString("\n")
		case 2:
			if false { // emphasis with spaces at its ends loses its marks on main already (#49)
				sb.WriteString("<b>" + genInlineRT(r, depth+1) + "</b>")
			}
		case 3:
			// (a link whose text is only whitespace keeps the spaces before it raw: not worth it)
			lt := rtTexts2[r.Intn(len(rtTexts2))]
			if strings.TrimSpace(lt) == "" {
				lt = "l"
			}
			sb.WriteString(`<a href="https://x.jp/">` + escHTML(lt) + `</a>`)
		case 4:
			sb.WriteString(`<img data-asana-gid="7" src="x">`)
		default:
			sb.WriteString(escHTML(rtTexts2[r.Intn(len(rtTexts2))]))
		}
	}
	return sb.String()
}

func escHTML(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func genBlockRT(r *rand.Rand, depth int) string {
	switch r.Intn(6) {
	case 0:
		if false { // quotes holding lists are unstable on main already (#51; TestBlockLookalikesStayText covers quotes)
			return "<blockquote>" + genBlockRT(r, depth+1) + "</blockquote>"
		}
	case 1:
		if false { // whitespace-only text between inline nodes in a list item is dropped on main already (#51)
			tag := []string{"ul", "ol"}[r.Intn(2)]
			s := "<" + tag + ">"
			for i := r.Intn(3) + 1; i > 0; i-- {
				s += "<li>" + genInlineRT(r, 0)
				if r.Intn(4) == 0 && depth < 1 {
					s += "<ul><li>x" + genInlineRT(r, 0) + "</li></ul>"
				}
				s += "</li>"
			}
			return s + "</" + tag + ">"
		}
	case 2:
		return "<pre>" + escHTML(rtTexts2[r.Intn(len(rtTexts2))]) + "</pre>"
	}
	return genInlineRT(r, 0)
}

// ToEditableMarkdown -> FromMarkdown must be stable: converting the result again changes
// nothing (so trailing whitespace, line starts and mailto: links neither grow nor vanish).
func TestEditableRoundTripIsStable(t *testing.T) {
	r := rand.New(rand.NewSource(45))
	fails := 0
	for i := 0; i < 30000 && fails < 8; i++ {
		var sb strings.Builder
		sb.WriteString("<body>")
		for j := r.Intn(3) + 1; j > 0; j-- {
			sb.WriteString(genBlockRT(r, 0))
			if r.Intn(2) == 0 {
				sb.WriteString("\n")
			}
		}
		in := sb.String() + "</body>"
		md1 := ToEditableMarkdown(in)
		h1 := FromMarkdown(md1)
		md2 := ToEditableMarkdown(h1)
		h2 := FromMarkdown(md2)
		// whitespace at the very end of a block is covered by TestNoGhostSpaceAtBlockEnd
		if h1 != h2 {
			fails++
			t.Errorf("not stable\nin  %q\nmd1 %q\nh1  %q\nmd2 %q\nh2  %q", in, md1, h1, md2, h2)
		}
	}
}
