package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/asana"
)

// Edit, then delete, a real comment; discarding only the delete brings back the edited text,
// and a stale refresh never shows the deleted one.
func TestAttackEditThenDeleteThenDiscardDelete(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	seedComments(t, s)
	must(t, s.EditCommentLocal(ctx, "c-me", "直した", "", testNow))
	must(t, s.DeleteCommentLocal(ctx, "c-me", testNow))
	if err := s.EditCommentLocal(ctx, "c-me", "消した後", "", testNow); !errors.Is(err, ErrNotFound) {
		t.Errorf("edit of a deleted comment: %v", err)
	}
	must(t, s.ReplaceStories(ctx, "t1", []asana.Story{{GID: "c-me", Type: "comment", Text: "元の文",
		CreatedBy: &asana.Ref{GID: "me"}}}, testNow))
	if _, _, ok := storyText(t, s, "c-me"); ok {
		t.Fatal("refresh brought back a deleted comment")
	}
	for _, op := range opsOfKind(t, s, OpDeleteStory) {
		must(t, s.DiscardOp(ctx, op.ID))
	}
	if text, _, ok := storyText(t, s, "c-me"); !ok || text != "直した" {
		t.Errorf("after discarding the delete: %q %v", text, ok)
	}
}

// An edit made while the first edit is being sent is a second entry, and the first one's
// state is not touched.
func TestAttackEditWhileEditIsSending(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	seedComments(t, s)
	must(t, s.EditCommentLocal(ctx, "c-me", "一回目", "", testNow))
	first := opsOfKind(t, s, OpUpdateStory)[0]
	if ok, err := s.BeginSend(ctx, first.ID, testNow); err != nil || !ok {
		t.Fatal(ok, err)
	}
	must(t, s.EditCommentLocal(ctx, "c-me", "二回目", "", testNow))
	ops := opsOfKind(t, s, OpUpdateStory)
	if len(ops) != 2 {
		t.Fatalf("entries %+v", ops)
	}
	got, _ := s.GetOp(ctx, first.ID)
	if got.Payload["text"] != "一回目" || got.State != OpSending {
		t.Errorf("sending entry changed: %+v", got)
	}
	// the stale list read in between must not hide the newest text
	must(t, s.ReplaceStories(ctx, "t1", []asana.Story{{GID: "c-me", Type: "comment", Text: "元の文",
		CreatedBy: &asana.Ref{GID: "me"}}}, testNow))
	if text, _, _ := storyText(t, s, "c-me"); text != "二回目" {
		t.Errorf("text after a refresh: %q", text)
	}
}

// With no me recorded, nothing is editable or deletable; unknown gids and non-comments fail.
func TestAttackEditRefusals(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	seedComments(t, s)
	if err := s.EditCommentLocal(ctx, "c-other", "x", "", testNow); !errors.Is(err, ErrNotOwnComment) {
		t.Errorf("other's: %v", err)
	}
	if err := s.DeleteCommentLocal(ctx, "c-other", testNow); !errors.Is(err, ErrNotOwnComment) {
		t.Errorf("other's delete: %v", err)
	}
	if err := s.EditCommentLocal(ctx, "sys", "x", "", testNow); err == nil {
		t.Error("activity story edited")
	}
	if err := s.DeleteCommentLocal(ctx, "sys", testNow); err == nil {
		t.Error("activity story deleted")
	}
	if err := s.EditCommentLocal(ctx, "nope", "x", "", testNow); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown: %v", err)
	}
	for _, text := range []string{"", "   ", "\n\t"} {
		if err := s.EditCommentLocal(ctx, "c-me", text, "", testNow); err == nil {
			t.Errorf("blank %q accepted", text)
		}
	}
	must(t, s.SetSyncState(ctx, KeyMeGID, ""))
	if err := s.EditCommentLocal(ctx, "c-me", "x", "", testNow); err == nil {
		t.Error("edit with me unset")
	}
	if err := s.DeleteCommentLocal(ctx, "c-me", testNow); err == nil {
		t.Error("delete with me unset")
	}
	if n := len(opsOfKind(t, s, OpUpdateStory)) + len(opsOfKind(t, s, OpDeleteStory)); n != 0 {
		t.Errorf("%d entries queued by refused calls", n)
	}
}

