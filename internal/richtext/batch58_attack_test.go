package richtext

import (
	"math/rand"
	"strings"
	"testing"
	"time"
)

var markTexts58 = []string{"a", "b", "日本", "。", "、", "（", "）", "「", "」", "(", ")", "[", "]", "!", "?", ".", ",", ":", ";", "-", "_", "*", "**", "***", "~", "~~", "`", "@", "a@x.jp", "x.jp", "www.x.jp", "https://x.jp/a_b", "http://x.jp/a*b*", "mailto:a@x.jp", "\\", "\\*", "&", "<", "#", "-", ">", "😀", "é"}

func gen58(r *rand.Rand, depth int, heading bool) string {
	var sb strings.Builder
	for i := r.Intn(5) + 1; i > 0; i-- {
		switch k := r.Intn(14); {
		case k < 5 && depth < 4:
			tag := []string{"strong", "b", "em", "i", "s", "del", "strong", "em"}[r.Intn(8)]
			sb.WriteString("<" + tag + ">" + gen58(r, depth+1, heading) + "</" + tag + ">")
		case k == 5 && !heading:
			sb.WriteString("<br>")
		case k == 6:
			sb.WriteString(`<a href="https://x.jp/">` + escHTML("l"+markTexts58[r.Intn(len(markTexts58))]) + "</a>")
		case k == 7:
			sb.WriteString("<code>" + escHTML([]string{"a", "x y", "`", "a@x.jp", "*", "**"}[r.Intn(6)]) + "</code>")
		case k == 8:
			sb.WriteString(`<img data-asana-gid="7" src="x">`)
		case k == 9:
			sb.WriteString(`<a data-asana-gid="1" data-asana-type="user" data-asana-accessible="true" href="https://app.asana.com/0/1"> </a>`)
		default:
			sb.WriteString(escHTML(markTexts58[r.Intn(len(markTexts58))]))
		}
	}
	return sb.String()
}

func TestBatch58MarksSurvive(t *testing.T) {
	r := rand.New(rand.NewSource(580))
	fails := 0
	for i := 0; i < 6000 && fails < 6; i++ {
		var in string
		switch r.Intn(5) {
		case 0:
			in = "<body><h1>" + gen58(r, 0, true) + "</h1></body>"
		case 1:
			in = "<body><ul><li>" + gen58(r, 0, false) + "</li></ul></body>"
		case 2:
			in = "<body><blockquote>" + gen58(r, 0, false) + "</blockquote></body>"
		default:
			in = "<body>" + gen58(r, 0, false) + "</body>"
		}
		md1, h1, md2, h2 := rt2(in)
		// the picture and mention nodes have no text of their own: compare styled text only
		if !sameStyle(styledText(in), styledText(h1)) || h1 != h2 {
			fails++
			t.Errorf("in  %q\nmd1 %q\nh1  %q\nmd2 %q\nh2  %q\n%v\n%v", in, md1, h1, md2, h2, styledText(in), styledText(h1))
		}
	}
}

// the same link with or without "\ " around it (the delimiters write it only where needed).
func TestBatch58LinkifySameWithEscapedSpace(t *testing.T) {
	for _, in := range []string{
		"<body>a <strong>x</strong>a@x.jp</body>",
		"<body><strong>x</strong>www.x.jp/a</body>",
		"<body>(<em>x</em>https://x.jp/a_b_)</body>",
		"<body><strong>a</strong> a@www.x.jp</body>",
		"<body>\tx<u>a@x.jp</u></body>",
		"<body>a@x.jp<br>b</body>",
		"<body>https://x.jp/a \nb</body>",
		"<body><em>https://x.jp/a</em> b</body>",
	} {
		md1, h1, md2, h2 := rt2(in)
		if h1 != h2 {
			t.Errorf("unstable\nin  %q\nmd1 %q\nh1  %q\nmd2 %q\nh2  %q", in, md1, h1, md2, h2)
		}
		if !sameStyle(styledText(in), styledText(h1)) {
			t.Errorf("style lost %q -> %q", in, h1)
		}
	}
}

// "\ " typed by the user: dropped as text, nowhere else.
func TestBatch58UserEscapedSpaceElsewhere(t *testing.T) {
	cases := map[string]string{
		"`a\\ b`":                 "<code>a\\ b</code>",
		"[l](https://x.jp/a\\ b)": "",
		"a\\\nb":                  "",
		"a\\ \nb":                 "",
		"    a\\ b":               "",
		"```\na\\ b\n```":         "<pre>a\\ b</pre>",
	}
	for md, want := range cases {
		h := FromMarkdown(md)
		if want != "" && !strings.Contains(h, want) {
			t.Errorf("%q -> %q, want %q in it", md, h, want)
		}
		// no words lost, stable
		h2 := FromMarkdown(ToEditableMarkdown(h))
		if h != h2 {
			t.Errorf("unstable %q: %q %q", md, h, h2)
		}
		if strings.Contains(h, " ") {
			t.Errorf("nbsp from %q: %q", md, h)
		}
	}
}

func TestBatch58LinearManyDelimiters(t *testing.T) {
	for _, unit := range []string{"<strong>a</strong><em>b</em>", "<em><strong>a</strong></em>」", "<s>a</s>(<b>x</b>)www.x.jp "} {
		in := "<body>" + strings.Repeat(unit, 20000) + "</body>"
		done := make(chan struct{})
		go func() { FromMarkdown(ToEditableMarkdown(in)); close(done) }()
		select {
		case <-done:
		case <-timeAfter10():
			t.Fatalf("too slow for %q", unit)
		}
	}
}

func timeAfter10() <-chan time.Time { return time.After(15 * time.Second) }

// A no-break space between two line breaks is not a blank line. Where an element ends with
// <br> and a no-break space and a <br> follows it, the breaks must not become adjacent (a
// blank line) inside the emphasis: the emphasis would end there in Markdown.
func TestBatch58NbspBetweenBreaksKeepsEmphasis(t *testing.T) {
	for _, in := range []string{
		"<body><em><strong>x<br> </strong><br>a</em></body>",
		"<body><em>x<s>y<br> </s><br>a</em></body>",
		"<body><strong>x<em>y<br>　</em><br>a</strong></body>",
	} {
		md, h1, _, h2 := rt2(in)
		if !sameStyle(styledText(in), styledText(h1)) || h1 != h2 {
			t.Errorf("in %q\nmd %q\nh1 %q\nh2 %q\n%v\n%v", in, md, h1, h2, styledText(in), styledText(h1))
		}
	}
}

// "1." at the end of a text node followed by an address: the "." is escaped as a list marker
// ("1\.a@x.jp"), which keeps Linkify from reading "1.a@x.jp" as an address, and the next trip
// (written without the escape, "1.a@x.jp") links it: not stable.
func TestBatch58ListMarkerEscapeBeforeAddress(t *testing.T) {
	for _, in := range []string{
		"<body><em>x<br>1.<i>a@x.jp</i></em></body>",
		"<body>x<br>1.<u>a@x.jp</u></body>",
	} {
		md, h1, md2, h2 := rt2(in)
		if h1 != h2 {
			t.Errorf("in %q\nmd1 %q\nh1 %q\nmd2 %q\nh2 %q", in, md, h1, md2, h2)
		}
	}
}
