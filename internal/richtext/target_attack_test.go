package richtext

import (
	"fmt"
	"math/rand"
	"regexp"
	"strings"
	"testing"
)

// genTargetBlock builds Markdown with headings (plain, setext, indented, with inline marks),
// rules in every spelling, images and containers, each word unique (wNw).
func genTargetBlock(r *rand.Rand, d int, n *int) []string {
	var lines []string
	next := func() string { *n++; return fmt.Sprintf("w%dw", *n) }
	for i := 1 + r.Intn(3); i > 0; i-- {
		w := next()
		switch k := r.Intn(22); k {
		case 0:
			lines = append(lines, w)
		case 1:
			lines = append(lines, "# "+w)
		case 2:
			lines = append(lines, "## **"+w+"** *"+next()+"* [l"+next()+"](https://x.jp/a_b)")
		case 3:
			lines = append(lines, "###### "+w+" `"+next()+"`")
		case 4:
			lines = append(lines, w, [...]string{"===", "---", "-----"}[r.Intn(3)])
		case 5:
			lines = append(lines, "   # "+w)
		case 6:
			lines = append(lines, "", [...]string{"---", "***", "___", "- - -", "* * *", " ***", "_ _ _"}[r.Intn(7)], "")
		case 7:
			lines = append(lines, "![a"+w+"](asana:"+fmt.Sprint(r.Intn(1000))+")")
		case 8:
			lines = append(lines, "# ![x"+w+"](asana:5) "+next())
		case 9:
			lines = append(lines, "![a [b"+w+"] \\] c](asana:7)")
		case 10:
			lines = append(lines, "# [@x](asana:user:9) "+w)
		case 11:
			lines = append(lines, "#")
		case 12:
			lines = append(lines, "- "+w)
		case 13:
			lines = append(lines, "1. "+w)
		case 14:
			lines = append(lines, "# "+w+" #")
		case 15:
			lines = append(lines, "<h1>"+w+"</h1>", "<hr/>", `<img src="x">`)
		case 16, 17:
			if d < 3 {
				for _, l := range genTargetBlock(r, d+1, n) {
					lines = append(lines, "> "+l)
				}
			}
		case 18, 19:
			if d < 3 {
				lines = append(lines, "- "+w)
				for _, l := range genTargetBlock(r, d+1, n) {
					lines = append(lines, "  "+l)
				}
			}
		case 20:
			lines = append(lines, "```", "# "+w, "---", "```")
		default:
			lines = append(lines, "")
		}
	}
	return lines
}

var tagRe = regexp.MustCompile(`<[^>]*>`)

// adjacentLists matches a list right after another, which the next trip makes one (#61).
var adjacentLists = regexp.MustCompile(`</(?:ul|ol)><(?:ul|ol)>`)
var wRe = regexp.MustCompile(`w\d+w`)

func TestTargetRandomAttack(t *testing.T) {
	r := rand.New(rand.NewSource(63))
	for i := 0; i < 4000; i++ {
		n := 0
		in := strings.Join(genTargetBlock(r, 0, &n), "\n")
		task := TaskNotes.FromMarkdown(in)
		taskStable := TaskNotes.FromMarkdown(ToEditableMarkdown(task)) == task
		for _, target := range []Target{Comment, StatusUpdate} {
			out := target.FromMarkdown(in)
			if m := layoutTag.FindString(out); m != "" {
				// raw HTML in the input is written as text (escaped), so only real tags count
				t.Fatalf("%v layout tag %s> in\nin  %q\nout %q", target, m, in, out)
			}
			if v := specViolations(out); len(v) > 0 {
				t.Fatalf("%v spec: %v\nin  %q\nout %q", target, v, in, out)
			}
			if a, b := strings.Join(wRe.FindAllString(in, -1), ","), strings.Join(wRe.FindAllString(out, -1), ","); a != b {
				t.Fatalf("%v words changed\nin  %q\nout %q", target, in, out)
			}
			// plain text carries the same words as the html
			plain := target.PlainFromMarkdown(in)
			if a, b := strings.Join(wRe.FindAllString(plain, -1), ","), strings.Join(wRe.FindAllString(out, -1), ","); a != b {
				t.Fatalf("%v plain words differ\nin    %q\nplain %q\nout   %q", target, in, plain, out)
			}
			// only where a task's notes are stable: nested lists and raw HTML are not always, on main
			// too; nor are lists next to each other (#61), which an empty heading between them
			// (no text outside a task's notes) leaves here
			if again := target.FromMarkdown(ToEditableMarkdown(out)); again != out && taskStable &&
				!adjacentLists.MatchString(out) {
				t.Fatalf("%v unstable\nin    %q\nout   %q\nedit  %q\nagain %q", target, in, out, ToEditableMarkdown(out), again)
			}
			if strings.Count(out, "<body>") != 1 || !strings.HasPrefix(out, "<body>") || !strings.HasSuffix(out, "</body>") {
				t.Fatalf("%v bad body %q", target, out)
			}
		}
	}
}

