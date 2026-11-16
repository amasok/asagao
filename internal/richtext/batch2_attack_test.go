package richtext

import (
	"math/rand"
	"strings"
	"testing"
)

func TestBatch2HandPicked(t *testing.T) {
	cases := []string{
		"<body><ul><li>a</li></ul><ul><li>b</li></ul><ul><li>c</li></ul></body>",
		"<body><ol><li>a</li></ol><ol><li>b</li></ol><ol><li>c</li></ol></body>",
		"<body><ul><li>a</li></ul><ol><li>b</li></ol><ul><li>c</li></ul></body>",
		"<body><ul><li></li></ul><ul><li></li></ul></body>",
		"<body><ul><li>a<ul><li>b</li></ul></li></ul><ul><li>c</li></ul></body>",
		"<body><ul><li>a<ul><li>b</li></ul><ul><li>c</li></ul></li></ul></body>",
		"<body><blockquote><ul><li>a</li></ul><ul><li>b</li></ul></blockquote></body>",
		"<body><ul><li>a</li></ul>\n<ul><li>b</li></ul></body>",
		"<body><ul><li>a</li></ul> <ul><li>b</li></ul></body>",
		"<body><ul><li>a<ul><li>b</li></ul>after</li></ul></body>",
		"<body><ul><li>a<ul><li>b<ul><li>c</li></ul>deep</li></ul>mid</li></ul></body>",
		"<body><ul><li>a<ul><li></li></ul>after</li></ul></body>",
		"<body><ul><li>a<ul><li>b</li></ul><br>after<br></li></ul></body>",
		"<body><ul><li>a<ul><li>b</li></ul>x<ul><li>c</li></ul>y</li></ul></body>",
		"<body><ul><li>a<ul><li>b</li></ul>\n</li></ul></body>",
		"<body><ul><li><blockquote>q<br>r\ns</blockquote></li></ul></body>",
		"<body><ul><li><blockquote><blockquote>q</blockquote></blockquote></li></ul></body>",
		"<body><ul><li><pre></pre></li></ul></body>",
		"<body><ul><li><h1></h1></li></ul></body>",
		"<body><ul><li><blockquote><ul><li>z</li></ul></blockquote></li></ul></body>",
		"<body><ul><li>a<hr/>b</li></ul></body>",
		"<body><ol><li>a<pre>c\n\nd</pre>b</li></ol></body>",
		`<body>a <a href="https://x.jp/">  b c  </a> d</body>`,
		`<body>a<a href="https://x.jp/"> b </a>d</body>`,
		`<body><a href="https://x.jp/">
b</a></body>`,
		`<body><strong>x <a href="https://x.jp/"> b </a> y</strong></body>`,
		`<body><a href="https://x.jp/"> </a>z</body>`,
	}
	for _, in := range cases {
		md1, h1, md2, h2 := rt2(in)
		if h1 != h2 && !onlyLinksAdded(h1, h2) {
			t.Errorf("unstable\nin  %q\nmd1 %q\nh1  %q\nmd2 %q\nh2  %q", in, md1, h1, md2, h2)
		}
	}
	// adjacent lists stay apart
	for _, in := range cases[:3] {
		h := FromMarkdown(ToEditableMarkdown(in))
		if strings.Count(h, "<ul>")+strings.Count(h, "<ol>") != 3 {
			t.Errorf("lists merged: %q -> %q", in, h)
		}
	}
}

func TestBatch2LinkEdgeSpacesOutside(t *testing.T) {
	h := FromMarkdown(ToEditableMarkdown(`<body>a  <a href="https://x.jp/"> b c </a>d</body>`))
	if strings.Contains(h, `> b`) || strings.Contains(h, `c </a>`) || strings.Contains(h, "ab") || strings.Contains(h, "cd") {
		t.Errorf("edge spaces: %q", h)
	}
}

func TestBatch2RandomLists(t *testing.T) {
	r := rand.New(rand.NewSource(31))
	fails := 0
	for i := 0; i < 40000 && fails < 6; i++ {
		n := 0
		var words []string
		wd := func() string { n++; s := "zw" + strings.Repeat("q", n); words = append(words, s); return s }
		inl := func() string {
			switch r.Intn(7) {
			case 0:
				return "<strong>" + wd() + "</strong>"
			case 1:
				return wd() + "<br>" + wd()
			case 2:
				return wd() + " "
			case 3:
				return `<a href="https://x.jp/">` + []string{" ", ""}[r.Intn(2)] + wd() + []string{" ", "\n", ""}[r.Intn(3)] + `</a>`
			case 4:
				return ""
			}
			return wd()
		}
		var list func(d int) string
		list = func(d int) string {
			tag := []string{"ul", "ol"}[r.Intn(2)]
			s := "<" + tag + ">"
			for k := r.Intn(3) + 1; k > 0; k-- {
				s += "<li>" + inl()
				for m := r.Intn(3); m > 0 && d < 3; m-- {
					switch r.Intn(5) {
					case 0, 1:
						s += list(d + 1)
					case 2:
						s += wd()
					case 3:
						s += "<blockquote>" + wd() + "</blockquote>"
					case 4:
						s += "<pre>" + wd() + "\n\n" + wd() + "</pre>"
					}
				}
				s += "</li>"
			}
			return s + "</" + tag + ">"
		}
		var sb strings.Builder
		sb.WriteString("<body>")
		for j := r.Intn(4) + 1; j > 0; j-- {
			switch r.Intn(5) {
			case 0:
				sb.WriteString("<blockquote>" + list(0) + "</blockquote>")
			case 1:
				sb.WriteString(wd() + "\n")
			default:
				sb.WriteString(list(0))
			}
			sb.WriteString([]string{"", "\n", " "}[r.Intn(3)])
		}
		sb.WriteString("</body>")
		in := sb.String()
		md1 := ToEditableMarkdown(in)
		h1 := FromMarkdown(md1)
		for _, x := range words {
			if !strings.Contains(h1, x) {
				fails++
				t.Errorf("lost %q\nin %q\nmd %q\nh1 %q", x, in, md1, h1)
				break
			}
		}
		md2 := ToEditableMarkdown(h1)
		h2 := FromMarkdown(md2)
		if h1 != h2 && !onlyLinksAdded(h1, h2) {
			fails++
			t.Errorf("unstable\nin  %q\nmd1 %q\nh1  %q\nmd2 %q\nh2  %q", in, md1, h1, md2, h2)
		}
		// Asana's spec: no pre / blockquote inside li in what we send
		if strings.Contains(h1, "<li><blockquote") || strings.Contains(h1, "<li><pre") {
			fails++
			t.Errorf("block in li sent: %q -> %q", in, h1)
		}
	}
}
