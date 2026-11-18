package commitref

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

const (
	a = "1204567890123456"
	b = "1204567890123457"
)

func TestParse(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  string
		want []Ref
	}{
		{"none", "Update README", nil},
		{"mention", "Add login #" + a, []Ref{{TaskGID: a, Fixes: false}}},
		{"at the start", "#" + a + " add login", []Ref{{TaskGID: a, Fixes: false}}},
		{"fixes", "Add login\n\nfixes #" + a, []Ref{{TaskGID: a, Fixes: true}}},
		{"Fixes with a colon", "Fixes: #" + a, []Ref{{TaskGID: a, Fixes: true}}},
		{"colon without space", "close:#" + a, []Ref{{TaskGID: a, Fixes: true}}},
		{"every keyword", "fix #1000000001 fixed #1000000002 close #1000000003 closes #1000000004 closed #1000000005 resolve #1000000006 resolves #1000000007 RESOLVED #1000000008",
			[]Ref{{TaskGID: "1000000001", Fixes: true}, {TaskGID: "1000000002", Fixes: true}, {TaskGID: "1000000003", Fixes: true}, {TaskGID: "1000000004", Fixes: true}, {TaskGID: "1000000005", Fixes: true}, {TaskGID: "1000000006", Fixes: true}, {TaskGID: "1000000007", Fixes: true}, {TaskGID: "1000000008", Fixes: true}}},
		{"keyword applies to the first ref only", "fixes #" + a + ", #" + b, []Ref{{TaskGID: a, Fixes: true}, {TaskGID: b, Fixes: false}}},
		{"keyword inside a word", "prefixes #" + a, []Ref{{TaskGID: a, Fixes: false}}},
		{"keyword glued to the hash", "fixes#" + a, nil}, // "#" after a letter
		{"mention then fixes", "#" + a + " ... fixes #" + a, []Ref{{TaskGID: a, Fixes: true}}},
		{"fixes then mention", "fixes #" + a + " see #" + a, []Ref{{TaskGID: a, Fixes: true}}},
		{"two tasks", "#" + a + " and #" + b, []Ref{{TaskGID: a, Fixes: false}, {TaskGID: b, Fixes: false}}},
		{"in parentheses", "Add login (fixes #" + a + ")", []Ref{{TaskGID: a, Fixes: true}}},
		{"GitHub issue number", "Merge pull request #30 from x; fixes #8", nil},
		{"nine digits are a GitHub number", "#123456789", nil},
		{"ten digits", "#1234567890", []Ref{{TaskGID: "1234567890", Fixes: false}}},
		{"asana prefix with a short gid", "fixes asana#42", []Ref{{TaskGID: "42", Fixes: true}}},
		{"asana prefix in any case", "ASANA#42 Asana#43", []Ref{{TaskGID: "42", Fixes: false}, {TaskGID: "43", Fixes: false}}},
		{"after a letter", "abc#" + a, nil},
		{"after a letter with the prefix", "xasana#42", nil},
		{"html entity", "&#" + a + ";", nil},
		{"cross-repo GitHub ref", "owner/repo#" + a, nil},
		{"double hash", "##" + a, nil},
		{"digits run into letters", "#" + a + "abc", nil},
		{"digits run into underscore", "#" + a + "_x", nil},
		{"too long", "#123456789012345678901234567890123", nil},
		{"after Japanese", "修正#" + a, []Ref{{TaskGID: a, Fixes: false}}},
		{"revert only mentions", "Revert \"Add login fixes #" + a + "\"\n\nThis reverts commit abc.", []Ref{{TaskGID: a, Fixes: false}}},
		{"revert word elsewhere", "Fix revert bug\n\nfixes #" + a, []Ref{{TaskGID: a, Fixes: true}}},
		{"keyword on the line before", "Small fixes\n\n#" + a, []Ref{{TaskGID: a, Fixes: false}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Parse(tc.msg); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Parse(%q) = %v, want %v", tc.msg, got, tc.want)
			}
		})
	}
}

