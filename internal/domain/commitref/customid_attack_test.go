package commitref

import (
	"slices"
	"strings"
	"testing"
)

// Boundaries of "asana#<custom ID>" (Issue #36).
func TestParseCustomIDAttack(t *testing.T) {
	for _, tc := range []struct {
		name, msg string
		want      []Ref
	}{
		{"max length ok", "asana#" + strings.Repeat("A", 60) + "-123", []Ref{{CustomID: strings.Repeat("A", 60) + "-123"}}},
		{"one over max", "asana#" + strings.Repeat("A", 61) + "-123", nil},
		{"huge digits", "asana#ENG-" + strings.Repeat("9", 5000), nil},
		{"trailing dot ok", "see asana#ENG-1.", []Ref{{CustomID: "ENG-1"}}},
		{"trailing comma ok", "asana#ENG-1, asana#ENG-2", []Ref{{CustomID: "ENG-1"}, {CustomID: "ENG-2"}}},
		{"in parentheses", "(asana#ENG-1)", []Ref{{CustomID: "ENG-1"}}},
		{"underscore before", "_asana#ENG-1", nil},
		{"slash before (url)", "https://x/asana#ENG-1", nil},
		{"double hash", "##ENG-1 asana##ENG-1", nil},
		{"unicode dash", "asana#ENG–1", nil},
		{"utf8 after digits", "asana#ENG-1é", []Ref{{CustomID: "ENG-1"}}},
		{"space after prefix", "asana# ENG-1", nil},
		{"closing keyword with colon", "Closes: asana#ENG-1", []Ref{{CustomID: "ENG-1", Fixes: true}}},
		{"keyword applies to one", "fixes asana#ENG-1, asana#ENG-2", []Ref{{CustomID: "ENG-1", Fixes: true}, {CustomID: "ENG-2"}}},
		{"revert does not fix", "Revert \"fixes asana#ENG-1\"\n\nThis reverts", []Ref{{CustomID: "ENG-1"}}},
		{"multi-line", "subject\n\nfixes asana#ENG-1\nasana#ENG-2", []Ref{{CustomID: "ENG-1", Fixes: true}, {CustomID: "ENG-2"}}},
		{"gid then dash is still a gid", "fix #1204567890123456-hotfix", []Ref{{TaskGID: "1204567890123456", Fixes: true}}},
		{"gid then dash and digits", "asana#12345-2", []Ref{{TaskGID: "12345"}}},
		{"custom ID then dash", "asana#ENG-1-hotfix", nil},
		{"empty", "", nil},
		{"only prefix", "asana#", nil},
		{"gid and custom share nothing", "asana#12345 asana#ENG-12345", []Ref{{TaskGID: "12345"}, {CustomID: "ENG-12345"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Parse(tc.msg); !slices.Equal(got, tc.want) {
				t.Fatalf("Parse(%q) = %+v, want %+v", tc.msg, got, tc.want)
			}
		})
	}
}
