package richtext

import (
	"strings"
	"testing"
)

// What Fit returns for a comment or status update has no tag Asana rejects there, whatever
// shape the old html had, and fitting it again changes nothing (#71).
func TestFitResultIsAcceptedAndStable(t *testing.T) {
	old := []string{
		"<body><h1>見出し</h1>本文</body>",
		"<body><H2>大文字</H2></body>",
		"<body><h4>四</h4></body>",
		"<body><h5>五</h5></body>",
		"<body><h6>六</h6></body>",
		"<body>a<hr/>b</body>",
		"<body>a<hr>b</body>",
		"<body><img data-asana-gid=\"1\" src=\"https://x/y.png\"/></body>",
		"<body><a href=\"https://x\"><img src=\"https://x/y.png\"/></a></body>",
		"<body><ul><li><h3>入れ子</h3></li><li>b</li></ul></body>",
		"<body><ol><li>x<ul><li><h4>deep</h4></li></ul></li></ol></body>",
		"<body><blockquote><h2>引用</h2></blockquote></body>",
		"<body><table><tr><td>A</td><td>B</td></tr></table></body>",
		"<body><a data-asana-gid=\"123\"/><h1>mention</h1></body>",
		"<body><strong><h1>bold h1</h1></strong></body>",
		"<body><pre>code <h1>in pre?</h1></pre></body>",
		"<body><h1></h1></body>",
		"<h1>no body</h1>",
		"<body><h1>a &lt;b&gt; &amp; c</h1></body>",
		"<body><h1>\x1b[31mred\x1b[0m</h1></body>",
		"<body><h1>" + strings.Repeat("長", 5000) + "</h1></body>",
	}
	for _, target := range []Target{Comment, StatusUpdate} {
		for _, in := range old {
			fitted, plain, changed := target.Fit(in)
			if !changed {
				t.Errorf("%q: not changed", in)
				continue
			}
			if hasLayoutTag(fitted) {
				t.Errorf("%q: fitted still has a rejected tag: %q", in, fitted)
			}
			if strings.Contains(plain, "<h") || strings.Contains(plain, "<img") {
				t.Errorf("%q: plain has markup: %q", in, plain)
			}
			if again, _, changed2 := target.Fit(fitted); changed2 || again != fitted {
				t.Errorf("%q: second Fit changed %q -> %q", in, fitted, again)
			}
		}
	}
}

// Text the user wrote is not lost when the tag around it goes.
func TestFitKeepsText(t *testing.T) {
	for _, w := range []string{"見出し", "本文", "セル", "入れ子"} {
		in := "<body><h1>見出し</h1>本文<table><tr><td>セル</td></tr></table><ul><li><h3>入れ子</h3></li></ul></body>"
		_, plain, _ := Comment.Fit(in)
		if !strings.Contains(plain, w) {
			t.Errorf("%q lost: %q", w, plain)
		}
	}
}

// Html that is fine as it is is returned as it is: no rewrite, no change reported.
func TestFitLeavesAcceptedHTMLAlone(t *testing.T) {
	for _, in := range []string{
		"",
		"plain text",
		"a < b and c > d",
		"<body>a &lt;h1&gt; literal</body>",
		"<body><strong>x</strong> <em>y</em> <a href=\"https://x\">l</a></body>",
		"<body><ul><li>a</li></ul><pre>c</pre><code>d</code></body>",
		"<body>&lt;img src=x&gt;</body>",
	} {
		for _, target := range []Target{Comment, StatusUpdate, TaskNotes} {
			if got, _, changed := target.Fit(in); changed || got != in {
				t.Errorf("%v %q: changed to %q", target, in, got)
			}
		}
	}
	// task notes accept the layout tags: never rewritten
	if _, _, changed := TaskNotes.Fit("<body><h1>x</h1><hr/></body>"); changed {
		t.Error("TaskNotes.Fit rewrote task notes")
	}
}
