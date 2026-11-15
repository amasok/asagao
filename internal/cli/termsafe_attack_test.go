package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asana-tui/internal/asana"
	"github.com/amasok/asana-tui/internal/config"
	"github.com/amasok/asana-tui/internal/store"
)

// Every escape-sequence family plus C1 controls, CR, BS and DEL.
const attack = "\x1b[2J\x1b]52;c;ZXZpbA==\x07\x1b]8;;https://evil.example\x1b\\\x1b_Ga=T;AAAA\x1b\\" +
	"\x1bP+q544e\x1b\\\x1bXsos\x1b\\\x1b^pm\x1b\\\u009b2J\u009d0;t\u009c\r\b\x7f"

// attackStore fills a store in a fresh data dir with Asana text carrying escapes in every
// place the CLI prints: tasks, notes, comments, custom fields, projects, status updates,
// tags, notifications and outbox entries.
func attackStore(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.MkdirAll(config.DataDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	st := createStoreAt(t, filepath.Join(config.DataDir(), "store.db"))
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	text := "テキスト" + attack
	must(st.UpsertProjects(ctx, "w1", []asana.Project{{GID: "p2", Name: "悪いプロジェクト" + attack, Members: []asana.Ref{{GID: "me"}}}}, "me"))
	must(st.ReplaceStatusUpdates(ctx, "p2", []asana.StatusUpdate{{GID: "su1", Title: "状況" + attack, Text: "本文" + attack,
		StatusType: "on_track", HTMLText: "<body>html" + attack + "</body>", Author: &asana.Ref{GID: "u2", Name: "Partner" + attack}}}))
	must(st.UpsertUsers(ctx, []asana.User{{GID: "u3", Name: "悪いユーザー" + attack}}))
	must(st.UpsertTags(ctx, []asana.Ref{{GID: "tg1", Name: "悪いタグ" + attack}}))
	must(st.UpsertTasks(ctx, []asana.Task{{GID: "e1", Name: "悪い" + attack, Notes: "本文" + attack,
		Assignee:    &asana.Ref{GID: "me", Name: "Me"},
		Tags:        []asana.Ref{{GID: "tg1", Name: "悪いタグ" + attack}},
		Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p2", Name: "悪いプロジェクト" + attack}, Section: &asana.Ref{GID: "s9", Name: "列" + attack}}},
		CustomFields: []asana.CustomField{
			{GID: "f-text", Name: "フィールド" + attack, ResourceSubtype: "text", TextValue: &text},
		}}}))
	must(st.ReplaceStories(ctx, "e1", []asana.Story{{GID: "c1", Type: "comment", Text: "コメント" + attack,
		CreatedBy: &asana.Ref{GID: "u3", Name: "悪いユーザー" + attack}}}, time.Now()))
	must(st.ReplaceNotifications(ctx, "w1", []asana.Notification{{GID: "n1", CreatedAt: time.Now(),
		CreatedBy: &asana.Ref{GID: "u3", Name: "悪いユーザー" + attack}, StoryResourceSubtype: "comment_added",
		Story:       &asana.Story{GID: "s1", Text: "通知" + attack},
		WorkObjects: []asana.WorkRef{{GID: "e1", ResourceType: "task", Name: "悪い" + attack}}}}))
	must(st.SetInboxState(ctx, store.InboxState{Status: store.InboxOK}))
	// a queued local change, as an MCP client could make one
	_, err := st.SetCustomFieldLocal(ctx, "e1", "f-text", "ローカル"+attack, time.Now())
	must(err)
	_ = st.Close()
}

var attackCommands = [][]string{
	{"task", "list"},
	{"task", "show", "e1"},
	{"search", "悪い"},
	{"project", "list"},
	{"project", "status", "p2"},
	{"tag", "list"},
	{"inbox"},
	{"outbox", "list"},
}

func TestCLIAttackTextIsTerminalSafe(t *testing.T) {
	attackStore(t)
	for _, args := range attackCommands {
		out, err := runCLI(t, args...)
		if err != nil {
			t.Errorf("%v: %v\n%s", args, err, out)
			continue
		}
		if !strings.Contains(out, "悪い") && !strings.Contains(out, "状況") && !strings.Contains(out, "ローカル") {
			t.Errorf("%v: the attack text should be shown (made harmless): %q", args, out)
		}
		assertNoControls(t, out)
	}
}

// --json stays valid JSON; nothing in it, raw or after decoding, is a control character.
// (Go's encoder does not escape U+0080–U+009F or DEL, so those must already be gone.)
func TestCLIJSONOutputIsTerminalSafe(t *testing.T) {
	attackStore(t)
	for _, args := range attackCommands {
		var cmdOut string
		var cmdErr error
		out := captureStdout(t, func() {
			cmdOut, cmdErr = runCLI(t, append(args, "--json")...)
		})
		if cmdErr != nil {
			t.Errorf("%v --json: %v\n%s", args, cmdErr, cmdOut)
			continue
		}
		if !json.Valid([]byte(out)) {
			t.Errorf("%v --json: not valid JSON: %q", args, out)
			continue
		}
		assertNoControls(t, out)
		var v any
		_ = json.Unmarshal([]byte(out), &v)
		walkStrings(v, func(s string) {
			for _, r := range s {
				if (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
					t.Errorf("%v --json: decoded string has %U: %q", args, r, s)
					return
				}
			}
		})
	}
}

func walkStrings(v any, f func(string)) {
	switch x := v.(type) {
	case string:
		f(x)
	case []any:
		for _, e := range x {
			walkStrings(e, f)
		}
	case map[string]any:
		for k, e := range x {
			f(k)
			walkStrings(e, f)
		}
	}
}
