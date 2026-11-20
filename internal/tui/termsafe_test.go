package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asagao/internal/asana"
)

// A task name, notes or comment with escape sequences in it must not reach the terminal as
// control sequences: Bubble Tea passes APC (kitty graphics) and OSC 8 through as they are.
func TestTUIRenderIsTerminalSafe(t *testing.T) {
	const evil = "\x1b]52;c;ZXZpbA==\x07\x1b]8;;https://evil.example\x1b\\\x1b_Ga=T;AAAA\x1b\\\x1bP1$qm\x1b\\\u009b2J"
	st := fixtureStore(t)
	ctx := context.Background()
	ref := func(gid, name string) *asana.Ref { return &asana.Ref{GID: gid, Name: name} }
	if err := st.UpsertTasks(ctx, []asana.Task{{GID: "e1", Name: "悪い" + evil, Notes: "本文" + evil, DueOn: "2026-11-01",
		Assignee:    ref("me", "Me"),
		Memberships: []asana.Membership{{Project: ref("p1", ""), Section: ref("s1", "")}}}}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceStories(ctx, "e1", []asana.Story{{GID: "c1", Type: "comment", Text: "コメント" + evil,
		CreatedBy: ref("u2", "Partner"+evil)}}, testNow); err != nil {
		t.Fatal(err)
	}
	m := New(ctx, onStore(st, Deps{Now: func() time.Time { return testNow }}))
	h := &harness{t: t, m: m}
	h.send(tea.WindowSizeMsg{Width: 160, Height: 50})
	h.run(m.Init())
	if h.selected() != "e1" {
		t.Fatalf("selected %q, want e1\n%s", h.selected(), h.screen())
	}
	h.key("enter") // open the detail with notes and comments
	frame := h.m.render()
	if !strings.Contains(h.screen(), "本文") || !strings.Contains(h.screen(), "コメント") {
		t.Fatalf("the detail should show notes and comments:\n%s", h.screen())
	}
	for _, bad := range []string{"\x1b]", "\x1b_", "\x1bP", "\x07", "\u009b", "\x1b\\"} {
		if strings.Contains(frame, bad) {
			t.Errorf("frame contains %q", bad)
		}
	}
}
