package richtext

import "testing"

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
