package commitref

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

const (
	g1 = "1204567890123456"
	g2 = "1204567890123457"
)

func gids(refs []Ref) []string {
	var out []string
	for _, r := range refs {
		s := r.TaskGID
		if r.Fixes {
			s += "!"
		}
		out = append(out, s)
	}
	return out
}

// What is and is not a reference: GitHub numbers, URL pieces, entities, neighbours.
func TestParseAttackBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, msg string
		want      []string
	}{
		{"github number", "fixes #123", nil},
		{"9 digits", "fixes #123456789", nil},
		{"10 digits", "fixes #1234567890", []string{"1234567890!"}},
		{"prefixed short", "asana#12", []string{"12"}},
		{"prefixed short fix", "closes asana#12", []string{"12!"}},
		{"prefix upper", "ASANA#12 Asana#13", []string{"12", "13"}},
		{"asana prefix glued to a word", "myasana#12", nil},
		{"asana with space before hash", "asana #12", nil},
		{"url fragment", "see https://example.com/page#" + g1, nil},
		{"url path", "https://github.com/o/r/pull/1#" + g1, nil},
		{"owner/repo#", "owner/repo#" + g1, nil},
		{"html entity", "&#" + g1 + ";", nil},
		{"letter before", "abc#" + g1, nil},
		{"digit before", "1#" + g1, nil},
		{"underscore before", "_#" + g1, nil},
		{"double hash", "##" + g1, nil},
		{"letter after", "#" + g1 + "a", nil},
		{"underscore after", "#" + g1 + "_", nil},
		{"punct after", "(#" + g1 + "), done", []string{g1}},
		{"dot after", "#" + g1 + ".", []string{g1}},
		{"hyphen before", "fix-#" + g1, []string{g1}},
		{"bracket", "[#" + g1 + "]", []string{g1}},
		{"cjk around", "対応#" + g1 + "を修正", []string{g1}},
		{"fullwidth hash", "＃" + g1, nil},
		{"fullwidth digits", "#１２０４５６７８９０１２３４５６", nil},
		{"31 digits ok, 33 not", "asana#" + strings.Repeat("1", 32), []string{strings.Repeat("1", 32)}},
		{"33 digits", "asana#" + strings.Repeat("1", 33), nil},
		{"hash only", "# " + g1, nil},
		{"empty", "", nil},
		{"only hash", "#", nil},
		{"at start", "#" + g1, []string{g1}},
		{"on line 2", "title\n\n#" + g1, []string{g1}},
		{"crlf", "title\r\n\r\nfixes #" + g1 + "\r\n", []string{g1 + "!"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := gids(Parse(tc.msg)); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Parse(%q) = %v, want %v", tc.msg, got, tc.want)
			}
		})
	}
}

// The keyword must be a word of its own, on the same line, right before the reference.
func TestParseAttackKeywords(t *testing.T) {
	for _, tc := range []struct {
		name, msg string
		want      []string
	}{
		{"fixes", "fixes #" + g1, []string{g1 + "!"}},
		{"upper", "FIXES #" + g1, []string{g1 + "!"}},
		{"colon", "Fixes: #" + g1, []string{g1 + "!"}},
		{"space colon", "Fixes : #" + g1, []string{g1 + "!"}},
		{"tab", "fix\t#" + g1, []string{g1 + "!"}},
		{"newline between", "fixes\n#" + g1, []string{g1}},
		{"two newlines", "fixes:\n\n#" + g1, []string{g1}},
		{"suffix of a word", "suffixes #" + g1, []string{g1}},
		{"prefix word", "prefix #" + g1, []string{g1}},
		{"unfixes", "unfixes #" + g1, []string{g1}},
		{"enclose", "enclose #" + g1, []string{g1}},
		{"disclose", "disclose #" + g1, []string{g1}},
		{"closing is not a keyword", "closing #" + g1, []string{g1}},
		{"fixing is not", "fixing #" + g1, []string{g1}},
		{"keyword with comma", "fixes, #" + g1, []string{g1}},
		{"fixes with asana prefix", "fixes asana#" + g1, []string{g1 + "!"}},
		{"only first after keyword", "fixes #" + g1 + ", #" + g2, []string{g1 + "!", g2}},
		{"keyword each", "fixes #" + g1 + ", fixes #" + g2, []string{g1 + "!", g2 + "!"}},
		{"and", "fixes #" + g1 + " and #" + g2, []string{g1 + "!", g2}},
		{"mention then fix same task", "#" + g1 + " ... fixes #" + g1, []string{g1 + "!"}},
		{"fix then mention same task", "fixes #" + g1 + " ... #" + g1, []string{g1 + "!"}},
		{"keyword before short github number then long", "fixes #12 #" + g1, []string{g1}},
		{"keyword in parentheses", "(fixes #" + g1 + ")", []string{g1 + "!"}},
		{"keyword after cjk", "これはfixes #" + g1, []string{g1 + "!"}}, // CJK is not an ASCII letter: the keyword stands alone
		{"keyword line before", "fixes\nsee #" + g1, []string{g1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := gids(Parse(tc.msg))
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Parse(%q) = %v, want %v", tc.msg, got, tc.want)
			}
		})
	}
}

