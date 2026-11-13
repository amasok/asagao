package richtext

import (
	"strings"
	"testing"
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
		{"lists",
			"<body>買うもの\n<ul>\n<li>牛乳</li>\n<li>卵<ol><li>赤</li><li>白</li></ol></li>\n</ul>以上</body>",
			"買うもの\n\n- 牛乳\n- 卵\n   1. 赤\n   2. 白\n\n以上"},
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
	for _, want := range []string{"# 候補", "**予算**", "- ニトリ", "![画像](asana:1220507679870013)", "[@Hanako](asana:user:9)"} {
		if !strings.Contains(md, want) {
			t.Fatalf("editable markdown missing %q:\n%s", want, md)
		}
	}
	out := FromMarkdown(md)
	for _, want := range []string{"<h1>候補</h1>", "<strong>予算</strong>", "<li>ニトリ</li>", `<img data-asana-gid="1220507679870013"/>`, `<a data-asana-gid="9"/>`} {
		if !strings.Contains(out, want) {
			t.Fatalf("html missing %q:\n%s", want, out)
		}
	}
	if got := PlainFromMarkdown(md); !strings.Contains(got, "予算は10万円") || strings.Contains(got, "**") || strings.Contains(got, "asana:") {
		t.Fatalf("plain = %q", got)
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
