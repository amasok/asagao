package richtext

import (
	"math/rand"
	"strings"
	"testing"
)

func rt2(h string) (md1, h1, md2, h2 string) {
	md1 = ToEditableMarkdown(h)
	h1 = FromMarkdown(md1)
	md2 = ToEditableMarkdown(h1)
	h2 = FromMarkdown(md2)
	return
}

func TestBatch1HandPicked(t *testing.T) {
	cases := []string{
		"<body><h1>a<br>b</h1></body>",
		"<body><h1>a #</h1></body>",
		"<body><h1>a \\#</h1></body>",
		"<body><h2>#</h2></body>",
		"<body><h1><img data-asana-gid=\"7\" src=\"x\"></h1></body>",
		"<body><h1><strong>a</strong> <em>b</em> <code>c</code> <a href=\"https://x.jp/\">l</a></h1></body>",
		"<body><h1></h1><h2></h2><ul><li><h1></h1></li></ul></body>",
		"<body><pre>\n\n\nx\n\n\n\ny\n</pre></body>",
		"<body><PRE class=\"a\">\nx</PRE></body>",
		"<body><pre data-language=\"go\">\nx</pre></body>",
		"<body><pre data-language=\"a&quot;b\">x</pre></body>",
		"<body><pre data-language=\"```\">x</pre></body>",
		"<body><pre data-language=\"\">x</pre></body>",
		"<body><pre>a\r\nb\r\n\r\n\r\nc</pre></body>",
		"<body><blockquote><pre>a\n\n\nb</pre></blockquote></body>",
		"<body><pre>```\nx\n```</pre></body>",
		"<body><pre>````\n```\nx</pre></body>",
		"<body><pre>~~~\nx</pre></body>",
		"<body><ul><li><pre>a\n\n\nb</pre></li></ul></body>",
		"<body><code>a\n\n b</code> x <code>c\n d</code></body>",
		"<body><code>\n\n</code></body>",
		"<body><code>a\n  b</code></body>",
		"<body><code>a\n*b*</code></body>",
		"<body><code>a\n# b</code></body>",
		"<body><code>a\n- b</code></body>",
		"<body><code>a\n1. b</code></body>",
		"<body><code>a\n> b</code></body>",
		"<body><code>a\n\\*b</code> *z*</body>",
		"<body><code>a\n    b</code></body>",
		"<body><ul><li>a<ul><li>b</li></ul></li></ul><hr/></body>",
		"<body><blockquote><ol><li>a</li><li>b</li></ol></blockquote></body>",
	}
	for _, in := range cases {
		md1, h1, md2, h2 := rt2(in)
		if h1 != h2 {
			t.Errorf("unstable\nin  %q\nmd1 %q\nh1  %q\nmd2 %q\nh2  %q", in, md1, h1, md2, h2)
		}
		for _, bad := range []string{"\r", "<h1><h", "# <ul>"} {
			if strings.Contains(h1, bad) {
				t.Errorf("bad %q in %q -> %q", bad, in, h1)
			}
		}
	}
}

func TestBatch1CRLFSameAsLF(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	for i := 0; i < 5000; i++ {
		var sb strings.Builder
		for j := r.Intn(5) + 1; j > 0; j-- {
			sb.WriteString([]string{"a", "# h", "```go", "```", "> q", "- x", "  ", "1. y", "---", "    c", "[a]: http://x", "<div>z</div>", "**b**"}[r.Intn(13)])
			sb.WriteString("\n")
		}
		lf := sb.String()
		crlf := strings.ReplaceAll(lf, "\n", "\r\n")
		if FromMarkdown(lf) != FromMarkdown(crlf) {
			t.Fatalf("CRLF differs\n%q\n%q\n%q", lf, FromMarkdown(lf), FromMarkdown(crlf))
		}
		if strings.Contains(FromMarkdown(crlf), "\r") {
			t.Fatalf("CR left %q", crlf)
		}
	}
}

