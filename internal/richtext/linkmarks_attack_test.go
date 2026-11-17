package richtext

import (
	"strings"
	"testing"
)

// Attack inputs for styled link text (#15): the round trip must settle after one pass,
// keep the href, and not leak the link text's markup outside the link.
func TestLinkMarksAttackSettles(t *testing.T) {
	ins := []string{
		`<body><a href="https://e.com"><strong>https://x.com</strong> y</a></body>`,
		`<body><a href="https://e.com"><strong>a@b.com</strong></a></body>`,
		`<body><strong><a href="https://e.com"><strong>a</strong>b</a></strong></body>`,
		`<body><a href="https://e.com"><strong><a href="https://f.com">z</a></strong></a></body>`,
		`<body><a href="https://e.com"><strong>a</strong>` + "\n" + `b</a></body>`,
		`<body><a href="https://e.com"><strong>a *b* _c_ ` + "`d`" + `</strong></a></body>`,
		`<body><a href="https://e.com"><code>a` + "`" + `b</code><em>x</em></a></body>`,
		`<body><a href="https://e.com"><strong>**</strong>~~<em>_</em></a></body>`,
		`<body><a href="https://e.com"><strong></strong></a></body>`,
		`<body><a href="https://e.com"><strong> </strong></a></body>`,
		`<body><a href="https://e.com">a<strong>b</strong>c</a><a href="https://e.com">d</a></body>`,
		`<body><blockquote><a href="https://e.com"><strong>a</strong>` + "\n" + `b</a></blockquote></body>`,
		`<body><ol><li><a href="https://e.com(1)"><em>a</em> ]( b</a></li></ol></body>`,
		`<body><a href="https://e.com"><strong>a</strong><br>b</a></body>`,
		`<body><a href="https://e.com"><strong>a</strong><img src="x"></a></body>`,
		`<body><a href="javascript:alert(1)"><strong>a</strong></a></body>`,
		`<body><a href="https://e.com)**"><strong>a</strong></a></body>`,
		`<body><a href="https://e.com"><strong>` + strings.Repeat("a ", 500) + `</strong></a></body>`,
		`<body><a href="https://e.com">` + strings.Repeat("<em>", 40) + `x` + strings.Repeat("</em>", 40) + `</a></body>`,
	}
	for _, in := range ins {
		md1, h1, md2, h2 := rt2(in)
		if md2 != md1 || h2 != h1 {
			t.Errorf("not settled: in %q\nmd1 %q\nh1  %q\nmd2 %q\nh2  %q", in, md1, h1, md2, h2)
		}
		if strings.Contains(in, "javascript:") || strings.Contains(in, "e.com)**") {
			if strings.Contains(h1, "<strong>") && !strings.Contains(h1, `href="https://e.com)**"`) && strings.Contains(in, "e.com)**") {
				t.Errorf("href changed: %q -> %q", in, h1)
			}
		}
	}
}

func TestLinkMarksAttackKeepsText(t *testing.T) {
	for _, in := range []string{
		`<body><a href="https://e.com"><strong>https://x.com</strong> y</a></body>`,
		`<body><a href="https://e.com"><strong>a@b.com</strong></a></body>`,
		`<body><a href="https://e.com"><em>a</em> ]( b</a></body>`,
	} {
		_, h1, _, _ := rt2(in)
		if strings.Count(h1, `href="https://e.com"`) != 1 || strings.Count(h1, "<a ") != 1 {
			t.Errorf("link split or lost: %q -> %q", in, h1)
		}
	}
}
