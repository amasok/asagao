package richtext

import (
	"testing"

	"github.com/amasok/asagao/internal/domain/rich"
)

// Target is rich.Target: each target here is the domain's one.
func TestTargetIsTheDomainTarget(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target Target
		domain rich.Target
	}{
		{"TaskNotes", TaskNotes, rich.TaskNotes},
		{"Comment", Comment, rich.Comment},
		{"StatusUpdate", StatusUpdate, rich.StatusUpdate},
	} {
		if rich.Target(tc.target) != tc.domain {
			t.Errorf("%s = %d, want rich.%s (%d)", tc.name, tc.target, tc.name, tc.domain)
		}
	}
}

// hasLayoutTag finds exactly the tags rich says are accepted only in task notes, whatever
// their case in the html.
func TestHasLayoutTagTags(t *testing.T) {
	for _, tc := range []struct {
		html string
		want bool
	}{
		{"<body><h1>a</h1></body>", true},
		{"<body><h2>a</h2></body>", true},
		{"<body><h3>a</h3></body>", true},
		{"<body><h6>a</h6></body>", true},
		{"<body><H1>a</H1></body>", true},
		{"<body>a<hr/>b</body>", true},
		{"<body>a<HR>b</body>", true},
		{`<body><img src="x"></body>`, true},
		{"<body><table><tr><td>a</td></tr></table></body>", true},
		{"<body><ul><li>a</li></ul></body>", false},
		{"<body><ol><li>a</li></ol></body>", false},
		{"<body><blockquote>a</blockquote></body>", false},
		{"<body><pre>a</pre></body>", false},
		{"<body><strong>a</strong> <em>b</em> <u>c</u> <s>d</s> <code>e</code> <a href=\"x\">f</a></body>", false},
		{"<body>&lt;h1&gt;a&lt;/h1&gt;</body>", false},
		{"<body><h7>a</h7></body>", false},
		{"plain text", false},
		{"", false},
	} {
		if got := hasLayoutTag(tc.html); got != tc.want {
			t.Errorf("hasLayoutTag(%q) = %v, want %v", tc.html, got, tc.want)
		}
	}
}
