package commitref

import (
	"reflect"
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
		{"mention", "Add login #" + a, []Ref{{a, false}}},
		{"at the start", "#" + a + " add login", []Ref{{a, false}}},
		{"fixes", "Add login\n\nfixes #" + a, []Ref{{a, true}}},
		{"Fixes with a colon", "Fixes: #" + a, []Ref{{a, true}}},
		{"colon without space", "close:#" + a, []Ref{{a, true}}},
		{"every keyword", "fix #1000000001 fixed #1000000002 close #1000000003 closes #1000000004 closed #1000000005 resolve #1000000006 resolves #1000000007 RESOLVED #1000000008",
			[]Ref{{"1000000001", true}, {"1000000002", true}, {"1000000003", true}, {"1000000004", true}, {"1000000005", true}, {"1000000006", true}, {"1000000007", true}, {"1000000008", true}}},
		{"keyword applies to the first ref only", "fixes #" + a + ", #" + b, []Ref{{a, true}, {b, false}}},
		{"keyword inside a word", "prefixes #" + a, []Ref{{a, false}}},
		{"keyword glued to the hash", "fixes#" + a, nil}, // "#" after a letter
		{"mention then fixes", "#" + a + " ... fixes #" + a, []Ref{{a, true}}},
		{"fixes then mention", "fixes #" + a + " see #" + a, []Ref{{a, true}}},
		{"two tasks", "#" + a + " and #" + b, []Ref{{a, false}, {b, false}}},
		{"in parentheses", "Add login (fixes #" + a + ")", []Ref{{a, true}}},
		{"GitHub issue number", "Merge pull request #30 from x; fixes #8", nil},
		{"nine digits are a GitHub number", "#123456789", nil},
		{"ten digits", "#1234567890", []Ref{{"1234567890", false}}},
		{"asana prefix with a short gid", "fixes asana#42", []Ref{{"42", true}}},
		{"asana prefix in any case", "ASANA#42 Asana#43", []Ref{{"42", false}, {"43", false}}},
		{"after a letter", "abc#" + a, nil},
		{"after a letter with the prefix", "xasana#42", nil},
		{"html entity", "&#" + a + ";", nil},
		{"cross-repo GitHub ref", "owner/repo#" + a, nil},
		{"double hash", "##" + a, nil},
		{"digits run into letters", "#" + a + "abc", nil},
		{"digits run into underscore", "#" + a + "_x", nil},
		{"too long", "#123456789012345678901234567890123", nil},
		{"after Japanese", "修正#" + a, []Ref{{a, false}}},
		{"revert only mentions", "Revert \"Add login fixes #" + a + "\"\n\nThis reverts commit abc.", []Ref{{a, false}}},
		{"revert word elsewhere", "Fix revert bug\n\nfixes #" + a, []Ref{{a, true}}},
		{"keyword on the line before", "Small fixes\n\n#" + a, []Ref{{a, false}}},
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
}

func TestAmended(t *testing.T) {
	for _, tc := range []struct {
		name       string
		prev, refs []Ref
		want       []Ref
	}{
		{"nothing before", nil, []Ref{{a, false}}, []Ref{{a, false}}},
		{"same mention", []Ref{{a, false}}, []Ref{{a, false}}, nil},
		{"same fix", []Ref{{a, true}}, []Ref{{a, true}}, nil},
		{"fix added", []Ref{{a, false}}, []Ref{{a, true}}, []Ref{{a, true}}},
		{"fix dropped", []Ref{{a, true}}, []Ref{{a, false}}, nil},
		{"new task", []Ref{{a, true}}, []Ref{{a, true}, {b, false}}, []Ref{{b, false}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Amended(tc.prev, tc.refs); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Amended = %v, want %v", got, tc.want)
			}
		})
	}
}
