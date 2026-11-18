package mcpserver

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/store"
)

const attack = "\x1b[2J\x1b]52;c;ZXZpbA==\x07\x1b]8;;https://evil.example\x1b\\\x1b_Ga=T;AAAA\x1b\\" +
	"\x1bP+q544e\x1b\\\u009b2J\u009d0;t\u009c\r\b\x7f"

func hasControl(s string) bool {
	for _, r := range s {
		if (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return true
		}
	}
	return false
}

// checkResult fails if any text of a tool result (the text content and every string of the
// structured content) has a terminal control character.
func checkResult(t *testing.T, name string, res *mcp.CallToolResult) {
	t.Helper()
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok && hasControl(tc.Text) {
			t.Errorf("%s: control characters in text content: %q", name, tc.Text)
		}
	}
	if res.StructuredContent == nil {
		return
	}
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			if hasControl(x) {
				t.Errorf("%s: control characters in %q", name, x)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		case map[string]any:
			for k, e := range x {
				walk(k)
				walk(e)
			}
		}
	}
	walk(v)
}

func TestToolOutputIsTerminalSafe(t *testing.T) {
	st := seed(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	text := "テキスト" + attack
	must(st.UpsertProjects(ctx, "w1", []asana.Project{{GID: "p9", Name: "悪い" + attack, Members: []asana.Ref{{GID: "me"}}}}, "me"))
	must(st.ReplaceStatusUpdates(ctx, "p9", []asana.StatusUpdate{{GID: "su1", Title: "状況" + attack, Text: "本文" + attack,
		StatusType: "on_track", Author: &asana.Ref{GID: "u9", Name: "誰か" + attack}}}))
	must(st.UpsertUsers(ctx, []asana.User{{GID: "u9", Name: "誰か" + attack}}))
	must(st.UpsertTags(ctx, []asana.Tag{{GID: "tg9", Name: "タグ" + attack}}))
	must(st.UpsertTasks(ctx, []asana.Task{{GID: "e1", Name: "悪い" + attack, Notes: "本文" + attack,
		Assignee: &asana.Ref{GID: "me", Name: "Me"}, Workspace: &asana.Ref{GID: "w1"},
		Tags:        []asana.Ref{{GID: "tg9", Name: "タグ" + attack}},
		Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p9"}, Section: &asana.Ref{GID: "s9", Name: "列" + attack}}},
		CustomFields: []asana.CustomField{
			{GID: "f-text", Name: "フィールド" + attack, ResourceSubtype: "text", TextValue: &text},
			{GID: "f-enum", Name: "選択" + attack, ResourceSubtype: "enum",
				EnumOptions: []asana.EnumOption{{GID: "o1", Name: "選択肢" + attack}},
				EnumValue:   &asana.EnumOption{GID: "o1", Name: "選択肢" + attack}},
		}}}))
	must(st.ReplaceStories(ctx, "e1", []asana.Story{{GID: "c1", Type: "comment", Text: "コメント" + attack,
		CreatedBy: &asana.Ref{GID: "u9", Name: "誰か" + attack}}}, time.Now()))
	must(st.ReplaceNotifications(ctx, "w1", []asana.Notification{{GID: "n1", CreatedAt: time.Now(),
		CreatedBy: &asana.Ref{GID: "u9", Name: "誰か" + attack}, StoryResourceSubtype: "comment_added",
		Story:       &asana.Story{GID: "s1", Text: "通知" + attack},
		WorkObjects: []asana.WorkRef{{GID: "e1", ResourceType: "task", Name: "悪い" + attack}}}}))
	must(st.SetInboxState(ctx, store.InboxState{Status: store.InboxOK}))

	cs := connect(t, &Server{Store: st})
	// writes from the MCP client itself are read back safely too
	for _, w := range []struct {
		name string
		args map[string]any
	}{
		{"update_task", map[string]any{"gid": "e1", "notes": "MCP" + attack}},
		{"add_comment", map[string]any{"gid": "e1", "text": "MCPコメント" + attack}},
		{"create_task", map[string]any{"name": "MCP作成" + attack}},
	} {
		res := call(t, cs, w.name, w.args, nil)
		if res.IsError {
			t.Fatalf("%s: %s", w.name, toolError(res))
		}
		checkResult(t, w.name, res)
	}
	for _, r := range []struct {
		name string
		args map[string]any
	}{
		{"get_task", map[string]any{"gid": "e1"}},
		{"list_tasks", nil},
		{"list_tasks", map[string]any{"project": "p9"}},
		{"search_tasks", map[string]any{"query": "悪い"}},
		{"list_projects", nil},
		{"get_project", map[string]any{"project": "p9"}},
		{"list_users", nil},
		{"list_tags", nil},
		{"list_inbox", nil},
		{"sync_status", nil},
	} {
		res := call(t, cs, r.name, r.args, nil)
		if res.IsError {
			t.Errorf("%s %v: %s", r.name, r.args, toolError(res))
			continue
		}
		checkResult(t, r.name, res)
	}
}
