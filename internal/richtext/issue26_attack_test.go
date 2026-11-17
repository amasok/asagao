package richtext

import (
	"strings"
	"testing"
)

// A round trip must be stable in one step (md -> html -> md -> html gives the same html)
// for headings and quoted items built from near forms: spaces, nesting, quotes, links.
func TestAttackHeadingRoundTripStable(t *testing.T) {
	heads := []string{"# ", "## ", "### ", " # ", "#\t"}
	sp := []string{"", "&#32;", " ", "&#9;", "&nbsp;", "&#32;&#32;"}
	cores := []string{
		"a", "[a](https://e.com)", "**a**", "*a*", "~~a~~", "`a`", "`&#32;a`", "[**a**](https://e.com)",
		"**[a](https://e.com)**", "![x](asana:5)", "<a data-asana-gid=\"9\"/>", "[&#32;](https://e.com)b",
		"[ ](https://e.com)", "***a***", "**a *b* c**", "[a **b**](https://e.com)", "a\\", "a  ",
	}
	var cases []string
	for _, h := range heads {
		for _, l := range sp {
			for _, c := range cores {
				if l == "&nbsp;" && strings.HasPrefix(c, "[") && strings.Contains(c, "](https://e.com)") && (strings.HasPrefix(c, "[ ]") || strings.HasPrefix(c, "[&#32;]")) {
					continue // TestAttackNbspBeforeBlankLink
				}
				for _, r := range sp {
					cases = append(cases, h+l+c+r, h+l+c+r+" b", h+l+c+r+"\n\nb")
				}
			}
		}
	}
	for _, c := range cores {
		for _, l := range sp {
			if l == "&nbsp;" && (strings.HasPrefix(c, "[ ]") || strings.HasPrefix(c, "[&#32;]")) {
				continue // TestAttackNbspBeforeBlankLink
			}
			cases = append(cases, l+c+"\n---", l+c+"&#32;\n===")
		}
	}
	bad := 0
	for _, md := range cases {
		h1 := FromMarkdown(md)
		h2 := FromMarkdown(ToEditableMarkdown(h1))
		p1 := TaskNotes.PlainFromMarkdown(md)
		p2 := TaskNotes.PlainFromMarkdown(ToEditableMarkdown(h1))
		if h1 != h2 || p1 != p2 {
			if bad++; bad <= 15 {
				t.Errorf("md %q\nh1 %q\nh2 %q\np1 %q p2 %q", md, h1, h2, p1, p2)
			}
		}
	}
	if bad > 15 {
		t.Errorf("... %d unstable of %d", bad, len(cases))
	}
}

func TestAttackQuoteItemRoundTripStable(t *testing.T) {
	items := []string{"-", "1.", "2.", "*", "+", "- x", "2. x", "-\t", "10)", "  -", "    -", "\t-"}
	quotes := []string{"> q", "> q\n  > r", "> > q", "> - i", ">q", "> q\n  lazy", "> q\n\tlazy"}
	var cases []string
	for _, lead := range []string{"- a", "1. a", "- x\n  - a", "* a"} {
		ind := "  "
		if strings.HasPrefix(lead, "1.") {
			ind = "   "
		}
		for _, q := range quotes {
			for _, it := range items {
				for _, tail := range []string{"", "\n\n" + ind + "c", "\n" + ind + "c", "\n\n\n" + ind + "c"} {
					body := ind + strings.ReplaceAll(q, "\n", "\n"+ind)
					cases = append(cases, lead+"\n"+body+"\n"+ind+it+tail, lead+"\n"+body+"\n"+it+tail)
				}
			}
		}
	}
	bad := 0
	for _, md := range cases {
		h1 := FromMarkdown(md)
		h2 := FromMarkdown(ToEditableMarkdown(h1))
		if h1 != h2 {
			if bad++; bad <= 15 {
				t.Errorf("md %q\nh1 %q\nh2 %q", md, h1, h2)
			}
		}
	}
	if bad > 15 {
		t.Errorf("... %d unstable of %d", bad, len(cases))
	}
}
