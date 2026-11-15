package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/amasok/asana-tui/internal/asana"
	"github.com/amasok/asana-tui/internal/termsafe"
)

// Payloads an attacker can put into any Asana text: every kind of escape sequence the
// terminal acts on, both string terminators, and the 8-bit C1 forms.
var attackPayloads = []string{
	"\x1b[2J\x1b[H",                       // CSI: clear the screen
	"\x1b]52;c;ZXZpbA==\x07",              // OSC 52 clipboard write, BEL terminator
	"\x1b]52;c;ZXZpbA==\x1b\\",            // OSC 52, ESC \ terminator
	"\x1b]8;;https://evil.example\x1b\\x", // OSC 8 fake link
	"\x1b_Ga=T,f=100;AAAA\x1b\\",          // APC: kitty graphics
	"\x1bP+q544e\x1b\\",                   // DCS: XTGETTCAP query
	"\x1bXsos\x1b\\",                      // SOS
	"\x1b^pm\x1b\\",                       // PM
	"\u009b2J",                            // 8-bit CSI (UTF-8 encoded U+009B)
	"\u009d52;c;ZXZpbA==\u009c",           // 8-bit OSC ... 8-bit ST
	"\x9b2J",                              // raw 8-bit CSI byte (invalid UTF-8)
	"a\rb\bc\x7f",                         // CR overwrite, backspace, DEL
}

func assertSafe(t *testing.T, where string, s string) {
	t.Helper()
	if !termsafe.Safe(s) {
		t.Errorf("%s: control characters in %q", where, s)
	}
}

// A text custom field's value is stored as a JSON string ("\u001b..."), not an object or
// array; decoding it must not bring the control characters back.
func TestStoreCustomFieldTextValueIsTerminalSafe(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	for i, p := range attackPayloads {
		gid := fmt.Sprintf("cf%d", i)
		val := "値" + p
		if err := s.UpsertTasks(ctx, []asana.Task{{GID: gid, Name: "フィールド" + gid,
			CustomFields: []asana.CustomField{
				{GID: "f-text", Name: "テキスト" + p, ResourceSubtype: "text", TextValue: &val},
				{GID: "f-enum", Name: "選択", ResourceSubtype: "enum",
					EnumOptions: []asana.EnumOption{{GID: "o1", Name: "選択肢" + p}},
					EnumValue:   &asana.EnumOption{GID: "o1", Name: "選択肢" + p}},
			}}}); err != nil {
			t.Fatal(err)
		}
		d, err := s.GetTask(ctx, gid)
		if err != nil {
			t.Fatal(err)
		}
		if len(d.CustomFields) == 0 {
			t.Fatalf("%s: no custom fields", gid)
		}
		for _, f := range d.CustomFields {
			assertSafe(t, "GetTask field name", f.Name)
			assertSafe(t, "GetTask field display", f.Display)
			if v, ok := f.Value.(string); ok {
				assertSafe(t, "GetTask field value ("+f.Type+")", v)
			}
			for _, o := range f.Options {
				assertSafe(t, "GetTask enum option", o.Name)
			}
		}
		ts, err := s.ListTasks(ctx, TaskFilter{Query: "フィールド" + gid})
		if err != nil {
			t.Fatal(err)
		}
		for _, x := range ts {
			for _, f := range x.CustomFields {
				if v, ok := f.Value.(string); ok {
					assertSafe(t, "ListTasks field value ("+f.Type+")", v)
				}
				assertSafe(t, "ListTasks field display", f.Display)
			}
		}
	}

	// Written locally (e.g. by an MCP client's update_task) rather than synced.
	if _, err := s.SetCustomFieldLocal(ctx, "cf0", "f-text", "ローカル\x1b]52;c;Zm9v\x07", time.Now()); err != nil {
		t.Fatal(err)
	}
	d, err := s.GetTask(ctx, "cf0")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range d.CustomFields {
		if v, ok := f.Value.(string); ok {
			assertSafe(t, "local field value", v)
		}
	}
	ops, err := s.ListOps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range ops {
		for k, v := range op.Payload {
			if str, ok := v.(string); ok {
				assertSafe(t, "outbox payload "+k, str)
			}
		}
		assertSafe(t, "outbox summary", DescribeChange(op))
	}
}

// Every other kind of text the store hands out: names of projects, sections, users, tags,
// notifications, attachments.
func TestStoreOtherTextsAreTerminalSafe(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	p := attackPayloads[1] + attackPayloads[4] + attackPayloads[9]
	if err := s.UpsertWorkspaces(ctx, []asana.Workspace{{GID: "w1", Name: "WS" + p}}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertProjects(ctx, "w1", []asana.Project{{GID: "p1", Name: "プロジェクト" + p}}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertUsers(ctx, []asana.User{{GID: "u1", Name: "ユーザー" + p}}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertTags(ctx, []asana.Ref{{GID: "tg1", Name: "タグ" + p}}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceNotifications(ctx, "w1", []asana.Notification{{GID: "n1", CreatedAt: time.Now(),
		CreatedBy:            &asana.Ref{GID: "u1", Name: "誰か" + p},
		StoryResourceSubtype: "comment_added",
		Story:                &asana.Story{GID: "s1", Text: "通知" + p},
		WorkObjects:          []asana.WorkRef{{GID: "t-x", ResourceType: "task", Name: "対象" + p}}}}); err != nil {
		t.Fatal(err)
	}

	ps, err := s.ListProjects(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range ps {
		assertSafe(t, "project name", x.Name)
	}
	us, err := s.ListUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range us {
		assertSafe(t, "user name", x.Name)
	}
	tags, err := s.ListTags(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range tags {
		assertSafe(t, "tag name", x.Name)
	}
	ns, err := s.ListNotifications(ctx, NotificationFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ns) != 1 {
		t.Fatalf("notifications: %v", ns)
	}
	for _, x := range []string{ns[0].ActorName, ns[0].Text, ns[0].TargetName} {
		assertSafe(t, "notification", x)
	}
}