// Revert: only mentions, whatever the keywords; other lines starting with "Revert" are
// ordinary messages.
func TestParseAttackRevert(t *testing.T) {
	for _, tc := range []struct {
		msg  string
		want []string
	}{
		{"Revert \"fixes #" + g1 + "\"\n\nThis reverts commit abc.", []string{g1}},
		{"Revert \"x\"\n\nfixes #" + g1, []string{g1}},
		{"Reverts the thing, fixes #" + g1, []string{g1 + "!"}},
		{"revert \"x\" fixes #" + g1, []string{g1 + "!"}},         // lowercase: not git's
		{"\nRevert \"x\" fixes #" + g1, []string{g1 + "!"}},       // not at the very start
		{"fixup! Revert \"x\"\nfixes #" + g1, []string{g1 + "!"}}, // fixup
	} {
		if got := gids(Parse(tc.msg)); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Parse(%q) = %v, want %v", tc.msg, got, tc.want)
		}
	}
}

// Each task once, in the order of first appearance; a large message does not blow up.
func TestParseAttackManyAndHuge(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 3000; i++ {
		fmt.Fprintf(&b, "#%d ", 1000000000+i%1500)
	}
	refs := Parse(b.String())
	if len(refs) != 1500 {
		t.Errorf("got %d refs, want 1500 distinct", len(refs))
	}
	if refs[0].TaskGID != "1000000000" || refs[1499].TaskGID != "1000001499" {
		t.Errorf("order lost: %v ... %v", refs[0], refs[1499])
	}
	// a megabyte of nearly-references finishes quickly and finds nothing
	junk := strings.Repeat("#12345 fixes asana# fixes #", 40000)
	if refs := Parse(junk); len(refs) != 0 {
		t.Errorf("junk gave %v", refs)
	}
	// invalid UTF-8 and NUL do not matter
	if got := gids(Parse("\xff\x00#" + g1 + "\xff")); !reflect.DeepEqual(got, []string{g1}) {
		t.Errorf("binary noise: %v", got)
	}
}

// Leading zeros are kept as written (the gid is a string).
func TestParseAttackLeadingZeros(t *testing.T) {
	if got := gids(Parse("#0000000000")); len(got) == 1 && got[0] != "0000000000" {
		t.Errorf("got %v", got)
	}
}

// Amended: the previous version's refs are not handled again; a new keyword on an old
// reference is; a lost keyword is not; refs order is kept.
func TestAmendedAttack(t *testing.T) {
	p := func(s string) []Ref { return Parse(s) }
	for _, tc := range []struct {
		name, prev, now string
		want            []string
	}{
		{"same", "#" + g1, "#" + g1, nil},
		{"fix added to old ref", "#" + g1, "fixes #" + g1, []string{g1 + "!"}},
		{"fix kept", "fixes #" + g1, "fixes #" + g1, nil},
		{"fix dropped", "fixes #" + g1, "#" + g1, nil},
		{"new ref", "#" + g1, "#" + g1 + " #" + g2, []string{g2}},
		{"all gone", "#" + g1, "no refs", nil},
		{"prev empty", "", "fixes #" + g1, []string{g1 + "!"}},
		{"swap", "#" + g1, "#" + g2, []string{g2}},
	} {
		got := gids(Amended(p(tc.prev), p(tc.now)))
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}
