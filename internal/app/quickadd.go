package app

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/amasok/asagao/internal/domain/task"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/termsafe"
)

// Quick add reads a task's assignee, due date and tags from the end of the line the user
// types for its name: "牛乳を買う @hanako 明日 #買い物".
//
// The words are read from the end, one at a time, and reading stops at the first word
// that is not one of these (that word and everything before it is the name):
//
//	@name      the assignee: @me, or a known user whose name or email matches uniquely
//	#name      a tag: an existing tag whose name matches uniquely (no new tags are made)
//	a date     as ParseDue reads it (明日, tomorrow, +3d, 2026-12-01, …), or due:<date>
//
// So a word in the middle of the name ("明日の準備をする", "#12 の件") is never read.
// A token that does not resolve (no match, several matches, a second assignee or due
// date) also stops the reading: it stays in the name, and the result says why it was
// not read, so nothing is dropped silently. The first word is always the name, so that
// a task can be called "明日". `\@` and `\#` put the sign itself in the name. The
// full-width ＠ and ＃ (as a Japanese IME types them) are read as @ and #.

// QuickAddStore is the part of the local cache that quick add reads. *store.Store
// satisfies it.
type QuickAddStore interface {
	MeGID(ctx context.Context) (string, error)
	ListUserContacts(ctx context.Context) ([]readmodel.UserContact, error)
	ListTags(ctx context.Context) ([]readmodel.TagCount, error)
}

// QuickAddCandidates are what the tokens are matched against.
type QuickAddCandidates struct {
	MeGID string // "" before the first sync: @me does not resolve
	Users []readmodel.UserContact
	Tags  []readmodel.TagCount
}

// QuickAddResult is what quick add read from a line.
type QuickAddResult struct {
	Name string // the rest of the line, with `\@` and `\#` unescaped
	Due  string // YYYY-MM-DD, or "" when no date was read

	// Assignee is the user read from "@name" (nil: none was read).
	Assignee *readmodel.UserRef
	// AssigneeIsMe is set when Assignee is the logged-in user.
	AssigneeIsMe bool
	// Tags are the existing tags read from "#name", in the order they were written.
	Tags []task.TagRef

	// Unread is the token that stopped the reading because it did not resolve, and
	// UnreadReason says why ("" both when reading stopped at an ordinary word).
	Unread, UnreadReason string

	// MidDue is set when the name has a due:<date> word that was not read because it is
	// not at the end (the TUI used to read it anywhere).
	MidDue bool
}

// minPrefix is the shortest "@name" matched as the start of a name ("@a" is not).
const minPrefix = 2

// halfWidthSign turns a word's leading full-width ＠ or ＃ (and \＠, \＃) into @ or #.
func halfWidthSign(w string) string {
	for _, p := range [][2]string{{"＠", "@"}, {"＃", "#"}, {`\＠`, `\@`}, {`\＃`, `\#`}} {
		if rest, ok := strings.CutPrefix(w, p[0]); ok {
			return p[1] + rest
		}
	}
	return w
}

// LoadQuickAddCandidates reads the users, tags and the logged-in user from the cache.
// A cache that has not been synced yet (no "me") is not an error: @me then does not resolve.
func LoadQuickAddCandidates(ctx context.Context, st QuickAddStore) (QuickAddCandidates, error) {
	var c QuickAddCandidates
	var err error
	c.MeGID, _ = st.MeGID(ctx)
	if c.Users, err = st.ListUserContacts(ctx); err != nil {
		return c, err
	}
	if c.Tags, err = st.ListTags(ctx); err != nil {
		return c, err
	}
	return c, nil
}

// ReadQuickAdd reads the line with the candidates in the cache (LoadQuickAddCandidates and
// ParseQuickAdd). Relative dates are relative to now.
func ReadQuickAdd(ctx context.Context, st QuickAddStore, line string, now time.Time) (QuickAddResult, error) {
	c, err := LoadQuickAddCandidates(ctx, st)
	if err != nil {
		return QuickAddResult{}, err
	}
	return ParseQuickAdd(line, now, c), nil
}