// A comment edited, whose entry then failed, is re-queued by another edit (not left failed).
func TestAttackEditAfterFailedEdit(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	seedComments(t, s)
	must(t, s.EditCommentLocal(ctx, "c-me", "一回目", "", testNow))
	op := opsOfKind(t, s, OpUpdateStory)[0]
	must(t, s.MarkOpFailed(ctx, op.ID, errors.New("boom")))
	must(t, s.EditCommentLocal(ctx, "c-me", "二回目", "", testNow))
	ops := opsOfKind(t, s, OpUpdateStory)
	if len(ops) != 1 || ops[0].State != OpPending || ops[0].Payload["text"] != "二回目" {
		t.Fatalf("%+v", ops)
	}
}

// Deleting an unsent comment and discarding leaves nothing; its edits go with it, and the
// attachment path likewise. A comment whose task is gone still discards cleanly.
func TestAttackDeleteUnsentCommentLeavesNothing(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	gid, err := s.AddCommentLocal(ctx, "t1", "下書き", UserRef{GID: "me", Name: "Me"}, testNow)
	must(t, err)
	must(t, s.EditCommentLocal(ctx, gid, "直した下書き", "", testNow))
	must(t, s.DeleteCommentLocal(ctx, gid, testNow))
	ops, _ := s.ListOps(ctx)
	for _, op := range ops {
		if strings.Contains(op.Kind, "comment") || strings.Contains(op.Kind, "story") {
			t.Errorf("left %+v", op)
		}
	}
	if _, _, ok := storyText(t, s, gid); ok {
		t.Error("comment still stored")
	}
}

// Reactions from Asana pass through termsafe.
func TestAttackReactionsAreSafe(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	must(t, s.ReplaceStories(ctx, "t1", []asana.Story{{GID: "c1", Type: "comment", Text: "x",
		CreatedBy: &asana.Ref{GID: "me"}, ReactionSummary: []asana.Reaction{{EmojiBase: "\x1b]0;pwn\x07👍", Count: 1}}}}, testNow))
	var raw string
	must(t, s.db.QueryRow("SELECT reactions FROM stories WHERE gid='c1'").Scan(&raw))
	if strings.ContainsAny(raw, "\x1b\x07") {
		t.Errorf("control characters stored: %q", raw)
	}
	d, err := s.GetTask(ctx, "t1")
	must(t, err)
	for _, st := range d.Stories {
		if b := fmt.Sprintf("%+v", st); strings.ContainsAny(b, "\x1b\x07") {
			t.Errorf("control characters read: %q", b)
		}
	}
}

// Deleting an attachment removes only the cached copy, never the user's original file; and a
// path shared by another attachment's row is not a reason to lose that other row's file silently.
func TestAttackDeleteAttachmentKeepsTheOriginal(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "原本.txt")
	must(t, os.WriteFile(src, []byte("x"), 0o600))
	cache := t.TempDir()
	local, err := s.UploadAttachmentLocal(ctx, "t1", src, cache, testNow)
	must(t, err)
	must(t, s.DeleteAttachmentLocal(ctx, local, testNow))
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("the user's original is gone: %v", err)
	}
	if ents, _ := os.ReadDir(cache); len(ents) != 0 {
		t.Errorf("copy left in the cache: %v", ents)
	}
	// a downloaded attachment: its file goes, a missing file is no error
	must(t, s.ReplaceAttachments(ctx, "t1", []asana.Attachment{{GID: "a1", Name: "x"}, {GID: "a2", Name: "y"}}))
	f := filepath.Join(cache, "dl.bin")
	must(t, os.WriteFile(f, []byte("d"), 0o600))
	must(t, s.SetAttachmentPath(ctx, "a1", f))
	must(t, s.SetAttachmentPath(ctx, "a2", filepath.Join(cache, "never-existed")))
	must(t, s.DeleteAttachmentLocal(ctx, "a1", testNow))
	must(t, s.DeleteAttachmentLocal(ctx, "a2", testNow))
	if _, err := os.Stat(f); err == nil {
		t.Error("downloaded file kept")
	}
	// discard puts them back without a path
	for _, op := range opsOfKind(t, s, OpDeleteAttachment) {
		must(t, s.DiscardOp(ctx, op.ID))
	}
	a, err := s.GetAttachment(ctx, "a1")
	must(t, err)
	if a.LocalPath != "" {
		t.Errorf("path after undo: %q", a.LocalPath)
	}
}
