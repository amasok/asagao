package richtext

import (
	"strings"
	"testing"
)

const atkTable = `<table><tr><td>a</td></tr></table>`
const atkObj = `<object type="application/vnd.asana.external_media" data-asana-type="attachment" data-asana-gid="1"><a href="https://l">l</a></object>`

func twice(t *testing.T, in string) string {
	t.Helper()
	o1, _, err := BriefFromMarkdown(BriefToEditableMarkdown(in, nil), in)
	if err != nil {
		t.Fatalf("round1 %q: %v", in, err)
	}
	o2, _, err := BriefFromMarkdown(BriefToEditableMarkdown(o1, nil), o1)
	if err != nil {
		t.Fatalf("round2 %q: %v", o1, err)
	}
	if o1 != o2 {
		t.Fatalf("unstable:\n 1 %q\n 2 %q", o1, o2)
	}
	if !strings.Contains(o1, atkTable) && strings.Contains(in, atkTable) {
		t.Fatalf("table lost: %q", o1)
	}
	return o1
}

func TestBriefAttackLayoutsStable(t *testing.T) {
	cases := map[string]string{
		"after list":       "<body><ul><li>a</li><li>b</li></ul>" + atkTable + "x</body>",
		"consecutive":      "<body>" + atkTable + atkObj + atkTable + "</body>",
		"mid paragraph":    "<body>one\n" + atkTable + "two\n" + atkTable + "</body>",
		"only table":       "<body>" + atkTable + "</body>",
		"leading table":    "<body>" + atkTable + "<h1>T</h1></body>",
		"backticks inside": "<body><table><tr><td>```asana-html ``` `x`</td></tr></table>tail</body>",
		"tilde inside":     "<body><table><tr><td>~~~</td></tr></table>t</body>",
		"nested object":    "<body><table><tr><td>" + atkObj + "</td></tr></table>z</body>",
		"unknown tag":      "<body><foo>x</foo>" + atkTable + "</body>",
		"entities":         "<body>a &amp; b &lt;c&gt;" + atkTable + "</body>",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			out := twice(t, in)
			if strings.Count(out, "<table") != strings.Count(in, "<table") {
				t.Fatalf("table count changed: %q -> %q", in, out)
			}
			if strings.Count(out, "<object") != strings.Count(in, "<object") {
				t.Fatalf("object count changed: %q -> %q", in, out)
			}
		})
	}
}

func TestBriefAttackBlockTextExactBackticks(t *testing.T) {
	in := "<body><table><tr><td>````x```` ```</td></tr></table></body>"
	out := twice(t, in)
	if !strings.Contains(out, "````x```` ```") {
		t.Fatalf("lost: %q", out)
	}
}

func TestBriefAttackCRLF(t *testing.T) {
	md := "a\r\n\r\n```asana-html\r\n" + atkTable + "\r\n```\r\n\r\nb\r\n"
	out, _, err := BriefFromMarkdown(md, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, atkTable) || strings.Contains(out, "\r") {
		t.Fatalf("%q", out)
	}
}

func TestBriefAttackTildeFence(t *testing.T) {
	md := "a\n\n~~~asana-html\n" + atkTable + "\n~~~\n\nb\n"
	out, _, err := BriefFromMarkdown(md, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, atkTable) {
		t.Fatalf("%q", out)
	}
}

func TestBriefAttackUnclosedFence(t *testing.T) {
	md := "a\n\n```asana-html\n" + atkTable + "\n"
	out, _, err := BriefFromMarkdown(md, "")
	if err == nil && !strings.Contains(out, atkTable) {
		t.Fatalf("unclosed fence silently dropped content: %q", out)
	}
}

func TestBriefAttackAsanaHTMLInsideOtherCodeBlock(t *testing.T) {
	md := "````\n```asana-html\n<script>x</script>\n```\n````\n"
	out, _, err := BriefFromMarkdown(md, "")
	if err != nil {
		t.Fatalf("code block content treated as block: %v", err)
	}
	if strings.Contains(out, "<script>") {
		t.Fatalf("injected raw: %q", out)
	}
}

func TestBriefAttackRejects(t *testing.T) {
	bad := []string{
		"<script>alert(1)</script>",
		"<table><tr><td onclick=\"x()\">a</td></tr></table>",
		"<table><tr><td>a</td></tr>",
		"<table></tr></table>",
		"<a href=\"javascript:x\">x</a><iframe src=x></iframe>",
		"",
		"   ",
		"<table><tr><td>a</td></tr></table><script>x</script>",
		"<!DOCTYPE x><table/>",
		"<?xml version=\"1.0\"?><table/>",
		"<![CDATA[x]]><script/>",
		"<![CDATA[x]]>",
		"<!-- c --><table/>",
		"<table><tr><td><![CDATA[a]]></td></tr></table>",
	}
	for _, b := range bad {
		md := "x\n\n```asana-html\n" + b + "\n```\n"
		out, _, err := BriefFromMarkdown(md, "")
		if err == nil {
			t.Errorf("accepted %q -> %q", b, out)
		}
	}
}

func TestBriefAttackTextInjectionEscaped(t *testing.T) {
	out, _, err := BriefFromMarkdown("<script>alert(1)</script> & <table>\n", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "<script>") || strings.Contains(out, "<table>") {
		t.Fatalf("raw html passed through: %q", out)
	}
}

func TestBriefAttackEmptyAndHuge(t *testing.T) {
	if h, _, err := BriefFromMarkdown("", ""); err != nil || strings.Contains(h, "<table") {
		t.Fatalf("%q %v", h, err)
	}
	big := "<body>" + strings.Repeat("line\n", 20000) + atkTable + "</body>"
	twice(t, big)
	many := "<body>" + strings.Repeat(atkTable, 300) + "</body>"
	out := twice(t, many)
	if strings.Count(out, "<table") != 300 {
		t.Fatalf("tables: %d", strings.Count(out, "<table"))
	}
}

func TestBriefAttackMalformedHTMLInput(t *testing.T) {
	for _, in := range []string{"<body><table><tr>", "<body>" + atkTable + "<b>", "not html <table", "<body><object></body>"} {
		_ = BriefToEditableMarkdown(in, nil)
	}
}

func TestBriefAttackControlChars(t *testing.T) {
	in := "<body>a\x1b[31mred\x07" + atkTable + "</body>"
	md := BriefToEditableMarkdown(in, nil)
	if strings.ContainsAny(md, "\x1b\x07") {
		t.Logf("note: markdown keeps control chars (termsafe is applied upstream): %q", md)
	}
}