// main's output must not change for input without the touched constructs
func TestBatch1NoTargetFailsRaw(t *testing.T) {
	r := rand.New(rand.NewSource(99))
	for i := 0; i < 20000; i++ {
		var sb strings.Builder
		sb.WriteString("<body>")
		for j := r.Intn(3) + 1; j > 0; j-- {
			sb.WriteString(genBlockRT(r, 0))
		}
		sb.WriteString("</body>")
		in := sb.String()
		h := FromMarkdown(ToEditableMarkdown(in))
		for _, bad := range []string{"\r", "<h1><h", "<h2><h", "<pre><pre", "<li><li>"} {
			if strings.Contains(h, bad) {
				t.Fatalf("%q in %q -> %q", bad, in, h)
			}
		}
	}
}

// every word typed in must survive html -> md -> html (words never vanish).
func TestBatch1WordsSurvive(t *testing.T) {
	r := rand.New(rand.NewSource(2024))
	n := 0
	wrap := func(open, body, cls string) string { return open + body + cls }
	for i := 0; i < 30000; i++ {
		n = 0
		w := func() string { n++; return "zw" + string(rune('a'+n%26)) + strings.Repeat("q", n) }
		var words []string
		wd := func() string { s := w(); words = append(words, s); return s }
		var inl func(d int) string
		inl = func(d int) string {
			switch r.Intn(9) {
			case 0:
				return "<strong>" + wd() + "</strong>"
			case 1:
				return "<em>" + wd() + "</em>"
			case 2:
				return "<code>" + wd() + "\n" + []string{"", "\n", " "}[r.Intn(3)] + wd() + "</code>"
			case 3:
				return `<a href="https://x.jp/">` + wd() + `</a>`
			case 4:
				return wd() + "<br>" + wd()
			case 5:
				return `<a data-asana-gid="1" data-asana-type="user" data-asana-accessible="true" href="https://app.asana.com/0/1">@m</a>`
			}
			return wd()
		}
		var blk func(d int) string
		blk = func(d int) string {
			switch r.Intn(7) {
			case 0:
				if d < 2 {
					return wrap("<blockquote>", blk(d+1), "</blockquote>")
				}
			case 1:
				return "<ul><li>" + inl(0) + "</li><li>" + inl(0) + "</li></ul>"
			case 2:
				return "<pre" + []string{"", ` data-language="go"`}[r.Intn(2)] + ">" + []string{"", "\n"}[r.Intn(2)] + wd() + "\n\n\n" + wd() + "</pre>"
			case 3:
				return "<h1>" + inl(0) + inl(0) + "</h1>"
			case 4:
				return "<ol><li>" + inl(0) + "<ul><li>" + inl(0) + "</li></ul></li></ol>"
			}
			return inl(0)
		}
		in := "<body>" + blk(0) + "\n" + blk(0) + "</body>"
		md := ToEditableMarkdown(in)
		h1 := FromMarkdown(md)
		for _, x := range words {
			if !strings.Contains(h1, x) {
				t.Fatalf("lost %q\nin %q\nmd %q\nout %q", x, in, md, h1)
			}
		}
		h2 := FromMarkdown(ToEditableMarkdown(h1))
		if h1 != h2 {
			t.Fatalf("unstable\nin %q\nh1 %q\nh2 %q", in, h1, h2)
		}
		// plain text: words not glued
		p := TaskNotes.PlainFromMarkdown(md)
		for _, x := range words {
			if !strings.Contains(p, x) {
				t.Fatalf("plain lost %q: %q", x, p)
			}
		}
	}
}

var mdPieces = []string{"a", "# h", "## ", "#", "- x", "  - y", "1. z", "> q", "> 1. a", "> 1.", "```go", "```", "````", "~~~", "`x`", "`a\n\nb`", "**b**", "*i*", "---", "- a\n  ---", "[r]: http://x", "[r]", "<div>d</div>", "> <div>d</div>", "    code", "- #", "- # x", "- 1. a", "- > q", "![画像](asana:7)", "[@m](asana:user:1)", "# **b** [l](https://x.jp/)", "\r\n", "  ", "\t"}

var allowedAll = map[string]bool{"body": true, "strong": true, "em": true, "u": true, "s": true, "code": true, "a": true, "ul": true, "ol": true, "li": true, "blockquote": true, "pre": true, "br": true}

func tagsOf(h string) []string {
	var out []string
	for i := 0; i < len(h); i++ {
		if h[i] == '<' {
			j := i + 1
			if j < len(h) && h[j] == '/' {
				j++
			}
			k := j
			for k < len(h) && (h[k] >= 'a' && h[k] <= 'z' || h[k] >= '0' && h[k] <= '9') {
				k++
			}
			out = append(out, h[j:k])
		}
	}
	return out
}

