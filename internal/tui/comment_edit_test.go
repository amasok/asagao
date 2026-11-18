package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/store"
)

// cursorTo moves the detail cursor onto the first item of kind (focusing the detail).
func (h *harness) cursorTo(kind itemKind) {
	h.t.Helper()
	if !h.m.inDetail() {
		h.key("l")
	}
	h.key("g")
	for range 60 {
		if it := h.m.currentItem(); it != nil && it.kind == kind {
			return
		}
		h.key("j")
	}
	h.t.Fatalf("no item of kind %d in the detail:\n%s", kind, h.screen())
}

func storyOps(t *testing.T, st *store.Store) []store.Op {
	t.Helper()
	ops, err := st.ListOps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return ops
}

// enter on my own comment offers to rewrite it in the editor (with its Markdown) or to
// delete it; a delete asks first, and "やめる" (the default) keeps it. Someone else's
// comment offers nothing. Reactions are shown under each comment.
func TestRewriteAndDeleteMyComment(t *testing.T) {
	st := fixtureStore(t)
	ctx := context.Background()
	at := testNow.Add(-time.Hour)
	if err := st.ReplaceStories(ctx, "t2", []asana.Story{
		{GID: "c-other", Type: "comment", Text: "ほかの人", CreatedBy: &asana.Ref{GID: "u2", Name: "Partner"}, CreatedAt: at,
			ReactionSummary: []asana.Reaction{{EmojiBase: "🎉", Count: 1}}},
		{GID: "c-me", Type: "comment", Text: "元の文", HTMLText: "<body><strong>元</strong>の文</body>",
			CreatedBy: &asana.Ref{GID: "me", Name: "Me"}, CreatedAt: at.Add(time.Minute),
			ReactionSummary: []asana.Reaction{{EmojiBase: "👍", Count: 2, Reacted: true}}},
	}, at); err != nil {
		t.Fatal(err)
	}
	var seen string
	m := New(ctx, Deps{Store: st, Now: func() time.Time { return testNow }, RunEditor: fakeEditor(t, func(s string) string {
		seen = s
		return strings.Replace(s, "**元**の文", "**新**しい文", 1)
	})})
	h := &harness{t: t, m: m}
	h.send(tea.WindowSizeMsg{Width: 140, Height: 40})
	h.run(m.Init())
	h.key("l")
	s := h.screen()
	if n := strings.Count(s, "enter で編集・削除"); n != 1 {
		t.Errorf("%d comments offered for editing, want only mine:\n%s", n, s)
	}
	for _, want := range []string{"👍* 2", "🎉 1"} {
		if !strings.Contains(s, want) {
			t.Errorf("reactions %q not shown:\n%s", want, s)
		}
	}

	h.cursorTo(itemComment)
	h.key("enter")
	if h.m.picker == nil || h.m.picker.kind != pickItemAction {
		t.Fatalf("no action list:\n%s", h.screen())
	}
	h.key("enter") // 書き直す（エディタ）
	if !strings.Contains(seen, "**元**の文") {
		t.Fatalf("the editor did not get the comment as Markdown:\n%s", seen)
	}
	ops := storyOps(t, st)
	if len(ops) != 1 || ops[0].Kind != store.OpUpdateStory || ops[0].Payload["story"] != "c-me" ||
		!strings.Contains(ops[0].Payload["html_text"].(string), "<strong>新</strong>") {
		t.Fatalf("outbox %+v", ops)
	}
	if s := h.screen(); !strings.Contains(s, "新しい文") || !strings.Contains(s, "コメントを書き直しました") {
		t.Errorf("the rewritten comment is not shown:\n%s", s)
	}

	// delete, but cancel at the confirmation
	h.cursorTo(itemComment)
	h.key("enter", "j", "enter")
	if h.m.picker == nil || h.m.picker.kind != pickConfirmDel || !strings.Contains(h.screen(), "取り消せません") {
		t.Fatalf("no confirmation:\n%s", h.screen())
	}
	h.key("enter") // やめる
	if n := len(storyOps(t, st)); n != 1 {
		t.Fatalf("cancelling queued something (%d entries)", n)
	}
	h.cursorTo(itemComment)
	h.key("enter", "j", "enter", "j", "enter")
	ops = storyOps(t, st)
	if len(ops) != 2 || ops[1].Kind != store.OpDeleteStory {
		t.Fatalf("outbox %+v", ops)
	}
	if s := h.screen(); strings.Contains(s, "新しい文") || strings.Contains(s, "enter で編集・削除") {
		t.Errorf("the deleted comment is still shown:\n%s", s)
	}
}

// enter on an attachment offers to open or delete it; the delete is confirmed first.
func TestDeleteAttachmentInTUI(t *testing.T) {
	st := fixtureStore(t)
	ctx := context.Background()
	if err := st.ReplaceAttachments(ctx, "t2", []asana.Attachment{{GID: "a1", Name: "見積.pdf"}}); err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, m: New(ctx, Deps{Store: st, Now: func() time.Time { return testNow }})}
	h.send(tea.WindowSizeMsg{Width: 140, Height: 40})
	h.run(h.m.Init())
	h.cursorTo(itemAttachment)
	h.key("enter")
	if s := h.screen(); !strings.Contains(s, "開く") || !strings.Contains(s, "削除する…") {
		t.Fatalf("no action list:\n%s", s)
	}
	h.key("j", "enter", "j", "enter")
	ops := storyOps(t, st)
	if len(ops) != 1 || ops[0].Kind != store.OpDeleteAttachment || ops[0].Payload["attachment"] != "a1" {
		t.Fatalf("outbox %+v", ops)
	}
	if s := h.screen(); strings.Contains(s, "見積.pdf") {
		t.Errorf("the deleted attachment is still shown:\n%s", s)
	}
}

// Deleting an image embedded in the description says so at the confirmation: the
// description loses the image too.
func TestDeleteInlineImageWarns(t *testing.T) {
	st := fixtureStore(t)
	ctx := context.Background()
	if err := st.UpsertTasks(ctx, []asana.Task{{GID: "t2", Name: "転居届を出す", DueOn: "2026-11-10", Assignee: &asana.Ref{GID: "me", Name: "Me"},
		Notes:       "見取り図\nhttps://app.asana.com/app/asana/-/get_asset?asset_id=777",
		Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}, Section: &asana.Ref{GID: "s2"}}}}}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceAttachments(ctx, "t2", []asana.Attachment{{GID: "777", Name: "図.png"}}); err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, m: New(ctx, Deps{Store: st, Now: func() time.Time { return testNow }})}
	h.send(tea.WindowSizeMsg{Width: 140, Height: 40})
	h.run(h.m.Init())
	h.cursorTo(itemAttachment)
	h.key("enter", "j", "enter")
	if s := h.screen(); !strings.Contains(s, "説明に埋め込まれたこの画像") {
		t.Errorf("the confirmation does not say the image is in the description:\n%s", s)
	}
}
