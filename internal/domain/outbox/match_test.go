package outbox

import (
	"strings"
	"testing"
)

func TestSameTaskFields(t *testing.T) {
	task := TaskFields{DueOn: "2026-11-20", AssigneeGID: "1200", Notes: "牛乳を買う\n- 2 本", HTMLNotes: `<body>牛乳を<strong>買う</strong><ul><li>2 本</li></ul></body>`}
	for _, tc := range []struct {
		name    string
		task    TaskFields
		payload map[string]any
		want    bool
	}{
		{"all the same", task, map[string]any{"due_on": "2026-11-20", "assignee": "1200", "notes": "牛乳を買う\n- 2 本"}, true},
		{"other due date", task, map[string]any{"due_on": "2026-11-21", "assignee": "1200", "notes": "牛乳を買う\n- 2 本"}, false},
		{"no due date in the payload", task, map[string]any{"assignee": "1200", "notes": "牛乳を買う\n- 2 本"}, false},
		{"other assignee", task, map[string]any{"due_on": "2026-11-20", "assignee": "1300", "notes": "牛乳を買う\n- 2 本"}, false},
		{"no assignee on either", TaskFields{}, map[string]any{}, true},
		{"assignee only in Asana", TaskFields{AssigneeGID: "1200"}, map[string]any{}, false},
		{"notes differ in spacing and marks only", task, map[string]any{"due_on": "2026-11-20", "assignee": "1200", "notes": "牛乳を買う 2本!"}, true},
		{"notes differ in words", task, map[string]any{"due_on": "2026-11-20", "assignee": "1200", "notes": "パンを買う"}, false},
		{"notes differ in a URL only", TaskFields{Notes: "見る https://a.example/x"}, map[string]any{"notes": "見る https://b.example/y"}, true},
		{"rich notes match by html", task, map[string]any{"due_on": "2026-11-20", "assignee": "1200", "html_notes": "<body><p>牛乳を買う</p><ol><li>2 本</li></ol></body>", "notes": "違う"}, true},
		{"rich notes match by plain notes", TaskFields{Notes: "メモ", HTMLNotes: "<body>別物</body>"}, map[string]any{"html_notes": "<body>他</body>", "notes": "メモ"}, true},
		{"rich notes match by neither", TaskFields{Notes: "メモ", HTMLNotes: "<body>別物</body>"}, map[string]any{"html_notes": "<body>他</body>", "notes": "違う"}, false},
		{"plain payload ignores Asana's html", TaskFields{Notes: "メモ", HTMLNotes: "<body>別物</body>"}, map[string]any{"notes": "メモ"}, true},
		{"a null value is empty", TaskFields{}, map[string]any{"due_on": nil, "assignee": nil, "notes": nil}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := PayloadOf[CreateTask](Op{Kind: KindCreateTask, Payload: tc.payload})
			if err != nil {
				t.Fatal(err)
			}
			if got := SameTaskFields(tc.task, p); got != tc.want {
				t.Fatalf("SameTaskFields = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSameComment(t *testing.T) {
	// clean stands for termsafe.String: it drops escape characters.
	clean := func(s string) string { return strings.ReplaceAll(s, "\x1b", "") }
	for _, tc := range []struct {
		name                              string
		storyText, storyHTML, text, htmlT string
		want                              bool
	}{
		{"same text", "了解です", "", "了解です", "", true},
		{"spacing and marks", "了解 です！", "", "了解です", "", true},
		{"other words", "了解です", "", "だめです", "", false},
		{"Asana's plain text of a link", "詳細 https://example.com/a", "", "[詳細](https://example.com/a)", "", true},
		{"by the html", "@山田 さん 見て", `<body><a data-asana-gid="1" data-asana-type="user">@山田</a> さん<strong>見て</strong></body>`, "@yamada 見て", `<body><a data-asana-gid="1" data-asana-type="user"></a> さん<em>見て</em></body>`, true},
		{"html only on our side", "@山田 さん 見て", "", "@yamada 見て", "<body>見て</body>", false},
		{"html only on Asana's side", "@山田 さん 見て", "<body>見て</body>", "@yamada 見て", "", false},
		{"html differs too", "a", "<body>b</body>", "c", "<body>d</body>", false},
		{"emoji only, same", " 👍 ", "", "👍", "", true},
		{"emoji only, other", "👎", "", "👍", "", false},
		{"emoji only, after clean", "👍\x1b", "", "👍", "", true},
		{"emoji only is not compared loosely", "👍👍", "", "👍", "", false},
		{"emoji only, Asana has words", "OK 👍", "", "👍", "", false},
		{"empty", "", "", "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := SameComment(tc.storyText, tc.storyHTML, tc.text, tc.htmlT, clean); got != tc.want {
				t.Fatalf("SameComment = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTextKeyAndStripHTML(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Hello, world!", "Helloworld"},
		{"- 牛乳\n- パン 2個", "牛乳パン2個"},
		{"see https://example.com/a?b=c done", "seedone"},
		{"http://x.example", ""},
		{"👍 !?", ""},
		{"Ⅻ ½ ٣", "Ⅻ½٣"}, // numbers of other kinds count
	} {
		if got := TextKey(tc.in); got != tc.want {
			t.Errorf("TextKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	for _, tc := range []struct{ in, want string }{
		{"<body>a<strong>b</strong></body>", " a b  "},
		{"<body>&lt;x&gt; &amp;</body>", " <x> & "},
		{`<body><a data-asana-gid="1">@山田</a>さん</body>`, "  さん "},
		{`<body><img data-asana-gid="2" src="x"/>図</body>`, "  図 "},
		{`<body><a href="https://e.example">リンク</a></body>`, "  リンク  "},
		{"<body>改\n行</body>", " 改\n行 "},
	} {
		if got := StripHTML(tc.in); got != tc.want {
			t.Errorf("StripHTML(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