func TestBatch1TargetsOnlyAllowedTags(t *testing.T) {
	r := rand.New(rand.NewSource(5))
	for i := 0; i < 30000; i++ {
		var sb strings.Builder
		for j := r.Intn(6) + 1; j > 0; j-- {
			sb.WriteString(mdPieces[r.Intn(len(mdPieces))])
			sb.WriteString([]string{"\n", "\n\n", " "}[r.Intn(3)])
		}
		md := sb.String()
		for _, tg := range []Target{Comment, StatusUpdate} {
			h := tg.FromMarkdown(md)
			for _, tag := range tagsOf(h) {
				if !allowedAll[tag] {
					t.Fatalf("target %d emits <%s>\nmd %q\nh %q", tg, tag, md, h)
				}
			}
			if strings.Contains(h, "\r") {
				t.Fatalf("CR in %q", h)
			}
			// stable once in html
			h2 := tg.FromMarkdown(ToEditableMarkdown(h))
			if h2 != tg.FromMarkdown(ToEditableMarkdown(h2)) {
				t.Fatalf("unstable target %d md %q\nh %q\nh2 %q", tg, md, h, h2)
			}
		}
		h := TaskNotes.FromMarkdown(md)
		if strings.Contains(h, "\r") || strings.Contains(h, "<li><li>") {
			t.Fatalf("bad %q -> %q", md, h)
		}
		h2 := FromMarkdown(ToEditableMarkdown(h))
		if h3 := FromMarkdown(ToEditableMarkdown(h2)); h2 != h3 {
			t.Fatalf("unstable md %q\nh %q\nh2 %q\nh3 %q", md, h, h2, h3)
		}
	}
}

// Markdown typed by the user: html1 = FromMarkdown(md) is what is sent; opening it again
// and saving without edits must send the same thing (the user's structure must not move).
func TestBatch1SendThenReopenSame(t *testing.T) {
	r := rand.New(rand.NewSource(11))
	bad := 0
	for i := 0; i < 30000 && bad < 6; i++ {
		var sb strings.Builder
		for j := r.Intn(5) + 1; j > 0; j-- {
			sb.WriteString(mdPieces[r.Intn(len(mdPieces))])
			sb.WriteString([]string{"\n", "\n\n"}[r.Intn(2)])
		}
		md := sb.String()
		h1 := FromMarkdown(md)
		if unstableOnMain(h1) || strings.Contains(h1, "[l](https://x.jp/)") { // literal link text: #77
			continue
		}
		h2 := FromMarkdown(ToEditableMarkdown(h1))
		if h1 != h2 {
			bad++
			t.Errorf("reopen changes\nmd %q\nh1 %q\nh2 %q", md, h1, h2)
		}
	}
}

func TestBatch1CodeBlockExact(t *testing.T) {
	for in, want := range map[string]string{
		"<PRE>\n\nx</PRE>":      "<body><pre>\n\nx</pre></body>",
		"<pre>\n\nx</pre>":      "<body><pre>\n\nx</pre></body>",
		"<pre>x\n\n</pre>":      "<body><pre>x</pre></body>",
		"<pre>x\n\n\n\ny</pre>": "<body><pre>x\n\n\n\ny</pre></body>",
	} {
		if got := FromMarkdown(ToEditableMarkdown("<body>" + in + "</body>")); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

// text kept as typed must be sent as typed: no link is made up out of it (#66, #77).
func TestBatch1DemotedTextAddsNoLink(t *testing.T) {
	for _, md := range []string{
		"- a\n  ---\n\n<div>[l](https://x.jp/)</div>",
		"> 1. a\n> 1. b\n\n<div>https://x.jp/</div>",
		"- a\n  - b\n  ---\n  [l](https://x.jp/)",
		"[r]: https://x.jp/\n\n<div>[l][r]</div>",
	} {
		h := FromMarkdown(md)
		if strings.Contains(h, `<a href="https://x.jp/">https://x.jp/</a>)`) || strings.Contains(h, `](<a `) {
			t.Errorf("link made up: %q -> %q", md, h)
		}
	}
}
