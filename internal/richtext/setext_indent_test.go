package richtext

import "testing"

// The lines of an indented paragraph that a setext underline makes a heading lose their
// indentation, as Markdown drops it from the heading's text: the editor writes the heading
// without it, so the round trip is stable after the first send (#12). The indentation of
// other paragraphs stays text.
func TestIndentedSetextHeading(t *testing.T) {
	for md, want := range map[string]string{
		"   a\n  ---":               "<body><h2>a</h2></body>",
		"   a\n-":                   "<body><h2>a</h2></body>",
		" a\n\tb\n===":              "<body><h1>a b</h1></body>",
		"> \ta\n>  b\n> ===":        "<body><blockquote><h1>a b</h1></blockquote></body>",
		"   a@x.jp\n---":            `<body><h2><a href="mailto:a@x.jp">a@x.jp</a></h2></body>`,
		"\t[l](https://e.com)\n---": `<body><h2><a href="https://e.com">l</a></h2></body>`,
		// 4 spaces are not code here: the line is the heading's
		"    a\n---": "<body><h2>a</h2></body>",
		// a lazy line of the quote's paragraph, then the quote's underline
		"> a\n   b\n> ---": "<body><blockquote><h2>a b</h2></blockquote></body>",
		// an item that cannot start a list after a paragraph line goes on with it
		"  a\n2)\n  ---": "<body><h2>a 2)</h2></body>",
		"  a\n2. x\n---": "<body><h2>a 2. x</h2></body>",
		// lines that do not end a paragraph in CommonMark go on with it: no ATX heading
		// ("#tag", 7 "#"), no rule (fewer than 3), and nothing indented 4 columns or more
		"  a\n#tag\n---":       "<body><h2>a #tag</h2></body>",
		"  a\n####### b\n---":  "<body><h2>a ####### b</h2></body>",
		"  a\n**\n---":         "<body><h2>a **</h2></body>",
		"  a\n__\n===":         "<body><h1>a __</h1></body>",
		"  a\n    # b\n---":    "<body><h2>a # b</h2></body>",
		"  a\n    - b\n---":    "<body><h2>a - b</h2></body>",
		"  a\n    > b\n---":    "<body><h2>a &gt; b</h2></body>",
		"  a\n    ```\nb\n---": "<body><h2>a ``` b</h2></body>",
		"  a\n\t1. b\n---":     "<body><h2>a 1. b</h2></body>",
		// these do end it
		"  a\n# b\n---":        "<body>  a\n<h1>b</h1><hr/></body>",
		"  a\n```\nb\n```":     "<body>  a\n<pre>b</pre></body>",
		"  a\n***\nb":          "<body>  a\n<hr/>b</body>",
		"  a\n<div>\n---":      "<body>  a\n\n&lt;div&gt;\n---</body>", // an HTML block ends it too
		"  a\n<!-- c -->\n---": "<body>  a\n\n&lt;!-- c --&gt;\n<hr/></body>",
		"  a\n<span>\n---":     "<body><h2>a &lt;span&gt;</h2></body>", // (not one that can)
		"  a\n<!x\n---":        "<body><h2>a &lt;!x</h2></body>",       // (goldmark: "<!" and an upper-case letter)
		"  a\n<!X\n---":        "<body>  a\n\n&lt;!X\n---</body>",
		// no underline of this paragraph: the indentation is text
		"  a\nb":          "<body>  a\nb</body>",
		"  a\n1. x\n---":  "<body>  a\n<ol><li>x</li></ol><hr/></body>",
		"  a\n- - -":      "<body>  a\n<hr/></body>",
		"> a\n>   b\n---": "<body><blockquote>a\n  b</blockquote><hr/></body>",
		"  a\n\n---":      "<body>  a\n<hr/></body>",
	} {
		h1 := FromMarkdown(md)
		md2 := ToEditableMarkdown(h1)
		if h2 := FromMarkdown(md2); h1 != want || h2 != h1 {
			t.Errorf("md  %q\nh1  %q (want %q)\nmd2 %q\nh2  %q", md, h1, want, md2, h2)
		}
	}
}
