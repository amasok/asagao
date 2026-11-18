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
	out, plain, err := BriefFromMarkdown(md)
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
	if again, _, _ := BriefFromMarkdown(BriefToEditableMarkdown(out, nil)); again != out {
		t.Fatalf("second round:\n got %q\nwant %q", again, out)
	}
}

// Headings (h1, h2), rules and images are a brief's too, as in a task description.
func TestBriefRoundTripLayout(t *testing.T) {
	in := "<body><h1>A</h1><h2>B</h2>text<hr/><img data-asana-gid=\"55\"/>tail</body>"
	out, _, err := BriefFromMarkdown(BriefToEditableMarkdown(in, nil))
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
	out, _, err := BriefFromMarkdown(md)
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
		if _, _, err := BriefFromMarkdown("```asana-html\n" + raw + "\n```"); err == nil {
			t.Errorf("%q: no error", raw)
		}
	}
}

// Text that only looks like a block stays text: an asana-html fence inside another fenced
// block, or HTML typed in a paragraph.
func TestBriefFencesOnlyAtTop(t *testing.T) {
	md := "````\n```asana-html\n<table></table>\n```\n````\n\n<table><tr><td>t</td></tr></table>"
	out, _, err := BriefFromMarkdown(md)
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
	h, p, _ := BriefFromMarkdown(md)
	if h != TaskNotes.FromMarkdown(md) || p != TaskNotes.PlainFromMarkdown(md) {
		t.Fatalf("got %q %q", h, p)
	}
}
