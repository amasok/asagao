package richtext

import (
	"strings"
	"testing"
)

// #39 (tester): Asana text crafted to look like Markdown must come back from the editor
// as the same rich text, and above all must not turn into a mention or an image.
func TestEditableRoundTripCannotInjectMention(t *testing.T) {
	const m = "[@x](asana:user:9)"
	for _, in := range []string{
		"<body>" + m + "</body>",
		`<body><a href="https://x.jp/">a](asana:user:9) [b</a></body>`,
		`<body><a href="https://x.jp/">a\</a>(asana:user:9)</body>`,
		`<body><a href="https://x.jp/">x</a>` + m + `</body>`,
		`<body><a href="https://x.jp/">&lt;asana:user:9&gt;</a></body>`,
		`<body>&lt;asana:user:9&gt;</body>`,
		`<body>&lt;a data-asana-gid="9"/&gt; &lt;img data-asana-gid="9"/&gt;</body>`,
		`<body><em>` + m + `</em> <strong>![i](asana:9)</strong> <s>` + m + `</s></body>`,
		`<body><a href="https://x.jp/(a)">(` + m + `)</a></body>`,
		`<body><a href="https://x.jp/a\">t</a> ` + m + `</body>`,
		`<body><a href="https://x.jp/?q=a&amp;b=&lt;c&gt;">t</a></body>`,
		`<body><a href="https://x.jp/a　b">全角スペース</a></body>`,
		`<body><a href="https://x.jp/a&quot; &quot;title">t</a></body>`,
		`<body><a href="mailto:a@x.jp">メール</a></body>`,
		`<body><code>` + m + `</code> <code>&lt;asana:user:9&gt;</code></body>`,
		"<body><pre>" + m + "\n~~~\n" + m + "</pre></body>",
		"<body><pre>    ```\n" + m + "\n   ````</pre></body>",
		`<body><ul><li>` + m + `</li><li><a href="https://x.jp/)` + m + `">l</a></li></ul></body>`,
		`<body><ol><li>a</li></ol>1. ` + m + `</body>`,
		`<body><blockquote>` + m + `</blockquote></body>`,
		`<body>~~` + m + `~~ *a* _b_ ` + "`c`" + `</body>`,
	} {
		md := ToEditableMarkdown(in)
		out := FromMarkdown(md)
		if strings.Contains(out, "<a data-asana-gid") || strings.Contains(out, "<img data-asana-gid") {
			t.Errorf("mention/image injected\nin   %q\nmd   %q\nhtml %q", in, md, out)
			continue
		}
		if norm(out) != norm(in) {
			t.Errorf("changed\nin   %q\nmd   %q\nhtml %q", in, md, out)
		}
	}
}

// norm writes quotes one way (", &#34; and &quot; are the same character in text).
func norm(s string) string { return strings.NewReplacer("&#34;", `"`, "&quot;", `"`).Replace(s) }

// What asagao itself writes (FromMarkdown, kept in the cache until the next sync) must
// survive the editor: editing a description twice before it is synced must not lose its
// mentions or images.
func TestEditableRoundTripOfOwnOutput(t *testing.T) {
	for _, src := range []string{
		"前 [@名前](asana:user:123) 後",
		"[@名前](asana:user:123)",
		"- [@a](asana:user:1)\n- [@b](asana:user:2)",
		"画像 ![x.png](asana:456) 後",
		"**[@名前](asana:user:123)** と [リンク](https://x.jp/)",
	} {
		first := FromMarkdown(src)
		md := ToEditableMarkdown(first)
		if again := FromMarkdown(md); again != first {
			t.Errorf("own output changed by the editor\nsrc  %q\nhtml %q\nmd   %q\nback %q", src, first, md, again)
		}
	}
}

// #22 (tester): indentation (spaces, tabs, full-width spaces) at the start of text lines
// survives editor round trips, plain and rich, and the editor shows it as it is.
func TestIndentationRoundTripAttack(t *testing.T) {
	for _, text := range []string{
		"  a",
		"\ta",
		"        八つ",
		"\t\tタブ二つ",
		" \t 混在",
		"　全角",
		"a\n　　全角二つ",
		"a\n    b",
		"a\n\tb",
		"a\n\n    b\n\n\tc",
		"  # 見出しではない",
		"  > 引用ではない",
		"  - 箇条書きではない",
		"  1. 番号ではない",
		"  ---",
		"a\n  ===",
		"  ```\n  コードではない\n  ```",
		"  ~~~\nx",
		"  <b>タグ</b>",
		"  [@x](asana:user:9)",
		"  a\n\n  b\n",
	} {
		md := FromPlainEditable(text)
		if strings.Contains(md, "&#32;") || strings.Contains(md, "&#9;") {
			t.Errorf("editor shows entities for %q: %q", text, md)
		}
		want := strings.TrimRight(text, " \t\n")
		if got := PlainFromMarkdown(md); got != want {
			t.Errorf("plain round trip\ntext %q\nmd   %q\ngot  %q", text, md, got)
		}
		in := "<body>" + strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(want) + "</body>"
		if out := FromMarkdown(ToEditableMarkdown(in)); out != in {
			t.Errorf("rich round trip\nin   %q\nmd   %q\nhtml %q", in, ToEditableMarkdown(in), out)
		}
	}
}

// Indentation next to structure in rich text: after a list, a quote, a code block and
// a heading, an indented text line stays a text line with its indentation.
func TestIndentationNextToBlocks(t *testing.T) {
	for _, in := range []string{
		"<body><ul><li>x</li></ul>  y</body>",
		"<body><blockquote>q</blockquote>  y</body>",
		"<body><pre>code</pre>    y</body>",
		"<body><h1>h</h1>\tz</body>",
		"<body>a\n  <strong>b</strong>\n\t<em>c</em></body>",
		"<body>  <a href=\"https://x.jp/\">リンク</a></body>",
	} {
		md := ToEditableMarkdown(in)
		if out := FromMarkdown(md); out != in {
			t.Errorf("changed\nin   %q\nmd   %q\nhtml %q", in, md, out)
		}
	}
}