func TestMarker(t *testing.T) {
	if got := Marker("abc"); got != "asagao-git: abc" {
		t.Errorf("Marker = %q", got)
	}
	m := Marker("abc")
	for _, tc := range []struct {
		text string
		want bool
	}{
		{m, true},
		{"x\n\n" + m + "\n", true},
		{"x\n  " + m + " \r\n\n", true},
		{"x\n" + m + "\nmore", false},  // quoted, not the last line
		{"x " + m, false},              // not the whole line
		{"x\n" + m + "def", false},     // another commit whose name starts the same
		{"x\nasagao-git: abcd", false}, // likewise
		{"", false},
	} {
		if got := Marked(tc.text, "", m); got != tc.want {
			t.Errorf("Marked(%q) = %v", tc.text, got)
		}
	}
	for _, tc := range []struct {
		html string
		want bool
	}{
		{"<body>コミット <code>abc</code> で言及されました:\n<pre>msg</pre>" + m + "</body>", true},
		{"<body><pre>msg\n" + m + "</pre>" + m + "</body>", true},
		{"<body>x\n<pre>second\n\n" + m + "\n</pre>asagao-git: other</body>", false}, // quoted only
		{"<body><pre data-language=\"go\">" + m + "</pre></body>", false},
		{"<body>(<code>" + m + "</code>)</body>", false},
		{"<body>" + m + "d</body>", false}, // runs on into a longer name
		{"<body>" + m + "d " + m + "</body>", true},
		{"<body>" + m + "<br/>more</body>", true},
	} {
		// the plain text says otherwise: the rich text decides
		if got := Marked("no marker", tc.html, m); got != tc.want {
			t.Errorf("Marked(html %q) = %v", tc.html, got)
		}
	}
	if Marked("", "", "") || Marked("x", "", "") || Marked("x", "<body>x</body>", "") {
		t.Error("an empty marker marks nothing")
	}
}

func TestAmended(t *testing.T) {
	for _, tc := range []struct {
		name       string
		prev, refs []Ref
		want       []Ref
	}{
		{"nothing before", nil, []Ref{{TaskGID: a, Fixes: false}}, []Ref{{TaskGID: a, Fixes: false}}},
		{"same mention", []Ref{{TaskGID: a, Fixes: false}}, []Ref{{TaskGID: a, Fixes: false}}, nil},
		{"same fix", []Ref{{TaskGID: a, Fixes: true}}, []Ref{{TaskGID: a, Fixes: true}}, nil},
		{"fix added", []Ref{{TaskGID: a, Fixes: false}}, []Ref{{TaskGID: a, Fixes: true}}, []Ref{{TaskGID: a, Fixes: true}}},
		{"fix dropped", []Ref{{TaskGID: a, Fixes: true}}, []Ref{{TaskGID: a, Fixes: false}}, nil},
		{"new task", []Ref{{TaskGID: a, Fixes: true}}, []Ref{{TaskGID: a, Fixes: true}, {TaskGID: b, Fixes: false}}, []Ref{{TaskGID: b, Fixes: false}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Amended(tc.prev, tc.refs); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Amended = %v, want %v", got, tc.want)
			}
		})
	}
}

// A task's custom ID is read only with the "asana" prefix (Issue #36).
func TestParseCustomID(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  string
		want []Ref
	}{
		{"custom ID", "Add login asana#ENG-123", []Ref{{CustomID: "ENG-123"}}},
		{"fixes", "fixes asana#ENG-123", []Ref{{CustomID: "ENG-123", Fixes: true}}},
		{"prefix in any case, ID as written", "ASANA#eng-7", []Ref{{CustomID: "eng-7"}}},
		{"letters and digits before the dash", "asana#A1B2-9", []Ref{{CustomID: "A1B2-9"}}},
		{"with a gid", "#" + a + " fixes asana#ENG-1", []Ref{{TaskGID: a}, {CustomID: "ENG-1", Fixes: true}}},
		{"once each", "asana#ENG-1 then fixes asana#ENG-1", []Ref{{CustomID: "ENG-1", Fixes: true}}},
		{"without the prefix: not a reference", "UTF-8 and SHA-256 and #ENG-123 and ENG-123", nil},
		{"must start with a letter", "asana#1A-2", nil},
		{"digits after the dash", "asana#ENG-", nil},
		{"no second dash", "asana#ENG-1-2", nil},
		{"a gid may be followed by a dash", "fixes #" + a + "-hotfix asana#12-x", []Ref{{TaskGID: a, Fixes: true}, {TaskGID: "12"}}},
		{"no letters after", "asana#ENG-12x", nil},
		{"after a word", "xasana#ENG-1", nil},
		{"too long", "asana#" + strings.Repeat("A", 70) + "-1", nil},
		{"revert only mentions", "Revert \"fixes asana#ENG-1\"", []Ref{{CustomID: "ENG-1"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Parse(tc.msg); !slices.Equal(got, tc.want) {
				t.Fatalf("Parse(%q) = %+v, want %+v", tc.msg, got, tc.want)
			}
		})
	}
}

// An amend that keeps a custom ID reference does not handle it again.
func TestAmendedCustomID(t *testing.T) {
	prev := Parse("asana#ENG-1")
	if got := Amended(prev, Parse("asana#ENG-1 and asana#ENG-2")); !slices.Equal(got, []Ref{{CustomID: "ENG-2"}}) {
		t.Fatalf("got %+v", got)
	}
	if got := Amended(prev, Parse("fixes asana#ENG-1")); !slices.Equal(got, []Ref{{CustomID: "ENG-1", Fixes: true}}) {
		t.Fatalf("fixes added: %+v", got)
	}
}
