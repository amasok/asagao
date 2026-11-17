package app

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/readmodel"
)

var quickCands = QuickAddCandidates{
	MeGID: "u-me",
	Users: []readmodel.UserContact{
		{GID: "u-me", Name: "Me Myself", Email: "me@example.com"},
		{GID: "u-hanako", Name: "山田 花子", Email: "hanako@example.com"},
		{GID: "u-taro", Name: "Taro Tanaka", Email: "taro.t@example.com"},
		{GID: "u-tama", Name: "Tamaki", Email: "tamaki@example.com"},
		{GID: "u-noname", Email: "ghost@example.com"},
	},
	Tags: []readmodel.TagCount{
		{GID: "g-buy", Name: "買い物"},
		{GID: "g-urgent", Name: "Urgent"},
		{GID: "g-home1", Name: "家事"},
		{GID: "g-home2", Name: "家事"}, // two tags with one name
		{GID: "g-space", Name: "Next Week"},
	},
}

// now is 2026-11-16 (tasks_test.go).
func TestParseQuickAdd(t *testing.T) {
	type want struct {
		name, due, assignee string
		tags                []string
		unread              string
	}
	for _, tc := range []struct {
		in   string
		want want
	}{
		// nothing to read
		{"牛乳を買う", want{name: "牛乳を買う"}},
		{"  牛乳を  買う  ", want{name: "牛乳を  買う"}}, // inner spaces are kept
		// each token
		{"牛乳を買う @me", want{name: "牛乳を買う", assignee: "u-me"}},
		{"牛乳を買う @ME", want{name: "牛乳を買う", assignee: "u-me"}},
		{"牛乳を買う 明日", want{name: "牛乳を買う", due: "2026-11-17"}},
		{"牛乳を買う 今日", want{name: "牛乳を買う", due: "2026-11-16"}},
		{"牛乳を買う 明後日", want{name: "牛乳を買う", due: "2026-11-18"}},
		{"牛乳を買う 昨日", want{name: "牛乳を買う", due: "2026-11-15"}},
		{"牛乳を買う tomorrow", want{name: "牛乳を買う", due: "2026-11-17"}},
		{"牛乳を買う +7d", want{name: "牛乳を買う", due: "2026-11-23"}},
		{"牛乳を買う -3d", want{name: "牛乳を買う", due: "2026-11-13"}},
		{"牛乳を買う 2026-12-01", want{name: "牛乳を買う", due: "2026-12-01"}},
		{"牛乳を買う due:+3d", want{name: "牛乳を買う", due: "2026-11-19"}},
		{"牛乳を買う #買い物", want{name: "牛乳を買う", tags: []string{"g-buy"}}},
		// all of them, in any order; tags keep the order they were written in
		{"牛乳を買う @hanako 明日 #買い物 #urgent", want{name: "牛乳を買う", due: "2026-11-17", assignee: "u-hanako", tags: []string{"g-buy", "g-urgent"}}},
		{"牛乳を買う #urgent 明日 #買い物 @hanako", want{name: "牛乳を買う", due: "2026-11-17", assignee: "u-hanako", tags: []string{"g-urgent", "g-buy"}}},
		{"牛乳を買う #買い物 #買い物", want{name: "牛乳を買う", tags: []string{"g-buy"}}},
		// users: the whole name without spaces, a word of it, the email before "@", a unique prefix
		{"a @山田花子", want{name: "a", assignee: "u-hanako"}},
		{"a @花子", want{name: "a", assignee: "u-hanako"}},
		{"a @tanaka", want{name: "a", assignee: "u-taro"}},
		{"a @taro.t", want{name: "a", assignee: "u-taro"}},
		{"a @ghost", want{name: "a", assignee: "u-noname"}},
		{"a @tam", want{name: "a", assignee: "u-tama"}},  // prefix of tamaki only (taro does not start with tam)
		{"a @taro", want{name: "a", assignee: "u-taro"}}, // exact beats the prefix of nothing else
		{"a @myself", want{name: "a", assignee: "u-me"}},
		// tags: case and spaces do not matter, when that is unique
		{"a #URGENT", want{name: "a", tags: []string{"g-urgent"}}},
		{"a #nextweek", want{name: "a", tags: []string{"g-space"}}},
		// words in the middle of the name are not read
		{"明日の準備をする", want{name: "明日の準備をする"}},
		{"#12 の件", want{name: "#12 の件"}},
		{"明日 @hanako と会う", want{name: "明日 @hanako と会う"}},
		{"牛乳 明日 買う 今日", want{name: "牛乳 明日 買う", due: "2026-11-16"}},
		// the first word is always the name
		{"明日", want{name: "明日"}},
		{"#買い物", want{name: "#買い物"}},
		{"@me 明日", want{name: "@me", due: "2026-11-17"}},
		// escapes
		{`メール \@hanako`, want{name: "メール @hanako"}},
		{`Issue \#12 明日`, want{name: "Issue #12", due: "2026-11-17"}},
		{`a\@b \#c`, want{name: "a@b #c"}},
		// a token that does not resolve stops the reading and stays in the name
		{"a @nobody 明日", want{name: "a @nobody", due: "2026-11-17", unread: "@nobody"}},
		{"a @ta 明日", want{name: "a @ta", due: "2026-11-17", unread: "@ta"}}, // Taro Tanaka, Tamaki
		{"a #家事 #買い物", want{name: "a #家事", tags: []string{"g-buy"}, unread: "#家事"}},
		{"a #新しい", want{name: "a #新しい", unread: "#新しい"}},
		{"a 明日 @me @hanako", want{name: "a 明日 @me", assignee: "u-hanako", unread: "@me"}},
		{"a 今日 明日", want{name: "a 今日", due: "2026-11-17", unread: "今日"}},
		{"a due:来週", want{name: "a due:来週", unread: "due:来週"}},
		{"a due:", want{name: "a due:", unread: "due:"}},
		// "@" and "#" alone, and dates that are not dates, are ordinary words
		{"a @", want{name: "a @"}},
		{"a #", want{name: "a #"}},
		{"a 2026-13-01", want{name: "a 2026-13-01"}},
		{"a +3days", want{name: "a +3days"}},
		{"", want{}},
	} {
		t.Run(tc.in, func(t *testing.T) {
			r := ParseQuickAdd(tc.in, now, quickCands)
			var assignee string
			if r.Assignee != nil {
				assignee = r.Assignee.GID
			}
			var tags []string
			for _, g := range r.Tags {
				tags = append(tags, g.GID)
			}
			got := want{r.Name, r.Due, assignee, tags, r.Unread}
			if got.name != tc.want.name || got.due != tc.want.due || got.assignee != tc.want.assignee ||
				!slices.Equal(got.tags, tc.want.tags) || got.unread != tc.want.unread {
				t.Errorf("ParseQuickAdd(%q)\n got  %+v\n want %+v", tc.in, got, tc.want)
			}
			if (r.Unread == "") != (r.UnreadReason == "") {
				t.Errorf("Unread %q with reason %q", r.Unread, r.UnreadReason)
			}
		})
	}
}

