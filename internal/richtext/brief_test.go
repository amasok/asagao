package richtext

import (
	"strings"
	"testing"
)

// A brief's tables and embeds survive editing: read as asana-html blocks and written back
// as they were (#60). Before, ToEditableMarkdown flattened a table to its text and dropped
// embeds.
func TestBriefRoundTripKeepsTablesAndEmbeds(t *testing.T) {
	table := `<table><tr><td data-cell-widths="150" width="150">a</td><td colspan="2"><strong>b</strong></td></tr></table>`
	media := `<object type="application/vnd.asana.external_media" data-asana-type="attachment" data-asana-gid="1234" data="https://e"><a href="https://l">https://l</a></object>`
	goals := `<object type="application/vnd.asana.project_goals" data-asana-gid="77" data-asana-type="project"></object>`
	in := "<body><h1>Title</h1>intro\n" + table + "after\n" + media + "<hr/>" + goals + "end</body>"
	md := BriefToEditableMarkdown(in, nil)
	for _, want := range []string{"# Title", "```asana-html\n" + table + "\n```", "```asana-html\n" + media + "\n```",
		"```asana-html\n" + goals + "\n```", "---", "intro", "after", "end"} {
		if !strings.Contains(md, want) {
			t.Fatalf("markdown lacks %q:\n%s", want, md)
		}
	}
	out, plain, err := BriefFromMarkdown(md, "")
	if err != nil {
		t.Fatal(err)
	}
	want := "<body><h1>Title</h1>intro\n" + table + "after\n" + media + "<hr/>" + goals + "end</body>"
	if out != want {
		t.Fatalf("html:\n got %q\nwant %q", out, want)
	}
	for _, w := range []string{"Title", "a\tb", "https://l", "end"} {
		if !strings.Contains(plain, w) {
			t.Fatalf("plain lacks %q: %q", w, plain)
		}
	}
	// and again: a second round changes nothing
	if again, _, _ := BriefFromMarkdown(BriefToEditableMarkdown(out, nil), out); again != out {
		t.Fatalf("second round:\n got %q\nwant %q", again, out)
	}
}

// Headings (h1, h2), rules and images are a brief's too, as in a task description.
func TestBriefRoundTripLayout(t *testing.T) {
	in := "<body><h1>A</h1><h2>B</h2>text<hr/><img data-asana-gid=\"55\"/>tail</body>"
	out, _, err := BriefFromMarkdown(BriefToEditableMarkdown(in, nil), in)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"<h1>A</h1>", "<h2>B</h2>", "<hr/>", `<img data-asana-gid="55"/>`} {
		if !strings.Contains(out, w) {
			t.Fatalf("%q lacks %q", out, w)
		}
	}
}

// A table edited in place is sent as edited.
func TestBriefTableEditedInPlace(t *testing.T) {
	md := "x\n\n```asana-html\n<table><tr><td>new</td></tr></table>\n```\n"
	out, _, err := BriefFromMarkdown(md, "")
	if err != nil {
		t.Fatal(err)
	}
	if out != "<body>x\n<table><tr><td>new</td></tr></table></body>" {
		t.Fatalf("got %q", out)
	}
}

// A block Asana would refuse is an error before anything is queued.
func TestBriefFromMarkdownRefusesBadBlocks(t *testing.T) {
	for _, raw := range []string{
		"<table><tr><td>a</td></tr>",           // not closed
		"<script>alert(1)</script>",            // not a brief's element
		`<table onclick="x"><tr></tr></table>`, // not an attribute it takes
		"",
	} {
		if _, _, err := BriefFromMarkdown("```asana-html\n"+raw+"\n```", ""); err == nil {
			t.Errorf("%q: no error", raw)
		}
	}
}

// Text that only looks like a block stays text: an asana-html fence inside another fenced
// block, or HTML typed in a paragraph.
func TestBriefFencesOnlyAtTop(t *testing.T) {
	md := "````\n```asana-html\n<table></table>\n```\n````\n\n<table><tr><td>t</td></tr></table>"
	out, _, err := BriefFromMarkdown(md, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "<table>") {
		t.Fatalf("raw HTML written: %q", out)
	}
}

// A brief without tables or embeds converts as a task description does.
func TestBriefWithoutBlocksIsTaskNotes(t *testing.T) {
	in := "<body><strong>b</strong> and <em>e</em>\n<ul><li>x</li></ul></body>"
	if got, want := BriefToEditableMarkdown(in, nil), ToEditableMarkdown(in); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	md := "**b** and *e*\n\n- x"
	h, p, _ := BriefFromMarkdown(md, "")
	if h != TaskNotes.FromMarkdown(md) || p != TaskNotes.PlainFromMarkdown(md) {
		t.Fatalf("got %q %q", h, p)
	}
}