// ParseQuickAdd reads the assignee, due date and tags from the end of line (see the
// package comment above). It never fails: what it cannot read stays in Name.
func ParseQuickAdd(line string, now time.Time, c QuickAddCandidates) QuickAddResult {
	var r QuickAddResult
	words := fieldSpans(line)
	end := len(words) // words[end:] have been read as tokens
	var tags []task.TagRef
	seen := map[string]bool{}
scan:
	for end > 1 {
		w := halfWidthSign(line[words[end-1][0]:words[end-1][1]])
		switch {
		case len(w) > 1 && w[0] == '@':
			u, isMe, reason := matchUser(w[1:], c)
			switch {
			case reason == "" && r.Assignee != nil:
				reason = "担当は 1 人だけ指定できます"
			case reason == "":
				r.Assignee, r.AssigneeIsMe = &u, isMe
				end--
				continue
			}
			r.Unread, r.UnreadReason = w, reason
			break scan
		case len(w) > 1 && w[0] == '#':
			tag, reason := matchTag(w[1:], c.Tags)
			if reason != "" {
				r.Unread, r.UnreadReason = w, reason
				break scan
			}
			if !seen[tag.GID] {
				seen[tag.GID] = true
				tags = append(tags, tag)
			}
			end--
		default:
			v, explicit := strings.CutPrefix(w, "due:")
			due, err := ParseDue(v, now)
			if v == "" || err != nil {
				if explicit { // due:<not a date>
					r.Unread, r.UnreadReason = w, "日付として読めません"
				}
				break scan // an ordinary word: the end of the name
			}
			if r.Due != "" {
				r.Unread, r.UnreadReason = w, "期日は 1 つだけ指定できます"
				break scan
			}
			r.Due = due
			end--
		}
	}
	// read from the end: put the tags back in the order they were written
	for i, j := 0, len(tags)-1; i < j; i, j = i+1, j-1 {
		tags[i], tags[j] = tags[j], tags[i]
	}
	r.Tags = tags
	name := strings.TrimSpace(line)
	if end < len(words) {
		name = strings.TrimSpace(line[:words[end-1][1]])
	}
	for _, sp := range words[:end] {
		if v, ok := strings.CutPrefix(line[sp[0]:sp[1]], "due:"); ok {
			if _, err := ParseDue(v, now); v != "" && err == nil {
				r.MidDue = true
			}
		}
	}
	r.Name = strings.NewReplacer(`\@`, "@", `\#`, "#", `\＠`, "＠", `\＃`, "＃").Replace(name)
	return r
}

// fieldSpans returns the [start, end) byte offsets of the space-separated words of s, so
// that the name keeps the spaces the user typed between its words.
func fieldSpans(s string) [][2]int {
	var spans [][2]int
	start := -1
	for i, r := range s {
		switch {
		case unicode.IsSpace(r) && start >= 0:
			spans = append(spans, [2]int{start, i})
			start = -1
		case !unicode.IsSpace(r) && start < 0:
			start = i
		}
	}
	if start >= 0 {
		spans = append(spans, [2]int{start, len(s)})
	}
	return spans
}

// matchKey is how names are compared: case and spaces do not matter (a name with spaces
// cannot be typed as one word).
func matchKey(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, s)
}

// userKeys are the words "@name" may be: the whole name, each word of it, and the part
// of the email before "@".
func userKeys(u readmodel.UserContact) []string {
	var keys []string
	if k := matchKey(u.Name); k != "" {
		keys = append(keys, k)
	}
	if f := strings.Fields(u.Name); len(f) > 1 {
		for _, w := range f {
			keys = append(keys, matchKey(w))
		}
	}
	if local, _, _ := strings.Cut(u.Email, "@"); local != "" {
		keys = append(keys, matchKey(local))
	}
	return keys
}

