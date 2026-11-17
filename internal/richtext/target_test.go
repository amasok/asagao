package richtext

import (
	"regexp"
	"strings"
	"testing"
)

// layoutTag matches the tags Asana accepts only in tasks and project briefs
// (https://developers.asana.com/docs/rich-text): headings, rules, images and tables.
var layoutTag = regexp.MustCompile(`(?i)<(h[1-6]|hr|img|table|tr|td|object)\b`)

// A comment or a status update is sent without headings, rules or images, which Asana
// rejects there with 400 (#63); what the user wrote stays: a heading as a bold paragraph,
// a rule and an image as the text written.
func TestFromMarkdownForCommentsAndStatusUpdates(t *testing.T) {
	for _, tc := range []struct{ md, want, plain string }{
		{"# 見出し\n\n本文", "<body><strong>見出し</strong>\n\n本文</body>", "見出し\n\n本文"},
		{"## 小見出し\n本文", "<body><strong>小見出し</strong>\n本文</body>", "小見出し\n本文"},
		{"###### 六", "<body><strong>六</strong></body>", "六"},
		{"見出し\n===\n本文", "<body><strong>見出し</strong>\n本文</body>", "見出し\n本文"},
		{"# 一\n# 二\n\n# 三", "<body><strong>一</strong>\n<strong>二</strong>\n\n<strong>三</strong></body>", "一\n二\n\n三"},
		{"# **太字** と *斜体*", "<body><strong>太字 と <em>斜体</em></strong></body>", "太字 と 斜体"},
		{"a\n\n---\n\nb", "<body>a\n\n---\n\nb</body>", "a\n\n---\n\nb"},
		{"a\n***\nb", "<body>a\n***\nb</body>", "a\n***\nb"},
		{"* * *", "<body>* * *</body>", "* * *"},
		{"> 引用\n> ___\n> 続き", "<body><blockquote>引用\n___\n続き</blockquote></body>", "引用\n___\n続き"},
		{"- a\n\n  ***\n- b", "<body><ul><li>a\n***</li><li>b</li></ul></body>", "a\n***\nb"},
		{"> - a\n>\n>   * * *", "<body><blockquote>- a\n* * *</blockquote></body>", "- a\n* * *"},
		{"![画像](asana:12)", "<body>![画像](asana:12)</body>", "![画像](asana:12)"},
		{`![画像](asana:12 "題")`, `<body>![画像](asana:12 &#34;題&#34;)</body>`, `![画像](asana:12 "題")`},
		// a cached file is referred to by its gid: its path is local to this machine
		{"見て ![x](/home/u/.cache/asana-tui/attachments/55-a.png)", "<body>見て ![x](asana:55)</body>", "見て ![x](asana:55)"},
		{"a\n\n#\n\nb", "<body>a\n\nb</body>", "a\n\nb"},
		// FromMarkdown writes no <table> (Asana takes it only in tasks and briefs): a
		// Markdown table stays as the text written
		{"| 品 | 数 |\n|---|---|\n| 椅子 | 2 |", "<body>| 品 | 数 |\n|---|---|\n| 椅子 | 2 |</body>", "| 品 | 数 |\n|---|---|\n| 椅子 | 2 |"},
		{"- # 項目の見出し", "<body><ul><li># 項目の見出し</li></ul></body>", "# 項目の見出し"},
	} {
		for _, target := range []Target{Comment, StatusUpdate} {
			out := target.FromMarkdown(tc.md)
			if out != tc.want {
				t.Errorf("%v.FromMarkdown(%q)\ngot  %q\nwant %q", target, tc.md, out, tc.want)
			}
			if m := layoutTag.FindString(out); m != "" {
				t.Errorf("%v.FromMarkdown(%q) = %q: Asana rejects %s> there", target, tc.md, out, m)
			}
			if got := target.PlainFromMarkdown(tc.md); got != tc.plain {
				t.Errorf("%v.PlainFromMarkdown(%q) = %q, want %q", target, tc.md, got, tc.plain)
			}
			// the comment as the editor shows it again converts to the same rich text
			if again := target.FromMarkdown(ToEditableMarkdown(out)); again != out {
				t.Errorf("%v: %q -> %q -> %q", target, tc.md, ToEditableMarkdown(out), again)
			}
		}
	}
}

// What Asana accepts everywhere is written for a comment or a status update as it is for a
// task's notes.
func TestFromMarkdownForCommentsGeneralTagsAsTask(t *testing.T) {
	for _, md := range []string{
		"**太字** *斜体* ~~消し~~ `code` [リンク](https://x.jp) [@K](asana:user:7) https://y.jp",
		"- a\n- b\n\n1. c\n2. d",
		"> 引用\n> 二行目",
		"```\ncode\n```",
		"段落\n\n段落\n  字下げ",
		"> - リスト in 引用",
		"- > 引用 in リスト",
	} {
		want := TaskNotes.FromMarkdown(md)
		for _, target := range []Target{Comment, StatusUpdate} {
			if got := target.FromMarkdown(md); got != want {
				t.Errorf("%v.FromMarkdown(%q) = %q, want %q (as for a task)", target, md, got, want)
			}
			if got, want := target.PlainFromMarkdown(md), TaskNotes.PlainFromMarkdown(md); got != want {
				t.Errorf("%v.PlainFromMarkdown(%q) = %q, want %q (as for a task)", target, md, got, want)
			}
		}
	}
}