// A table as Asana gave it goes back unchecked, whatever it holds (a <br> without an end
// tag, a list, attributes not in briefTags): editing only the text around it is not
// refused, and the table is sent as it was. A table changed here is checked (review of #60).
func TestBriefAsanaBlocksUnchecked(t *testing.T) {
	table := `<table><tr><td style="x" data-foo="1">a<br>b</td><td><ul><li>c</li></ul><p>d</p></td></tr></table>`
	orig := "<body><h1>見出し</h1>" + table + "</body>"
	md := BriefToEditableMarkdown(orig, nil)
	if !strings.Contains(md, "```asana-html\n"+table+"\n```") {
		t.Fatalf("md:\n%s", md)
	}
	out, _, err := BriefFromMarkdown(strings.Replace(md, "# 見出し", "# 新しい見出し", 1), orig)
	if err != nil {
		t.Fatalf("an edit beside Asana's table refused: %v", err)
	}
	if !strings.Contains(out, table) || !strings.Contains(out, "<h1>新しい見出し</h1>") {
		t.Fatalf("out = %q", out)
	}
	// the same table changed here is checked (style, <p>)
	changed := strings.Replace(md, ">a<br>", ">A<br>", 1)
	if _, _, err := BriefFromMarkdown(changed, orig); err == nil {
		t.Fatalf("a changed block not checked: %v", err)
	}
	// the block of another brief (or a new one) is checked too
	if _, _, err := BriefFromMarkdown(md, ""); err == nil {
		t.Fatal("a block not from the brief passed unchecked")
	}
}

// A block written here may use HTML's void elements and data-* attributes; tags must be
// closed in order.
func TestBriefCheckWrittenBlocks(t *testing.T) {
	for raw, ok := range map[string]bool{
		`<table><tr><td>a<br>b</td></tr></table>`:                              true,
		`<table><tr><td>a<br/>b<hr/></td></tr></table>`:                        true,
		`<table><tr><td data-cell-widths="150" data-x="1">a</td></tr></table>`: true,
		`<table><tr><td><ul><li>x</li></ul></td></tr></table>`:                 true,
		`<table><tr><td>a</tr></td></table>`:                                   false,
		`<table><tr><td>a</td></tr>`:                                           false,
		`<table><tr><td onclick="x">a</td></tr></table>`:                       false,
	} {
		_, _, err := BriefFromMarkdown("```asana-html\n"+raw+"\n```", "")
		if (err == nil) != ok {
			t.Errorf("%s: err = %v, want ok=%v", raw, err, ok)
		}
	}
}

// A Markdown table becomes a brief's <table> (Asana shows "| a | b |" as text otherwise,
// found on the real Asana, #60): the header is the first row, a cell's Markdown is inline
// rich text, "\|" is a pipe in a cell. A table not after a blank line, or inside a code
// block, stays as it is.
func TestBriefMarkdownTable(t *testing.T) {
	md := "前\n\n| 項目 | 金額 |\n| --- | ---: |\n| **家賃** | 10\\|万 |\n| 敷金 |\n\n後"
	out, plain, err := BriefFromMarkdown(md, "")
	if err != nil {
		t.Fatal(err)
	}
	want := "<body>前\n<table><tr><td>項目</td><td>金額</td></tr><tr><td><strong>家賃</strong></td><td>10|万</td></tr>" +
		"<tr><td>敷金</td><td></td></tr></table>後</body>"
	if out != want {
		t.Fatalf("got  %q\nwant %q", out, want)
	}
	if !strings.Contains(plain, "項目\t金額") {
		t.Fatalf("plain = %q", plain)
	}
	for _, keep := range []string{"段落\n| a | b |\n| - | - |", "```\n| a | b |\n| - | - |\n```", "| a | b |\nただの行"} {
		out, _, err := BriefFromMarkdown(keep, "")
		if err != nil || strings.Contains(out, "<table>") {
			t.Errorf("%q: %v %q", keep, err, out)
		}
	}
	// a heading in a cell is its text
	out, _, _ = BriefFromMarkdown("| # x |\n| - |", "")
	if !strings.Contains(out, "<td># x</td>") {
		t.Fatalf("heading cell: %q", out)
	}
}

// An image in a block is written as Asana takes it, <img data-asana-gid/> only, also in a
// block Asana gave (it reads it back with src, which it refuses when written: #60
// realcheck). An image written here without a gid is refused.
func TestBriefBlockImages(t *testing.T) {
	table := `<table><tr><td><img data-asana-gid="77" data-asana-type="attachment" src="https://x/y.png" style="w"></img>a</td></tr></table>`
	orig := "<body>" + table + "</body>"
	out, _, err := BriefFromMarkdown(BriefToEditableMarkdown(orig, nil), orig)
	if err != nil {
		t.Fatal(err)
	}
	if out != `<body><table><tr><td><img data-asana-gid="77"/>a</td></tr></table></body>` {
		t.Fatalf("out = %q", out)
	}
	if _, _, err := BriefFromMarkdown("```asana-html\n<table><tr><td><img src=\"https://x/y.png\"/></td></tr></table>\n```", ""); err == nil {
		t.Fatal("an image without a gid accepted")
	}
}