// Headings and rules keep their inline content; links, mentions and marks survive.
func TestTargetHeadingInlinesAttack(t *testing.T) {
	for _, tc := range []struct{ md, want string }{
		{"# [リンク](https://x.jp)", `<body><strong><a href="https://x.jp">リンク</a></strong></body>`},
		{"## [@K](asana:user:7) です", ""},
		{"# a & b < c", "<body><strong>a &amp; b &lt; c</strong></body>"},
		{"# `code`", ""},
		{"# ![alt](asana:3)", "<body><strong>![alt](asana:3)</strong></body>"},
		{"![a\\]b](asana:3)", ""},
		{"![](asana:3)", ""},
		{"# \\# not", ""},
	} {
		for _, target := range []Target{Comment, StatusUpdate} {
			out := target.FromMarkdown(tc.md)
			if m := layoutTag.FindString(out); m != "" {
				t.Errorf("%q: %s> in %q", tc.md, m, out)
			}
			if tc.want != "" && out != tc.want {
				t.Errorf("%q = %q, want %q", tc.md, out, tc.want)
			}
			if again := target.FromMarkdown(ToEditableMarkdown(out)); again != out {
				t.Errorf("%q unstable: %q -> %q -> %q", tc.md, out, ToEditableMarkdown(out), again)
			}
		}
	}
}

// Markdown that is not a heading/rule/image must not be rewritten: same output for every target.
func TestTargetNoLayoutSameEverywhere(t *testing.T) {
	r := rand.New(rand.NewSource(64))
	atoms := []string{"a", "**b**", "- c", "1. d", "> e", "`f`", "[g](https://x.jp)", "~~h~~", "#hash", "a#b", "- - x", "\\---", "\\# x", "```\nx\n```", "", "    i", "&", "<j>"}
	for i := 0; i < 3000; i++ {
		var parts []string
		for j := r.Intn(5) + 1; j > 0; j-- {
			parts = append(parts, atoms[r.Intn(len(atoms))])
		}
		in := strings.Join(parts, "\n")
		want := TaskNotes.FromMarkdown(in)
		if layoutTag.MatchString(want) {
			continue
		}
		for _, target := range []Target{Comment, StatusUpdate} {
			if got := target.FromMarkdown(in); got != want {
				t.Fatalf("%v differs for %q\ngot  %q\nwant %q", target, in, got, want)
			}
		}
	}
}

// No Markdown pattern makes a table (or its rows) in any target.
func TestNoTableEver(t *testing.T) {
	for _, md := range []string{"|a|b|\n|-|-|\n|1|2|", "a | b\n--|--\n1 | 2", "> |a|b|\n> |-|-|", "- |a|b|\n  |-|-|", "# |a|b|\n|-|-|", "<table><tr><td>x</td></tr></table>"} {
		for _, target := range []Target{TaskNotes, Comment, StatusUpdate} {
			if m := regexp.MustCompile(`(?i)<(table|tr|td|th|tbody|thead)\b`).FindString(target.FromMarkdown(md)); m != "" {
				t.Errorf("%v %q -> %s>", target, md, m)
			}
		}
	}
}