func TestParseQuickAddNoMe(t *testing.T) {
	c := quickCands
	c.MeGID = ""
	r := ParseQuickAdd("a @me", now, c)
	if r.Assignee != nil || r.Name != "a @me" || r.Unread != "@me" || !strings.Contains(r.UnreadReason, "sync") {
		t.Errorf("%+v", r)
	}
}

// What was read and what was not are both said; Asana's names pass termsafe.
func TestQuickAddSummary(t *testing.T) {
	c := quickCands
	c.Users = append(slices.Clone(c.Users), readmodel.UserContact{GID: "u-evil", Name: "evil\x1b]0;x\x07", Email: "evil@example.com"})
	c.Tags = append(slices.Clone(c.Tags), readmodel.TagCount{GID: "g-evil", Name: "bad\x1b[31m"})
	for _, tc := range []struct{ in, read, problem string }{
		{"a", "", ""},
		{"a @me 明日 #買い物 #urgent", "担当: 自分 / 期日: 2026-11-17 / タグ: #買い物 #Urgent", ""},
		{"a @hanako", "担当: 山田 花子", ""},
		{"a @evil #bad\x1b[31m", "担当: evil]0;x / タグ: #bad[31m", ""},
		{"a @ta 明日", "期日: 2026-11-17", "「@ta」は読めませんでした（2 人に一致します: Taro Tanaka, Tamaki）。名前に残しました"},
		{"a #x\x1b[2J", "", "「#x[2J」は読めませんでした（そのタグはありません（新しいタグは作りません））。名前に残しました"},
	} {
		r := ParseQuickAdd(tc.in, now, c)
		problem := strings.ReplaceAll(r.Problem(), "…", "...")
		if r.Read() != tc.read || problem != tc.problem {
			t.Errorf("%q:\n read    %q, want %q\n problem %q, want %q", tc.in, r.Read(), tc.read, problem, tc.problem)
		}
		if s := r.Summary(); strings.ContainsAny(s, "\x1b\x07") {
			t.Errorf("%q: summary has control characters: %q", tc.in, s)
		}
	}
	if s := ParseQuickAdd("a", now, c).Summary(); s != "" {
		t.Errorf("nothing read: summary %q", s)
	}
	if s := ParseQuickAdd("a @nobody 明日", now, c).Summary(); !strings.HasPrefix(s, "（期日: 2026-11-17。「@nobody」") {
		t.Errorf("summary %q", s)
	}
}

type quickStore struct {
	me              string
	meErr, usersErr error
}

func (s quickStore) MeGID(context.Context) (string, error) { return s.me, s.meErr }
func (s quickStore) ListUserContacts(context.Context) ([]readmodel.UserContact, error) {
	return quickCands.Users, s.usersErr
}
func (s quickStore) ListTags(context.Context) ([]readmodel.TagCount, error) {
	return quickCands.Tags, nil
}

// Before the first sync there is no "me": that is not an error, @me just does not resolve.
// Other read errors are.
func TestReadQuickAdd(t *testing.T) {
	ctx := context.Background()
	r, err := ReadQuickAdd(ctx, quickStore{me: "u-me"}, "a @me #買い物", now)
	if err != nil || r.Assignee == nil || r.Assignee.GID != "u-me" || !r.AssigneeIsMe || len(r.Tags) != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	r, err = ReadQuickAdd(ctx, quickStore{meErr: errors.New("no me")}, "a @me", now)
	if err != nil || r.Assignee != nil || r.Unread != "@me" {
		t.Fatalf("no me: %+v %v", r, err)
	}
	boom := errors.New("boom")
	if _, err := ReadQuickAdd(ctx, quickStore{me: "u-me", usersErr: boom}, "a", now); !errors.Is(err, boom) {
		t.Fatalf("err %v", err)
	}
}
