package richtext

import (
	"html"
	"strings"
	"testing"
	"time"
)

func TestToMarkdown(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
	}{
		{"empty", "", ""},
		{"plain with newlines",
			"<body>めっちゃ見にくいな\n\nこれ\nつぎの行</body>",
			"めっちゃ見にくいな\n\nこれ  \nつぎの行"},
		{"inline styles",
			"<body><strong>太字</strong>と<em>斜体</em>と<s>取り消し</s>と<code>x := 1</code>と<u>下線</u></body>",
			"**太字**と*斜体*と~~取り消し~~と`x := 1`と下線"},
		{"heading and rule",
			"<body><h1>見出し</h1>本文<hr/>後</body>",
			"# 見出し\n\n本文\n\n---\n\n後"},
		{"h2",
			"<body><h2>小見出し</h2>本文</body>",
			"## 小見出し\n\n本文"},
		{"h1 h2 h3 levels",
			"<body><h1>一</h1><h2>二</h2><h3>三</h3></body>",
			"# 一\n\n## 二\n\n### 三"},
		{"lists",
			"<body>買うもの\n<ul>\n<li>牛乳</li>\n<li>卵<ol><li>赤</li><li>白</li></ol></li>\n</ul>以上</body>",
			"買うもの\n\n- 牛乳\n- 卵\n   1. 赤\n   2. 白\n\n以上"},
		{"link text starting with @ is a mention",
			`<body><a href="https://app.asana.com/0/1">@Hanako</a> と <a href="https://example.com"> @Taro </a></body>`,
			"**@Hanako** と **@Taro**"},
		{"@ in the middle of link text is not a mention",
			`<body><a href="mailto:a@example.com">a@example.com</a></body>`,
			"[a@example.com](mailto:a@example.com)"},
		{"links and mentions",
			`<body><a href="https://example.com">サイト</a> <a href="https://x.jp">https://x.jp</a> <a data-asana-type="user" data-asana-gid="1" href="https://app.asana.com/0/1">@Hanako</a></body>`,
			"[サイト](https://example.com) <https://x.jp> **@Hanako**"},
		{"image in place",
			`<body>これ<img data-asana-gid="1220507679870013" src="https://app.asana.com/app/asana/-/get_asset?asset_id=1220507679870013">これ</body>`,
			"これ\n\n⟦img:1220507679870013⟧\n\nこれ"},
		{"image by src only",
			`<body><img src="https://app.asana.com/app/asana/-/get_asset?asset_id=42"></body>`,
			"⟦img:42⟧"},
		{"escapes markdown in text",
			"<body>*注意* [メモ] 1_2\n# ではない\n- でもない</body>",
			`\*注意\* \[メモ\] 1\_2  ` + "\n" + `\# ではない  ` + "\n" + `\- でもない`},
		{"escapes block syntax at line start",
			"<body>1. 番号\n12) 括弧\n3.\n-- -\n+ プラス\n> 引用\n  # 字下げ\n-\n---\n===\n~~消し~~ と 10時~12時</body>",
			`1\. 番号  ` + "\n" + `12\) 括弧  ` + "\n" + `3\.  ` + "\n" + `\-- -  ` + "\n" + `\+ プラス  ` + "\n" + `\> 引用  ` + "\n" + `&#32; # 字下げ  ` + "\n" + `\-  ` + "\n" + `\---  ` + "\n" + `\===  ` + "\n" + `\~\~消し\~\~ と 10時\~12時`},
		{"indentation is kept with a character reference",
			"<body>前\n\t# 井桁\n\t- ハイフン\n \t+ プラス\n\t1. 番号\n\t> 引用\n    コード風\n-\t-\t-\n--\t-\n見出し風\n===\t\n後</body>",
			"前  \n&#9;# 井桁  \n&#9;- ハイフン  \n&#32;\t+ プラス  \n&#9;1. 番号  \n&#9;> 引用  \n&#32;   コード風  \n\\-\t-\t-  \n\\--\t-  \n見出し風  \n\\===&#9;  \n後"},
		{"indentation only at real line starts",
			"<body><strong>太字</strong>  後ろ\n<ul><li>  字下げ項目</li></ul><ol><li>\t番号項目</li></ol><blockquote>引用\n  字下げ</blockquote><blockquote>  先頭から字下げ</blockquote></body>",
			"**太字**  後ろ\n\n- &#32; 字下げ項目\n\n1. &#9;番号項目\n\n> 引用  \n> &#32; 字下げ\n\n> &#32; 先頭から字下げ"},
		{"whitespace-only lines stay blank",
			"<body>前\n  \n\t\n後</body>",
			"前  \n\n後"}, // the trailing "  " before a blank line is harmless (as before)
		{"character references in text stay as written",
			"<body>A&amp;amp;B &amp;copy; &amp;#32; &amp; ; &amp;foo</body>",
			`A\&amp;B \&copy; \&#32; & ; &foo`},
		{"does not escape look-alikes",
			"<body>1.5倍\n2020.1\n-5度\n+1\n=a\n-- x</body>",
			"1.5倍  \n2020.1  \n-5度  \n+1  \n=a  \n-- x"},
		{"pre",
			"<body><pre>a *b*\n  c</pre></body>",
			"```\na *b*\n  c\n```"},
		{"blockquote",
			"<body><blockquote>引用</blockquote>本文</body>",
			"> 引用\n\n本文"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ToMarkdown(tc.in); got != tc.want {
				t.Errorf("got\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}

func TestImages(t *testing.T) {
	got := Images(`<body><img data-asana-gid="1"><p>x</p><img src="https://app.asana.com/app/asana/-/get_asset?asset_id=2"></body>`)
	if len(got) != 2 || got[0] != "1" || got[1] != "2" {
		t.Fatalf("Images = %v", got)
	}
}

func TestFromMarkdown(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"paragraphs and breaks", "一行目\n二行目\n\n次の段落", "<body>一行目\n二行目\n\n次の段落</body>"},
		{"inline", "**太字** と *斜体* と ~~消す~~ と `code` と <tag> & 記号", "<body><strong>太字</strong> と <em>斜体</em> と <s>消す</s> と <code>code</code> と &lt;tag&gt; &amp; 記号</body>"},
		{"heading", "# 大\n## 中\n### 小\n本文", "<body><h1>大</h1><h2>中</h2><h2>小</h2>本文</body>"},
		{"lists", "買うもの\n\n- 牛乳\n- 卵\n  1. 赤\n  2. 白\n\n以上", "<body>買うもの\n<ul><li>牛乳</li><li>卵<ol><li>赤</li><li>白</li></ol></li></ul>以上</body>"},
		{"links", "[サイト](https://example.com) と https://x.jp", `<body><a href="https://example.com">サイト</a> と <a href="https://x.jp">https://x.jp</a></body>`},
		{"image and mention", "見て\n\n![画像](asana:123)\n\n[@Hanako](asana:user:9) さん", `<body>見て

<img data-asana-gid="123"/>

<a data-asana-gid="9"/> さん</body>`},
		{"character references", "A &amp; B &copy; &#32;x \\&amp; &foo; `&amp;`", "<body>A &amp; B ©  x &amp;amp; &amp;foo; <code>&amp;amp;</code></body>"},
		{"code and quote", "```\na < b\n```\n> 引用\n\n---\n後", "<body><pre>a &lt; b</pre><blockquote>引用</blockquote><hr/>後</body>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := FromMarkdown(tc.in); got != tc.want {
				t.Errorf("got\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}

// Editing round trip: Asana HTML -> editable Markdown -> Asana HTML keeps the content.
func TestEditableRoundTrip(t *testing.T) {
	in := `<body><h1>候補</h1><strong>予算</strong>は10万円
<ul><li>ニトリ</li><li>無印</li></ul>これ

<img data-asana-gid="1220507679870013" src="https://app.asana.com/app/asana/-/get_asset?asset_id=1220507679870013">

<a data-asana-type="user" data-asana-gid="9" href="https://app.asana.com/0/9">@Hanako</a> よろしく</body>`
	md := ToEditableMarkdown(in)
	wantMD := "# 候補\n\n**予算**は10万円\n\n- ニトリ\n- 無印\n\nこれ\n\n![画像](asana:1220507679870013)\n\n[@Hanako](asana:user:9) よろしく"
	if md != wantMD {
		t.Fatalf("editable markdown\n%q\nwant\n%q", md, wantMD)
	}
	// Mentions go back as a bare <a data-asana-gid/>: Asana fills in the name.
	wantHTML := `<body><h1>候補</h1><strong>予算</strong>は10万円
<ul><li>ニトリ</li><li>無印</li></ul>これ

<img data-asana-gid="1220507679870013"/>

<a data-asana-gid="9"/> よろしく</body>`
	if out := FromMarkdown(md); out != wantHTML {
		t.Fatalf("html\n%q\nwant\n%q", out, wantHTML)
	}
	if got, want := PlainFromMarkdown(md), "候補\n予算は10万円\nニトリ\n無印\nこれ\n\n\n\n@Hanako よろしく"; got != want {
		t.Fatalf("plain\n%q\nwant\n%q", got, want)
	}
}

// Markdown -> Asana HTML -> editable Markdown gives back exactly the same Markdown.
func TestMarkdownRoundTrip(t *testing.T) {
	for _, md := range []string{
		"# 大\n\n## 中\n\n本文",
		"**太字** と *斜体* と ~~消す~~ と `code` と \\*記号\\*  \n次の行",
		"- 牛乳\n- 卵\n   1. 赤\n   2. 白\n\n以上",
		"> 引用\n\n```\na < b\n```\n\n---\n\n後",
		"[サイト](https://example.com) と <https://x.jp>\n\n![画像](asana:123)",
		"前\n\n    字下げ  \n\tタブ  \n\\&amp; は文字",
		"  先頭の字下げ  \n次\n\n> 引用  \n>   字下げ引用",
		`1\. 番号ではない  ` + "\n" + `\+ リストではない  ` + "\n" + `\# 見出しではない  ` + "\n" + `\~\~消さない\~\~ 10時\~12時`,
	} {
		h := FromMarkdown(md)
		if back := ToEditableMarkdown(h); back != md {
			t.Errorf("round trip of %q via %q\ngot  %q", md, h, back)
		}
	}
}

// Plain text that looks like Markdown survives Asana HTML -> editable Markdown -> Asana HTML
// unchanged, and its plain-text form is the original text.
func TestPlainTextRoundTrip(t *testing.T) {
	for _, text := range []string{
		"*注意* [メモ] 1_2 a\\b `x` <tag>",
		"1. 番号\n2) 括弧\n10. 十",
		"- ハイフン\n+ プラス\n-\n> 引用\n# 井桁",
		"見出し風\n---\n見出し風\n===",
		"段落\n-- -\n3.\n4)",
		"段落\n--\t-\n後",
		"前\n\n\t- z",
		"前\n\n    コード風\n  二行目\n\t\tタブ二つ",
		"  先頭の字下げ\n項目\n\t# x\n \t> y",
		"A&amp;B &copy; &#32; & ; &foo; \\&amp;",
		"~~消し~~ と 10時~12時と14時~15時",
		"1.5倍\n-5度\n\n次の段落",
	} {
		in := "<body>" + html.EscapeString(text) + "</body>"
		md := ToEditableMarkdown(in)
		if out := FromMarkdown(md); out != in {
			t.Errorf("text %q\nmarkdown %q\nhtml %q\nwant %q", text, md, out, in)
		}
		if plain := PlainFromMarkdown(md); plain != text {
			t.Errorf("plain of %q = %q", md, plain)
		}
	}
}

// Tabs do not let text turn into Markdown blocks: a tab-indented "- " or "# " inside a list
// item stays text (with its tab) instead of becoming a nested list or a heading.
func TestTabsDoNotCreateBlocks(t *testing.T) {
	in := "<body><ul><li>項目\n\t- 子\n\t# 見出し\n\t1. 番号</li></ul></body>"
	want := in
	md := ToEditableMarkdown(in)
	if out := FromMarkdown(md); out != want {
		t.Fatalf("markdown %q\nhtml %q\nwant %q", md, out, want)
	}
	// "===" followed by a tab would make the line above a heading. The trailing tab is kept
	// (as a character reference: Markdown drops whitespace before a line break).
	in = "<body>見出し風\n===\t\n後</body>"
	want = in
	md = ToEditableMarkdown(in)
	if out := FromMarkdown(md); out != want {
		t.Fatalf("markdown %q\nhtml %q\nwant %q", md, out, want)
	}
}

func TestEditableImagesAsLocalPaths(t *testing.T) {
	in := `<body>前<img data-asana-gid="111">後</body>`
	path := "/Users/me/.cache/asana-tui/attachments/111-photo.png"
	md := ToEditableMarkdownWith(in, func(gid string) string {
		if gid == "111" {
			return path
		}
		return ""
	})
	if !strings.Contains(md, "![画像]("+path+")") {
		t.Fatalf("image should link to the local file: %s", md)
	}
	if out := FromMarkdown(md); !strings.Contains(out, `<img data-asana-gid="111"/>`) {
		t.Fatalf("local path should map back to the attachment: %s", out)
	}
	if md2 := ToEditableMarkdownWith(in, func(string) string { return "" }); !strings.Contains(md2, "(asana:111)") {
		t.Fatalf("fallback to asana: link: %s", md2)
	}
	for dest, want := range map[string]string{
		"asana:5": "5", "file:///x/attachments/77-a.png": "77", "/tmp/attachments/9-b c.jpg": "9",
	} {
		if gid, ok := ImageGID(dest); !ok || gid != want {
			t.Errorf("ImageGID(%q) = %q %v", dest, gid, ok)
		}
	}
	for _, dest := range []string{"https://example.com/a.png", "asana:user:3", "/tmp/photos/9-b.jpg"} {
		if _, ok := ImageGID(dest); ok {
			t.Errorf("ImageGID(%q) should be false", dest)
		}
	}
}

func TestEditableImagePathWithSpaces(t *testing.T) {
	path := "/Users/me/Library/Application Support/x/attachments/9-a.png"
	md := ToEditableMarkdownWith(`<body><img data-asana-gid="9"></body>`, func(string) string { return path })
	if md != "![画像](<"+path+">)" {
		t.Fatalf("md = %q", md)
	}
	if out := FromMarkdown(md); !strings.Contains(out, `<img data-asana-gid="9"/>`) {
		t.Fatalf("angle-bracket path should map back: %s", out)
	}
}

// Text from Asana cannot turn into Markdown of its own in editable Markdown: a crafted href,
// code or heading must not close its construct early and add a mention (which would notify
// the user when the description is saved). Each input comes back unchanged. (#39)
func TestEditableMarkdownDoesNotInjectMarkup(t *testing.T) {
	const mention = "[@x](asana:user:999)"
	for _, in := range []string{
		`<body><a href="https://x.jp/a)` + mention + `">リンク</a></body>`,
		`<body><a href="https://x.jp/a) ` + mention + ` (">リンク</a></body>`,
		`<body><a href="https://x.jp/&gt;` + mention + `&lt;">https://x.jp/&gt;` + mention + `&lt;</a></body>`,
		`<body><a href="https://x.jp/a b&gt;c">リンク</a></body>`,
		`<body><a href="https://x.jp/\)` + mention + `">リンク</a></body>`,
		`<body><a href="https://x.jp/a&amp;copy;b&amp;#41;">リンク</a></body>`,
		`<body><a href="https://x.jp/` + "`" + `a">リンク</a></body>`,
		"<body><pre>前\n```\n" + mention + "\n```\n後</pre></body>",
		"<body><pre>````\n" + mention + "\n~~~</pre></body>",
		`<body><a href="asana:user:999">リンク</a> <a href="asana:123">画像風</a></body>`,
		`<body>注意!<a href="asana:123">リンク</a> !<a href="/x/attachments/5-a.png">リンク</a></body>`,
		"<body><code>a` " + mention + " `b</code></body>",
		"<body><code>``x``</code> と <code> 空白 </code></body>",
		"<body><h1>見出し " + mention + "</h1>本文</body>",
		"<body><h2># *強調* `x`</h2></body>",
	} {
		md := ToEditableMarkdown(in)
		out := FromMarkdown(md)
		if strings.Contains(out, "data-asana-gid") {
			t.Errorf("mention injected\nin   %q\nmd   %q\nhtml %q", in, md, out)
			continue
		}
		if out != in {
			t.Errorf("changed\nin   %q\nmd   %q\nhtml %q", in, md, out)
		}
	}
}

// A code block inside a quote stays in the quote (#51), and its text cannot become
// Markdown, even with another code block later.
func TestCodeBlockInQuoteDoesNotInjectMarkup(t *testing.T) {
	in := "<body><blockquote>引用<pre>コード\n```\n[@x](asana:user:9)</pre>後</blockquote><pre>[@y](asana:user:8)</pre></body>"
	md := ToEditableMarkdown(in)
	out := FromMarkdown(md)
	if strings.Contains(out, "data-asana-gid") {
		t.Fatalf("mention injected\nmd   %q\nhtml %q", md, out)
	}
	for _, want := range []string{"<pre>コード\n```\n[@x](asana:user:9)</pre>", "<pre>[@y](asana:user:8)</pre>"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q\nmd   %q\nhtml %q", want, md, out)
		}
	}
}

// Inline code with line breaks do not leak Markdown either: a blank
// line in <code> would end the paragraph and leave the rest to be read as Markdown.
func TestInlineCodeWithBlankLine(t *testing.T) {
	in := "<body><code>a\n\n[@x](asana:user:9)</code></body>"
	out := FromMarkdown(ToEditableMarkdown(in))
	if strings.Contains(out, "data-asana-gid") {
		t.Fatalf("mention injected: %q", out)
	}
	if want := "<body><code>a  [@x](asana:user:9)</code></body>"; out != want {
		t.Fatalf("got %q want %q", out, want)
	}
}

// Indentation written as spaces or tabs in editable Markdown is kept as text, while
// indentation that is Markdown structure (list items, quotes, code) keeps its meaning. (#22)
func TestIndentationAsWhitespace(t *testing.T) {
	for _, tc := range []struct{ md, html string }{
		{"  前\n    四つ\n\tタブ", "<body>  前\n    四つ\n\tタブ</body>"},
		{"    四つ\n後", "<body>    四つ\n後</body>"},
		{"- a\n  続き\n  - 子\n\n本文", "<body><ul><li>a\n続き<ul><li>子</li></ul></li></ul>本文</body>"},
		{"1. a\n   b", "<body><ol><li>a\nb</li></ol></body>"},
		{"> 引用\n>   字下げ", "<body><blockquote>引用\n  字下げ</blockquote></body>"},
		{"```\n  コード\n```\n  後", "<body><pre>  コード</pre>  後</body>"},
		{"~~~\n  x\n~~~", "<body><pre>  x</pre></body>"},
		{"  # 見出し", "<body><h1>見出し</h1></body>"},
		{"前\n\n&#32; 古い形", "<body>前\n\n  古い形</body>"},
	} {
		if got := FromMarkdown(tc.md); got != tc.html {
			t.Errorf("FromMarkdown(%q) = %q, want %q", tc.md, got, tc.html)
		}
	}
	// what the editor and MCP show has no character references for indentation
	for _, in := range []string{"<body>  前\n\tタブ\n\n    四つ</body>", "<body><blockquote>引用\n  字下げ</blockquote></body>"} {
		md := ToEditableMarkdown(in)
		if strings.Contains(md, "&#") {
			t.Errorf("editable markdown of %q shows a character reference: %q", in, md)
		}
		if out := FromMarkdown(md); out != in {
			t.Errorf("round trip of %q via %q = %q", in, md, out)
		}
	}
	if md := FromPlainEditable("  前\n\tタブ"); md != "  前  \n\tタブ" {
		t.Errorf("FromPlainEditable = %q", md)
	}
	// the display form keeps the character references (the renderer would drop the spaces)
	if md := ToMarkdown("<body>  前</body>"); md != "&#32; 前" {
		t.Errorf("ToMarkdown = %q", md)
	}
}

// An email address written in the editor links to mailto: (#45). Without it Asana gets a
// relative link ("a@x.jp"). The text of a linkified "www." address stays as written.
func TestFromMarkdownAutolinks(t *testing.T) {
	for _, tc := range []struct{ md, want string }{
		{"mail a@x.jp now", `<body>mail <a href="mailto:a@x.jp">a@x.jp</a> now</body>`},
		{"<a@x.jp>", `<body><a href="mailto:a@x.jp">a@x.jp</a></body>`},
		{"<mailto:a@x.jp>", `<body><a href="mailto:a@x.jp">mailto:a@x.jp</a></body>`},
		{"see www.x.jp", `<body>see <a href="http://www.x.jp">www.x.jp</a></body>`},
		{"see https://x.jp/a", `<body>see <a href="https://x.jp/a">https://x.jp/a</a></body>`},
	} {
		if got := FromMarkdown(tc.md); got != tc.want {
			t.Errorf("FromMarkdown(%q)\ngot  %q\nwant %q", tc.md, got, tc.want)
		}
	}
	// A mailto link from Asana goes back as it came.
	in := `<body>連絡先 <a href="mailto:a@x.jp">a@x.jp</a></body>`
	if got := FromMarkdown(ToEditableMarkdown(in)); got != in {
		t.Errorf("round trip of %q\ngot %q", in, got)
	}
}

// Spaces and tabs at the end of a line are text in Asana: they survive editing instead of
// being dropped or read as a Markdown line break (#45).
func TestTrailingWhitespaceRoundTrip(t *testing.T) {
	for _, in := range []string{
		"<body>a  \nb</body>",
		"<body>a\t\nb</body>",
		"<body>a \t\n\nb</body>",
		"<body>end  \n\nnext</body>",
		"<body>一  \n二 \n三</body>",
		"<body><strong>太</strong>  \n次</body>",
		"<body><blockquote>a  \nb</blockquote></body>",
		"<body><ul><li>a  \nb</li></ul></body>",
		"<body>a  \n<strong>b</strong></body>",       // text after the break is in an element
		"<body>a \n<a data-asana-gid=\"9\"/></body>", // or a mention
	} {
		md := ToEditableMarkdown(in)
		if out := FromMarkdown(md); out != in {
			t.Errorf("html %q\nmarkdown %q\ngot %q", in, md, out)
		}
	}
	// plain text keeps them too
	text := "a  \nb\t\n\nc"
	if got := PlainFromMarkdown(ToEditableMarkdown("<body>" + text + "</body>")); got != text {
		t.Errorf("plain %q, want %q", got, text)
	}
	// a line of only spaces is not made text (it stays a blank line, as before)
	if got, want := ToEditableMarkdown("<body>a\n  \nb</body>"), "a  \n\nb"; got != want {
		t.Errorf("blank line: got %q, want %q", got, want)
	}
	// a <br> after trailing spaces
	if got, want := ToMarkdown("<body>a  <br>b</body>"), "a &#32;  \nb"; got != want {
		t.Errorf("br: got %q, want %q", got, want)
	}
	// structure at the start of a line (list markers, quote prefixes) is not text
	if got, want := ToMarkdown("<body><blockquote>a\n\nb</blockquote></body>"), "> a\n> \n> b"; got != want {
		t.Errorf("quote: got %q, want %q", got, want)
	}
}

// Converting a long description takes time proportional to its length (#23): checking
// whether text starts a line used to scan all the output written so far. 8000 lines took
// about 13 s before; now well under 100 ms, so the limit leaves room for -race and slow CI.
func TestLongDescriptionIsLinear(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test")
	}
	in := "<body>" + strings.Repeat("<strong>a</strong> # b\n", 8000) + "</body>"
	start := time.Now()
	md := ToEditableMarkdown(in)
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("converting %d bytes took %v", len(in), d)
	}
	if !strings.HasSuffix(md, "**a** # b") || strings.Count(md, "\n") != 7999 {
		t.Fatalf("unexpected output %q", md[len(md)-40:])
	}
}

// Text right after a list marker or a quote prefix starts the line, so block syntax in it is
// escaped; text after other inline content on the line is not.
func TestAtLineStart(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"<body><ul><li># x</li></ul></body>", `- \# x`},
		{"<body><ol><li>- x</li></ol></body>", `1. \- x`},
		{"<body><ul><li><strong>a</strong># x</li></ul></body>", `- **a**# x`},
		{"<body><blockquote># x\n# y</blockquote></body>", "> \\# x  \n> \\# y"},
		{"<body><strong>a</strong>\n# x</body>", "**a**\n\\# x"},
		{"<body>a > b\n> c</body>", "a > b  \n\\> c"},
	} {
		if got := ToMarkdown(tc.in); got != tc.want {
			t.Errorf("ToMarkdown(%q)\ngot  %q\nwant %q", tc.in, got, tc.want)
		}
	}
}

// roundTrip converts rich text to editable Markdown and back, twice, and checks that the
// first round gives want and the second changes nothing.
func roundTrip(t *testing.T, in, want string) {
	t.Helper()
	md := ToEditableMarkdown(in)
	h1 := FromMarkdown(md)
	md2 := ToEditableMarkdown(h1)
	h2 := FromMarkdown(md2)
	if h1 != want || h2 != h1 {
		t.Errorf("in   %q\nmd   %q\nh1   %q\nmd2  %q\nh2   %q\nwant %q", in, md, h1, md2, h2, want)
	}
}

// Whitespace at the edges of emphasis is written outside the marks: "**a **b" would not be
// emphasis in Markdown, and the marks would become text (#49).
func TestEmphasisWithSpaceAtItsEdges(t *testing.T) {
	for in, want := range map[string]string{
		"<body><strong>a </strong>b</body>": "<body><strong>a</strong> b</body>",
		"<body>a<strong> b</strong></body>": "<body>a <strong>b</strong></body>",
		"<body>x<em>a \n</em>b</body>":      "<body>x<em>a</em> \nb</body>",
		"<body>x<em>a<br></em>b</body>":     "<body>x<em>a</em>\nb</body>",
		"<body>x<s>\ta\t</s>y</body>":       "<body>x\t<s>a</s>\ty</body>",
		"<body>x<b><i> a </i></b>y</body>":  "<body>x <em><strong>a</strong></em> y</body>",
		"<body>x<b><u> a</u> </b>y</body>":  "<body>x <strong>a</strong> y</body>",
		"<body>x<b>a </b>y</body>":          "<body>x<strong>a</strong> y</body>",
		"<body><b> </b>a</body>":            "<body> a</body>",
	} {
		roundTrip(t, in, want)
	}
}

// Blocks inside a quote (lists, code, paragraphs) stay in the quote, and whitespace
// between inline content in a list item is text, not layout between list tags (#51).
func TestBlocksInQuoteAndSpacesInListItems(t *testing.T) {
	for in, want := range map[string]string{
		"<body><blockquote><ul><li># x</li></ul></blockquote></body>":                                "<body><blockquote><ul><li># x</li></ul></blockquote></body>",
		"<body><blockquote><ol><li>a</li><li>b<ul><li>c</li></ul></li></ol>d</blockquote>e</body>":   "<body><blockquote><ol><li>a</li><li>b<ul><li>c</li></ul></li></ol>d</blockquote>e</body>",
		"<body><blockquote><pre>p\n> q</pre></blockquote></body>":                                    "<body><blockquote><pre>p\n&gt; q</pre></blockquote></body>",
		"<body><blockquote>q<pre>p</pre>r</blockquote></body>":                                       "<body><blockquote>q\n<pre>p</pre>r</blockquote></body>",
		"<body><blockquote><p>a</p><p>b</p></blockquote><p>c</p></body>":                             "<body><blockquote>a\n\nb</blockquote>c</body>",
		"<body><blockquote>a<blockquote>b<ul><li>c</li></ul></blockquote>d</blockquote>e</body>":     "<body><blockquote>a\n<blockquote>b\n<ul><li>c</li></ul></blockquote>d</blockquote>e</body>",
		`<body><ul><li><a href="https://x.jp/">a</a> <a href="https://x.jp/">b</a></li></ul></body>`: `<body><ul><li><a href="https://x.jp/">a</a> <a href="https://x.jp/">b</a></li></ul></body>`,
		"<body><ul><li><strong>a </strong>b</li></ul></body>":                                        "<body><ul><li><strong>a</strong> b</li></ul></body>",
		"<body><ul><li><b>a</b><i> </i><b>c</b></li></ul></body>":                                    "<body><ul><li><strong>a</strong> <strong>c</strong></li></ul></body>",
		"<body><ul>\n<li>a</li>\n<li> b</li>\n</ul></body>":                                          "<body><ul><li>a</li><li> b</li></ul></body>",
	} {
		roundTrip(t, in, want)
	}
}

// Spaces and tabs at the end of a block's text (a list item, a quote, the text before a
// nested list or a code block) are kept, and stay as they are on the next round trip (#48).
// (A line break that ends a block goes, with the spaces before it: TestNoGhostSpaceAtBlockEnd.)
func TestTrailingSpaceAtBlockEnd(t *testing.T) {
	for in, want := range map[string]string{
		"<body><ul><li>a  </li><li>b</li></ul></body>":                "<body><ul><li>a  </li><li>b</li></ul></body>",
		"<body><ul><li>a\t</li></ul>c</body>":                         "<body><ul><li>a\t</li></ul>c</body>",
		"<body>a  <ul><li>b</li></ul></body>":                         "<body>a  \n<ul><li>b</li></ul></body>",
		"<body>a  \n<ul><li>b</li></ul></body>":                       "<body>a  \n<ul><li>b</li></ul></body>",
		"<body><blockquote>a  </blockquote>b</body>":                  "<body><blockquote>a  </blockquote>b</body>",
		"<body><blockquote>a <p>b</p></blockquote></body>":            "<body><blockquote>a \n\nb</blockquote></body>",
		"<body>a <pre>p</pre></body>":                                 "<body>a \n<pre>p</pre></body>",
		"<body>a <br><pre>p</pre></body>":                             "<body>a \n<pre>p</pre></body>",
		"<body><ul><li>a <br><ul><li>b</li></ul></li></ul></body>":    "<body><ul><li>a <ul><li>b</li></ul></li></ul></body>",
		"<body><ol><li>a\t\n<ul><li>b</li></ul></li></ol></body>":     "<body><ol><li>a\t<ul><li>b</li></ul></li></ol></body>",
		"<body><ul><li><b>a</b> <ul><li>b</li></ul></li></ul></body>": "<body><ul><li><strong>a</strong> <ul><li>b</li></ul></li></ul></body>",
		"<body><ul><li><b>a </b></li></ul></body>":                    "<body><ul><li><strong>a</strong> </li></ul></body>",
		"<body>a </body>": "<body>a </body>",
	} {
		roundTrip(t, in, want)
	}
}

// An empty list item stays an empty item: its marker alone after the parent's text would
// make the parent a setext heading ("-") or become text ("1.") (#52).
func TestEmptyListItems(t *testing.T) {
	for in, want := range map[string]string{
		"<body><ul><li>a<ul><li></li></ul></li></ul></body>":                          "<body><ul><li>a<ul><li></li></ul></li></ul></body>",
		"<body><ul><li>a<ul><li>\n</li></ul></li></ul></body>":                        "<body><ul><li>a<ul><li></li></ul></li></ul></body>",
		"<body><ul><li>a<ul><li> <br></li></ul></li></ul></body>":                     "<body><ul><li>a<ul><li></li></ul></li></ul></body>",
		"<body><ol><li>a<ol><li></li></ol></li></ol></body>":                          "<body><ol><li>a<ol><li></li></ol></li></ol></body>",
		"<body><ol><li>a<ol><li>b</li><li></li></ol></li></ol></body>":                "<body><ol><li>a<ol><li>b</li><li></li></ol></li></ol></body>",
		"<body><ul><li>a</li><li></li><li>b</li></ul></body>":                         "<body><ul><li>a</li><li></li><li>b</li></ul></body>",
		"<body><ul><li></li></ul></body>":                                             "<body><ul><li></li></ul></body>",
		"<body><ul><li>a<ul><li><ul><li>b</li></ul></li></ul></li></ul></body>":       "<body><ul><li>a<ul><li><ul><li>b</li></ul></li></ul></li></ul></body>",
		"<body><ul><li><b> </b><ul><li></li></ul></li></ul></body>":                   "<body><ul><li><ul><li></li></ul></li></ul></body>",
		"<body><blockquote><ul><li>a<ul><li></li></ul></li></ul></blockquote></body>": "<body><blockquote><ul><li>a<ul><li></li></ul></li></ul></blockquote></body>",
	} {
		roundTrip(t, in, want)
	}
	// an item that is only a mention or an image is not empty
	roundTrip(t, `<body><ul><li>a<ul><li><img data-asana-gid="7"/></li></ul></li></ul></body>`,
		`<body><ul><li>a<ul><li><img data-asana-gid="7"/></li></ul></li></ul></body>`)
	roundTrip(t, `<body><ul><li>a<ul><li><a data-asana-gid="9"/></li></ul></li></ul></body>`,
		`<body><ul><li>a<ul><li><a data-asana-gid="9"/></li></ul></li></ul></body>`)
}

// Line breaks inside a list item keep the rest of the item in it: a blank line in the text
// is written as a backslash line break (a blank line would end the item), and line breaks
// at the start of the item go (Markdown cannot keep them; after the marker they would make
// the rest leave the list). Spaces before inline content at the start of an item line stay.
// An empty quote stays a quote. (#51, #52)
func TestLineBreaksInListItems(t *testing.T) {
	for in, want := range map[string]string{
		"<body><ul><li>a\n\nb</li><li>c</li></ul></body>":                                        "<body><ul><li>a\n\nb</li><li>c</li></ul></body>",
		"<body><ul><li>a<br><br>b</li></ul></body>":                                              "<body><ul><li>a\n\nb</li></ul></body>",
		"<body><ul><li>a<br>\n\nb</li></ul></body>":                                              "<body><ul><li>a\n\n\nb</li></ul></body>",
		"<body><ul><li>a<b>x</b>\n\n<b>y</b></li></ul></body>":                                   "<body><ul><li>a<strong>x</strong>\n\n<strong>y</strong></li></ul></body>",
		"<body><blockquote><ol><li>a\n\nb</li></ol></blockquote></body>":                         "<body><blockquote><ol><li>a\n\nb</li></ol></blockquote></body>",
		"<body><ul><li>a<ul><li>b\n\n<a href=\"https://x.jp/\">c</a></li></ul></li></ul></body>": "<body><ul><li>a<ul><li>b\n\n<a href=\"https://x.jp/\">c</a></li></ul></li></ul></body>",
		"<body><ul><li>a\n\n</li><li>b</li></ul></body>":                                         "<body><ul><li>a</li><li>b</li></ul></body>",
		"<body><ul><li><br>a</li></ul></body>":                                                   "<body><ul><li>a</li></ul></body>",
		"<body><ul><li>\n\n  a</li></ul></body>":                                                 "<body><ul><li>  a</li></ul></body>",
		"<body><ul><li>\t<b><br>m</b></li></ul></body>":                                          "<body><ul><li><strong>m</strong></li></ul></body>",
		"<body><ul><li>    <a href=\"https://x.jp/\">a</a></li></ul></body>":                     "<body><ul><li>    <a href=\"https://x.jp/\">a</a></li></ul></body>",
		"<body><ul><li>a<br>  <a href=\"https://x.jp/\">b</a></li></ul></body>":                  "<body><ul><li>a\n  <a href=\"https://x.jp/\">b</a></li></ul></body>",
		"<body><blockquote></blockquote>a</body>":                                                "<body><blockquote></blockquote>a</body>",
		"<body><blockquote>\n<br></blockquote></body>":                                           "<body><blockquote></blockquote></body>",
		"<body><blockquote><blockquote></blockquote></blockquote></body>":                        "<body><blockquote><blockquote></blockquote></blockquote></body>",
	} {
		roundTrip(t, in, want)
	}
	// a transparent element with nothing but a comment at the start of an item does not hang
	if got := ToEditableMarkdown("<body><ul><li><b><!-- c --></b><br>a</li></ul></body>"); got != "- a" {
		t.Errorf("got %q", got)
	}
}
