package richtext

import "testing"

// Review follow-up for #12/#14: what ends a paragraph before a setext underline, and an
// <img> with neither gid nor alt.
func TestSetextParagraphEndersAttack(t *testing.T) {
	for _, src := range []string{
		"  a\n#tag\n---",
		"  a\n    # b\n---",
		"  a\n# b\n---",
		"  a\n#\n---",
		"  a\n####### b\n---",
		"  a\n   ```\n  b\n---",
		"  a\n    ```\n---",
		"  a\n~~~\n---",
		"  a\n***\n  ---",
		"  a\n  - b\n---",
		"  a\n  1. b\n---",
		"  a\n  2. b\n---",
		"  a\n> b\n---",
		"  a\n  b\n  #\n  c\n  ---",
	} {
		h1 := FromMarkdown(src)
		md := ToEditableMarkdown(h1)
		if h2 := FromMarkdown(md); h1 != h2 {
			t.Errorf("src %q\nh1 %q\nmd %q\nh2 %q", src, h1, md, h2)
		}
	}
	for src, want := range map[string]string{
		"  a\n#tag\n---": "<body><h2>a #tag</h2></body>",
	} {
		if got := FromMarkdown(src); got != want {
			t.Errorf("%q: got %q want %q", src, got, want)
		}
	}
}

func TestBareImgAttack(t *testing.T) {
	for _, h := range []string{
		`<ul><li>a` + "\n\n\t" + `<img src="x"><br>x</li></ul>`,
		`<ul><li>a` + "\n\n\t" + `<img><br>x</li></ul>`,
		`<ul><li>a` + "\n\n\t" + `<u><img src="x"></u><br>x</li></ul>`,
		`<ul><li>a` + "\n\n\t" + `<img alt="p" src="x"><br>x</li></ul>`,
		`<ul><li>a` + "\n\n\t" + `<img data-asana-gid="1"><br>x</li></ul>`,
		`<ul><li>a ` + `<img src="x">` + "\n" + `b</li></ul>`,
		`<ol><li>1.<img src="x"><br>!</li></ol>`,
	} {
		h = "<body>" + h + "</body>"
		md1, h1, md2, h2 := rt2(h)
		if h1 != h2 {
			t.Errorf("in %q\nmd1 %q\nh1 %q\nmd2 %q\nh2 %q", h, md1, h1, md2, h2)
		}
	}
}

// An HTML block start (types 1-6) ends a paragraph; type 7 (any tag) does not.
func TestSetextHTMLBlockEndersAttack(t *testing.T) {
	for _, src := range []string{
		"  a\n<div>\n---", "  a\n<!--\n---", "  a\n<script>\n---", "  a\n<pre>\n---", "  a\n<?x\n---",
		"  a\n<!DOCTYPE x>\n---", "  a\n<![CDATA[\n---", "  a\n<table>\n---", "  a\n<span>\n---",
		"  a\n<x>\n---", "  a\n    <div>\n---", "  a\n<div\n---", "  a\n</div>\n---", "  a\n<DIV>\n  ---",
	} {
		h1 := FromMarkdown(src)
		md := ToEditableMarkdown(h1)
		if h2 := FromMarkdown(md); h1 != h2 {
			t.Errorf("src %q\nh1 %q\nmd %q\nh2 %q", src, h1, md, h2)
		}
	}
}

func TestSetextLowercaseDeclarationAttack(t *testing.T) {
	for _, src := range []string{"  a\n<!x\n---", "  a\n<!X\n---", "  a\n<!-x\n---"} {
		h1 := FromMarkdown(src)
		if h2 := FromMarkdown(ToEditableMarkdown(h1)); h1 != h2 {
			t.Errorf("src %q: %q vs %q", src, h1, h2)
		}
	}
}