// A task's notes keep headings, rules and images as elements, as before #63.
func TestFromMarkdownForTaskNotesKeepsLayout(t *testing.T) {
	for md, want := range map[string]string{
		"# 見出し\n\n本文":       "<body><h1>見出し</h1>本文</body>",
		"## 小見出し":           "<body><h2>小見出し</h2></body>",
		"a\n***\nb":         "<body>a\n<hr/>b</body>",
		"![画像](asana:12)":   `<body><img data-asana-gid="12"/></body>`,
		"> - a\n>\n>   ***": "<body><blockquote>- a\n***</blockquote></body>", // a rule as text: as typed (#66)
	} {
		if got := TaskNotes.FromMarkdown(md); got != want {
			t.Errorf("TaskNotes.FromMarkdown(%q) = %q, want %q", md, got, want)
		}
	}
}

// Whatever is written, a comment or a status update carries no tag Asana rejects there and
// keeps every word of it.
func TestFromMarkdownForCommentsNoLayoutTags(t *testing.T) {
	parts := []string{"# 見出し", "## 二", "見出し\n---", "見出し\n===", "---", "***", "___", "- - -",
		"![画像](asana:9)", "| 表 | 列 |\n|---|---|\n| 値 | 二 |", "> 引用", "- 項目", "1. 番号", "```\nコード\n```", "本文", "", "    字下げ"}
	words := regexp.MustCompile(`[\p{Han}\p{Hiragana}\p{Katakana}]+`)
	for i, a := range parts {
		for j, b := range parts {
			for _, sep := range []string{"\n", "\n\n", "\n> ", "\n- ", "\n  "} {
				md := a + sep + b
				for _, target := range []Target{Comment, StatusUpdate} {
					out := target.FromMarkdown(md)
					if m := layoutTag.FindString(out); m != "" {
						t.Fatalf("parts %d,%d: %v.FromMarkdown(%q) = %q has %s>", i, j, target, md, out, m)
					}
					for _, w := range words.FindAllString(md, -1) {
						if !strings.Contains(out, w) {
							t.Fatalf("%v.FromMarkdown(%q) = %q lost %q", target, md, out, w)
						}
					}
					if again := target.FromMarkdown(ToEditableMarkdown(out)); again != out {
						t.Fatalf("%v: %q -> %q -> %q", target, md, ToEditableMarkdown(out), again)
					}
				}
			}
		}
	}
}

func TestConverterTaskNotes(t *testing.T) {
	src := "# 見出し\n\n本文 **太字**\n\n---\n"
	html, plain := Converter{}.TaskNotes(src)
	if want := TaskNotes.FromMarkdown(src); html != want {
		t.Errorf("html = %q, want %q", html, want)
	}
	if want := TaskNotes.PlainFromMarkdown(src); plain != want {
		t.Errorf("plain = %q, want %q", plain, want)
	}
	if html == plain {
		t.Errorf("html and plain are the same: %q", html)
	}
}

// Rich text queued for a comment or a status update before #63 is fitted to what Asana
// accepts there, as FromMarkdown writes it now; task notes and rich text without layout tags
// are kept (#71).
func TestFitLayoutTags(t *testing.T) {
	md := "# 予定\n\n明日やる\n\n---\n\n## 準備\n\n![図](asana:42)"
	old := TaskNotes.FromMarkdown(md)
	for _, target := range []Target{Comment, StatusUpdate} {
		got, plain, changed := target.Fit(old)
		want := target.FromMarkdown(ToEditableMarkdown(old))
		if !changed || got != want || plain != target.PlainFromMarkdown(ToEditableMarkdown(old)) {
			t.Errorf("%v: Fit = %q %q %v, want %q", target, got, plain, changed, want)
		}
		for _, tag := range []string{"<h1", "<h2", "<hr", "<img"} {
			if strings.Contains(got, tag) {
				t.Errorf("%v: %s left in %q", target, tag, got)
			}
		}
	}
	for _, c := range []struct {
		target Target
		html   string
	}{
		{TaskNotes, old},
		{Comment, "<body><strong>a</strong> &lt;h1&gt; <code>&lt;hr/&gt;</code></body>"},
		{Comment, ""},
		{StatusUpdate, "plain text"},
	} {
		if got, _, changed := c.target.Fit(c.html); changed || got != c.html {
			t.Errorf("%v: Fit(%q) = %q, %v; want it kept", c.target, c.html, got, changed)
		}
	}
	// a table or an h3 (not written by FromMarkdown, but rejected all the same)
	for _, h := range []string{"<body><h3>x</h3></body>", "<body><table><tr><td>x</td></tr></table></body>"} {
		if got, _, changed := Comment.Fit(h); !changed || strings.Contains(got, "<h3") || strings.Contains(got, "<t") || !strings.Contains(got, "x") {
			t.Errorf("Fit(%q) = %q, %v", h, got, changed)
		}
	}
}
