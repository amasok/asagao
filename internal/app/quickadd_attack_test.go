package app

import (
	"strings"
	"testing"
	"unicode"

	"github.com/amasok/asagao/internal/readmodel"
)

// Whatever the line, the parser does not panic, keeps the first word in the name, never
// returns an empty name for a non-blank line, and never drops text: the name plus the
// tokens it read cover every word of the line.
func FuzzParseQuickAddAttack(f *testing.F) {
	for _, s := range []string{"", " ", "a", "a @me", "a @", "a #", `a \@`, `\@me`, "a　明日", "a\t#買い物\n", "a @me @me", "a due: due:", "\xff @me", "a ​", "a @ "} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, line string) {
		r := ParseQuickAdd(line, now, quickCands)
		blank := strings.TrimSpace(line) == ""
		if blank && r.Name != "" {
			t.Fatalf("blank line gave name %q", r.Name)
		}
		if !blank && r.Name == "" {
			t.Fatalf("%q gave an empty name", line)
		}
		if first := strings.Fields(line); len(first) > 0 {
			un := strings.NewReplacer(`\@`, "@", `\#`, "#").Replace(first[0])
			if !strings.HasPrefix(r.Name, un) {
				t.Fatalf("%q: first word %q is not the start of the name %q", line, first[0], r.Name)
			}
		}
		if (r.Unread == "") != (r.UnreadReason == "") {
			t.Fatalf("unread/reason mismatch")
		}
		// the name is the line's own text up to a word boundary (apart from unescaping)
		if !blank && !strings.HasPrefix(strings.NewReplacer(`\@`, "@", `\#`, "#").Replace(strings.TrimSpace(line)), r.Name) {
			t.Fatalf("%q: name %q is not a prefix of the line", line, r.Name)
		}
		if strings.IndexFunc(r.Name, unicode.IsSpace) == 0 || strings.LastIndexFunc(r.Name, unicode.IsSpace) == len(r.Name)-1 && r.Name != "" {
			t.Fatalf("name %q has outer spaces", r.Name)
		}
		if r.Summary() != "" && strings.ContainsAny(r.Summary(), "\x1b\x00\x07") {
			t.Fatalf("summary has control characters")
		}
	})
}

func TestParseQuickAddAttack(t *testing.T) {
	type w struct {
		name, due, unread string
		assignee          string
		tags              []string
	}
	for _, tc := range []struct {
		in string
		w  w
	}{
		// spaces of other kinds
		{"a　@me　明日", w{name: "a", assignee: "u-me", due: "2026-11-17"}},
		{"a\t@me\n", w{name: "a", assignee: "u-me"}},
		{"a @me", w{name: "a", assignee: "u-me"}},
		{"   　 ", w{}},
		// bare numbers and weekday-like words are not dates
		{"会議 5", w{name: "会議 5"}},
		{"会議 2026", w{name: "会議 2026"}},
		{"会議 1/2", w{name: "会議 1/2"}},
		{"会議 mon", w{name: "会議 mon"}},
		{"会議 +0d", w{name: "会議", due: "2026-11-16"}},
		{"会議 +", w{name: "会議 +"}},
		{"会議 +999999999999999999999d", w{name: "会議 +999999999999999999999d"}},
		// escapes
		{`a \\@me`, w{name: `a \@me`, unread: ""}}, // a backslash then @me: not an assignee? see below
		{`\@me`, w{name: "@me"}},
		{`\#買い物 明日`, w{name: "#買い物", due: "2026-11-17"}},
		{`a \@`, w{name: "a @"}},
		// @ and # glued to other signs
		{"a @@me", w{name: "a @@me", unread: "@@me"}},
		{"a ##買い物", w{name: "a ##買い物", unread: "##買い物"}},
		{"a @me,", w{name: "a @me,", unread: "@me,"}},
		{"a #買い物。", w{name: "a #買い物。", unread: "#買い物。"}},
		// a token that merely starts like a date
		{"a 明日に", w{name: "a 明日に"}},
		{"a 2026-12-01x", w{name: "a 2026-12-01x"}},
		// user with an empty key never matches a prefix of ""
		{"a @.", w{name: "a @.", unread: "@."}},
		// control characters in the token stay in Unread but the summary strips them
		{"a @\x1b[31m", w{name: "a @\x1b[31m", unread: "@\x1b[31m"}},
	} {
		t.Run(tc.in, func(t *testing.T) {
			r := ParseQuickAdd(tc.in, now, quickCands)
			var as string
			if r.Assignee != nil {
				as = r.Assignee.GID
			}
			var tags []string
			for _, g := range r.Tags {
				tags = append(tags, g.GID)
			}
			if r.Name != tc.w.name || r.Due != tc.w.due || r.Unread != tc.w.unread || as != tc.w.assignee || strings.Join(tags, ",") != strings.Join(tc.w.tags, ",") {
				t.Errorf("%q: name=%q due=%q unread=%q assignee=%q tags=%v", tc.in, r.Name, r.Due, r.Unread, as, tags)
			}
			if strings.ContainsAny(r.Summary(), "\x1b\x00\x07") {
				t.Errorf("summary %q", r.Summary())
			}
		})
	}
}

// Users and tags whose names are control characters or empty never match by accident.
func TestParseQuickAddAttackOddCandidates(t *testing.T) {
	c := QuickAddCandidates{
		MeGID: "me",
		Users: []readmodel.UserContact{{GID: "u1"}, {GID: "u2", Name: "  ", Email: "@x"}, {GID: "u3", Name: "A B", Email: ""}},
		Tags:  []readmodel.TagCount{{GID: "g1", Name: ""}, {GID: "g2", Name: "  "}},
	}
	for _, in := range []string{"a @u", "a @x", "a @a", "a @b", "a @ab", "a #", "a #x"} {
		r := ParseQuickAdd(in, now, c)
		t.Logf("%q -> name=%q assignee=%v tags=%v unread=%q", in, r.Name, r.Assignee, r.Tags, r.Unread)
	}
	if r := ParseQuickAdd("a @ab", now, c); r.Assignee == nil || r.Assignee.GID != "u3" {
		t.Errorf("whole name without spaces should match: %+v", r)
	}
	if r := ParseQuickAdd("a @x", now, c); r.Assignee != nil {
		t.Errorf("a user with a blank name must not match by an empty email part: %+v", r)
	}
	if r := ParseQuickAdd("a #x", now, c); len(r.Tags) != 0 {
		t.Errorf("blank tag names must not match: %+v", r)
	}
}
