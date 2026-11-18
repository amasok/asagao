package richtext

import (
	"strings"
	"testing"
)

func briefHTML(t *testing.T, md string) string {
	t.Helper()
	out, _, err := BriefFromMarkdown(md, "")
	if err != nil {
		t.Fatalf("%q: %v", md, err)
	}
	return out
}

func TestBriefAttackTableConverted(t *testing.T) {
	out := briefHTML(t, "intro\n\n| a | b |\n| --- | :-: |\n| 1 | **2** |\n| 3 |\n\nafter\n")
	if strings.Count(out, "<table>") != 1 || strings.Count(out, "<tr>") != 3 || strings.Count(out, "<td>") != 6 {
		t.Fatalf("%q", out)
	}
	if !strings.Contains(out, "<strong>2</strong>") || !strings.Contains(out, "after") {
		t.Fatalf("%q", out)
	}
}

func TestBriefAttackNotATable(t *testing.T) {
	cases := map[string]string{
		"pipe in paragraph":  "a | b\n\ntext | more\n",
		"no blank before":    "para\n| a | b |\n| --- | --- |\n| 1 | 2 |\n",
		"in list":            "- item\n\n  | a | b |\n  | --- | --- |\n  | 1 | 2 |\n",
		"indented code":      "x\n\n    | a | b |\n    | --- | --- |\n",
		"fenced code":        "```\n\n| a | b |\n| --- | --- |\n| 1 | 2 |\n```\n",
		"width mismatch":     "x\n\n| a | b |\n| --- |\n| 1 | 2 |\n",
		"escaped pipes only": "x\n\na \\| b\n--- | ---\n",
		"setext-ish":         "x\n\nTitle\n---\n",
	}
	for name, md := range cases {
		out, _, err := BriefFromMarkdown(md, "")
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if strings.Contains(out, "<table>") {
			t.Errorf("%s: became a table: %q", name, out)
		}
	}
}

func TestBriefAttackTableCellInjection(t *testing.T) {
	out := briefHTML(t, "x\n\n| a | b |\n| --- | --- |\n| <script>x</script> | a \\| b |\n")
	if strings.Contains(out, "<script>") {
		t.Fatalf("raw html passed: %q", out)
	}
	if !strings.Contains(out, "a | b") {
		t.Fatalf("escaped pipe lost: %q", out)
	}
	if _, _, err := BriefFromMarkdown("x\n\n| a |\n| --- |\n| <b>x |\n", ""); err != nil {
		t.Fatal(err)
	}
}

func TestBriefAttackTableCellBlockStaysText(t *testing.T) {
	out := briefHTML(t, "x\n\n| a | b |\n| --- | --- |\n| # h | - li |\n")
	if strings.Contains(out, "<h1") || strings.Contains(out, "<ul") {
		t.Fatalf("%q", out)
	}
}

func TestBriefAttackTableAtEnd(t *testing.T) {
	out := briefHTML(t, "| a | b |\n| --- | --- |\n| 1 | 2 |")
	if out == "" {
		t.Fatal("empty")
	}
	// a table at the very start has no blank before it: still must not crash or lose text
	if !strings.Contains(out, "1") || !strings.Contains(out, "2") {
		t.Fatalf("%q", out)
	}
}

func TestBriefAttackImages(t *testing.T) {
	cases := map[string]string{
		`<img data-asana-gid="5" src="https://x" style="a"></img>`: `<img data-asana-gid="5"/>`,
		`<IMG src="x" DATA-ASANA-GID="6"/>`:                        `<img data-asana-gid="6"/>`,
		`<img data-asana-gid="7"/>`:                                `<img data-asana-gid="7"/>`,
		`<img src="x" data-asana-gid="8" width="3" height="4"/>`:   `<img data-asana-gid="8"/>`,
	}
	for in, want := range cases {
		md := "x\n\n```asana-html\n<table><tr><td>" + in + "</td></tr></table>\n```\n"
		out, _, err := BriefFromMarkdown(md, "")
		if err != nil {
			t.Errorf("%s: %v", in, err)
			continue
		}
		if !strings.Contains(out, want) || strings.Contains(out, "src=") || strings.Contains(out, "</img>") {
			t.Errorf("%s -> %q (want %s)", in, out, want)
		}
	}
	for _, bad := range []string{`<img src="x"/>`, `<img data-asana-gid="" />`, `<img/>`} {
		md := "x\n\n```asana-html\n<table><tr><td>" + bad + "</td></tr></table>\n```\n"
		if out, _, err := BriefFromMarkdown(md, ""); err == nil && strings.Contains(out, "<img") {
			t.Errorf("%s accepted: %q", bad, out)
		}
	}
}

// An unchanged original block passes without checks, even one with src.
func TestBriefAttackOriginalBlockUnchecked(t *testing.T) {
	orig := `<body><table><tr><td><img src="https://x" data-asana-gid="9"/></td></tr></table></body>`
	md := BriefToEditableMarkdown(orig, nil)
	out, _, err := BriefFromMarkdown(md, orig)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "<table>") {
		t.Fatalf("%q", out)
	}
}