// matchUser finds the user "@q" names: me, or the one user one of whose keys is q, or
// failing that (and when q is at least minPrefix long), the one user one of whose keys
// starts with q. reason is why there is none.
func matchUser(q string, c QuickAddCandidates) (u readmodel.UserRef, isMe bool, reason string) {
	if strings.EqualFold(q, "me") {
		if c.MeGID == "" {
			return u, false, "自分のユーザー情報がありません（先に sync してください）"
		}
		u = readmodel.UserRef{GID: c.MeGID}
		for _, cu := range c.Users {
			if cu.GID == c.MeGID {
				u.Name = displayName(cu)
			}
		}
		return u, true, ""
	}
	key := matchKey(q)
	for _, match := range []func(k string) bool{
		func(k string) bool { return k == key },
		func(k string) bool { return utf8.RuneCountInString(key) >= minPrefix && strings.HasPrefix(k, key) },
	} {
		var found []readmodel.UserContact
		for _, cu := range c.Users {
			for _, k := range userKeys(cu) {
				if match(k) {
					found = append(found, cu)
					break
				}
			}
		}
		switch len(found) {
		case 0:
			continue
		case 1:
			return readmodel.UserRef{GID: found[0].GID, Name: displayName(found[0])}, found[0].GID == c.MeGID, ""
		}
		names := make([]string, 0, 3)
		for i, f := range found {
			if i == 3 {
				names = append(names, "…")
				break
			}
			names = append(names, displayName(f))
		}
		return u, false, fmt.Sprintf("%d 人に一致します: %s", len(found), strings.Join(names, ", "))
	}
	return u, false, "一致するユーザーがいません"
}

func displayName(u readmodel.UserContact) string {
	if u.Name != "" {
		return u.Name
	}
	return u.Email
}

// matchTag finds the existing tag "#q" names: the one whose name is q, or failing that,
// the one whose name is q ignoring case and spaces. reason is why there is none.
func matchTag(q string, tags []readmodel.TagCount) (task.TagRef, string) {
	key := matchKey(q)
	for _, match := range []func(name string) bool{
		func(name string) bool { return name == q },
		func(name string) bool { return matchKey(name) == key },
	} {
		var found []readmodel.TagCount
		for _, t := range tags {
			if match(t.Name) {
				found = append(found, t)
			}
		}
		switch len(found) {
		case 0:
			continue
		case 1:
			return task.TagRef{GID: found[0].GID, Name: found[0].Name}, ""
		}
		return task.TagRef{}, fmt.Sprintf("%d 個のタグに一致します", len(found))
	}
	return task.TagRef{}, "そのタグはありません（新しいタグは作りません）"
}

// Read says what was read, for a status line or the CLI's output: "担当: 花子 / 期日:
// 2026-11-19 / タグ: #買い物", or "" when nothing was. Names from Asana pass termsafe.
func (r QuickAddResult) Read() string {
	var parts []string
	if r.Assignee != nil {
		name := termsafe.String(r.Assignee.Name)
		if r.AssigneeIsMe {
			name = "自分"
		} else if name == "" {
			name = r.Assignee.GID
		}
		parts = append(parts, "担当: "+name)
	}
	if r.Due != "" {
		parts = append(parts, "期日: "+r.Due)
	}
	if len(r.Tags) > 0 {
		names := make([]string, len(r.Tags))
		for i, t := range r.Tags {
			names[i] = "#" + termsafe.String(t.Name)
		}
		parts = append(parts, "タグ: "+strings.Join(names, " "))
	}
	return strings.Join(parts, " / ")
}

// Problem says which token was not read and why ("" when every token was read), as
// "「@ta」は読めませんでした（2 人に一致します: …）。名前に残しました", and that a due:
// in the middle of the name is not read.
func (r QuickAddResult) Problem() string {
	var parts []string
	if r.Unread != "" {
		parts = append(parts, fmt.Sprintf("「%s」は読めませんでした（%s）。名前に残しました",
			termsafe.String(r.Unread), termsafe.String(r.UnreadReason)))
	}
	if r.MidDue {
		parts = append(parts, "名前の途中の due: は読みません（末尾に書いてください）")
	}
	return strings.Join(parts, "。")
}

// Summary is Read and Problem together, in parentheses after a message ("" when both are
// empty): "（担当: 自分 / 期日: 2026-11-19。「#x」は読めませんでした（…）。名前に残しました）".
func (r QuickAddResult) Summary() string {
	var parts []string
	if s := r.Read(); s != "" {
		parts = append(parts, s)
	}
	if s := r.Problem(); s != "" {
		parts = append(parts, s)
	}
	if len(parts) == 0 {
		return ""
	}
	return "（" + strings.Join(parts, "。") + "）"
}
