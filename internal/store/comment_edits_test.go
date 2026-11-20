package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/readmodel"
)

// storyText returns a stored comment's text and html_text ("", "", false if it is not there).
func storyText(t *testing.T, s *Store, gid string) (text, html string, ok bool) {
	t.Helper()
	err := s.db.QueryRow("SELECT text, html_text FROM stories WHERE gid = ?", gid).Scan(&text, &html)
	if err != nil {
		return "", "", false
	}
	return text, html, true
}

func opsOfKind(t *testing.T, s *Store, kind Kind) []Op {
	t.Helper()
	ops, err := s.ListOps(context.Background())
	must(t, err)
	var out []Op
	for _, op := range ops {
		if op.Kind == kind {
			out = append(out, op)
		}
	}
	return out
}

// seedComments stores t1's comments from Asana: c-me (mine), c-other (someone else's) and
// an activity story.
func seedComments(t *testing.T, s *Store) {
	t.Helper()
	must(t, s.ReplaceStories(context.Background(), "t1", []asana.Story{
		{GID: "c-me", Type: "comment", ResourceSubtype: "comment_added", Text: "元の文", HTMLText: "<body>元の文</body>",
			CreatedBy: &asana.Ref{GID: "me", Name: "Me"}, CreatedAt: testNow.Add(-2 * 3600e9)},
		{GID: "c-other", Type: "comment", Text: "ほかの人", CreatedBy: &asana.Ref{GID: "u2", Name: "誰か"}, CreatedAt: testNow.Add(-3600e9)},
		{GID: "sys", Type: "system", Text: "assigned", CreatedBy: &asana.Ref{GID: "me"}},
	}, testNow.Add(-3600e9)))
}

// Editing my comment shows the new text at once and queues one update_story whose base is
// the text Asana has; more edits join it, and an edit back to that text drops it. Someone
// else's comment and an activity story are refused.
func TestEditCommentLocal(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	seedComments(t, s)

	must(t, s.EditCommentLocal(ctx, "c-me", "直した", "", testNow))
	if text, html, _ := storyText(t, s, "c-me"); text != "直した" || html != "" {
		t.Errorf("stored %q / %q", text, html)
	}
	must(t, s.EditCommentLocal(ctx, "c-me", "もう一度", "<body>もう一度</body>", testNow))
	ops := opsOfKind(t, s, OpUpdateStory)
	if len(ops) != 1 {
		t.Fatalf("update_story entries: %+v", ops)
	}
	op := ops[0]
	if op.TaskGID != "t1" || op.Payload["story"] != "c-me" || op.Payload["text"] != "もう一度" ||
		op.Payload["html_text"] != "<body>もう一度</body>" || op.Base["text"] != "元の文" || op.Base["html_text"] != "<body>元の文</body>" {
		t.Errorf("entry %+v", op)
	}
	d, err := s.GetTask(ctx, "t1")
	must(t, err)
	for _, st := range d.Stories {
		if st.GID == "c-me" && !st.Pending {
			t.Error("an edited comment not sent yet is not shown as pending")
		}
	}

	must(t, s.EditCommentLocal(ctx, "c-me", "元の文", "<body>元の文</body>", testNow))
	if ops := opsOfKind(t, s, OpUpdateStory); len(ops) != 0 {
		t.Errorf("an edit back to Asana's text left %+v", ops)
	}

	if err := s.EditCommentLocal(ctx, "c-other", "乗っ取り", "", testNow); !errors.Is(err, ErrNotOwnComment) {
		t.Errorf("someone else's comment: err = %v", err)
	}
	if err := s.DeleteCommentLocal(ctx, "c-other", testNow); !errors.Is(err, ErrNotOwnComment) {
		t.Errorf("deleting someone else's comment: err = %v", err)
	}
	if err := s.EditCommentLocal(ctx, "sys", "x", "", testNow); err == nil {
		t.Error("an activity story was edited")
	}
	if err := s.EditCommentLocal(ctx, "c-me", "  ", "", testNow); err == nil {
		t.Error("an empty comment was accepted")
	}
	if err := s.EditCommentLocal(ctx, "missing", "x", "", testNow); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing comment: err = %v", err)
	}
	if text, _, _ := storyText(t, s, "c-other"); text != "ほかの人" {
		t.Errorf("someone else's comment changed to %q", text)
	}
	if ops, _ := s.ListOps(ctx); len(ops) != 0 {
		t.Errorf("refused changes queued %+v", ops)
	}
}

// Without a logged-in user (before the first sync) no comment can be checked to be mine.
func TestEditCommentNeedsMe(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	seedComments(t, s)
	must(t, s.SetSyncState(ctx, keyMeGID, ""))
	if err := s.EditCommentLocal(ctx, "c-me", "x", "", testNow); err == nil {
		t.Error("edited without knowing who I am")
	}
	if err := s.DeleteCommentLocal(ctx, "c-me", testNow); err == nil {
		t.Error("deleted without knowing who I am")
	}
}

// A refresh of the comments read before the edit or the delete was sent keeps what the
// user did (#34: the list must not bring the old text or the deleted comment back), and
// discarding the entries puts Asana's version back.
func TestReplaceStoriesKeepsQueuedEdits(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	seedComments(t, s)
	must(t, s.ReplaceStories(ctx, "t1", []asana.Story{
		{GID: "c-me", Type: "comment", Text: "元の文", CreatedBy: &asana.Ref{GID: "me"}},
		{GID: "c-me2", Type: "comment", Text: "二つ目", CreatedBy: &asana.Ref{GID: "me"}},
	}, testNow))
	must(t, s.EditCommentLocal(ctx, "c-me", "直した", "", testNow))
	must(t, s.DeleteCommentLocal(ctx, "c-me2", testNow))
	if _, _, ok := storyText(t, s, "c-me2"); ok {
		t.Fatal("the deleted comment is still shown")
	}

	stale := []asana.Story{
		{GID: "c-me", Type: "comment", Text: "元の文", CreatedBy: &asana.Ref{GID: "me"}},
		{GID: "c-me2", Type: "comment", Text: "二つ目", CreatedBy: &asana.Ref{GID: "me"}},
	}
	must(t, s.ReplaceStories(ctx, "t1", stale, testNow))
	if text, _, _ := storyText(t, s, "c-me"); text != "直した" {
		t.Errorf("after a refresh the edited comment reads %q", text)
	}
	if _, _, ok := storyText(t, s, "c-me2"); ok {
		t.Error("a refresh brought the deleted comment back")
	}

	for _, op := range append(opsOfKind(t, s, OpUpdateStory), opsOfKind(t, s, OpDeleteStory)...) {
		must(t, s.DiscardOp(ctx, op.ID))
	}
	if text, _, _ := storyText(t, s, "c-me"); text != "元の文" {
		t.Errorf("after discarding the edit: %q", text)
	}
	if text, _, ok := storyText(t, s, "c-me2"); !ok || text != "二つ目" {
		t.Errorf("after discarding the delete: %q, %v", text, ok)
	}
	d, err := s.GetTask(ctx, "t1")
	must(t, err)
	var found bool
	for _, st := range d.Stories {
		if st.GID == "c-me2" {
			found = st.CreatedBy != nil && st.CreatedBy.GID == "me" && st.Type == "comment"
		}
	}
	if !found {
		t.Errorf("the comment put back lost who wrote it: %+v", d.Stories)
	}
}

// A comment not sent yet is edited by rewriting its add_comment, and deleted by dropping it.
func TestEditUnsentComment(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	gid, err := s.AddCommentLocal(ctx, "t1", "書きかけ", UserRef{GID: "me", Name: "Me"}, testNow)
	must(t, err)
	must(t, s.EditCommentLocal(ctx, gid, "書き直し", "<body><strong>書き直し</strong></body>", testNow))
	ops, err := s.ListOps(ctx)
	must(t, err)
	if len(ops) != 1 || ops[0].Kind != OpAddComment || ops[0].Payload["text"] != "書き直し" ||
		ops[0].Payload["html_text"] != "<body><strong>書き直し</strong></body>" {
		t.Fatalf("outbox %+v", ops)
	}
	must(t, s.EditCommentLocal(ctx, gid, "平文に戻す", "", testNow))
	ops, _ = s.ListOps(ctx)
	if _, ok := ops[0].Payload["html_text"]; ok || ops[0].Payload["text"] != "平文に戻す" {
		t.Errorf("payload %+v keeps the old rich text", ops[0].Payload)
	}

	must(t, s.DeleteCommentLocal(ctx, gid, testNow))
	if ops, _ := s.ListOps(ctx); len(ops) != 0 {
		t.Errorf("deleting an unsent comment left %+v", ops)
	}
	if _, _, ok := storyText(t, s, gid); ok {
		t.Error("the unsent comment is still shown")
	}
}

// A comment whose add_comment is being sent (or checked) keeps it as it was sent: the edit
// and the delete are queued with the local gid, and once Asana's comment is recorded they
// apply to it.
func TestEditCommentBeingSent(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	gid, err := s.AddCommentLocal(ctx, "t1", "送信中", UserRef{GID: "me", Name: "Me"}, testNow)
	must(t, err)
	add := opsOfKind(t, s, OpAddComment)[0]
	if ok, err := s.BeginSend(ctx, add.ID, testNow); err != nil || !ok {
		t.Fatal(ok, err)
	}
	must(t, s.EditCommentLocal(ctx, gid, "送信中に直した", "", testNow))
	got, err := s.GetOp(ctx, add.ID)
	must(t, err)
	if got.Payload["text"] != "送信中" {
		t.Errorf("the add_comment being sent was rewritten: %+v", got.Payload)
	}
	if real, waiting, err := s.LocalResult(ctx, readmodel.ResultStory, gid); real != "" || !waiting || err != nil {
		t.Errorf("LocalResult = %q, %v, %v; want it to wait", real, waiting, err)
	}

	must(t, s.CompleteComment(ctx, add.ID, gid, "t1", asana.Story{GID: "c-real", Type: "comment", Text: "送信中",
		CreatedBy: &asana.Ref{GID: "me", Name: "Me"}, CreatedAt: testNow}))
	if text, _, _ := storyText(t, s, "c-real"); text != "送信中に直した" {
		t.Errorf("Asana's comment shows %q, want the queued edit", text)
	}
	if real, waiting, err := s.LocalResult(ctx, readmodel.ResultStory, gid); real != "c-real" || waiting || err != nil {
		t.Errorf("LocalResult = %q, %v, %v", real, waiting, err)
	}
	ops := opsOfKind(t, s, OpUpdateStory)
	if len(ops) != 1 || ops[0].Payload["story"] != gid {
		t.Fatalf("update_story %+v", ops)
	}

	// Deleting one being sent: it disappears now and stays deleted when Asana's arrives.
	gid2, err := s.AddCommentLocal(ctx, "t1", "消す", UserRef{GID: "me", Name: "Me"}, testNow)
	must(t, err)
	add2 := opsOfKind(t, s, OpAddComment)[0]
	must(t, s.MarkOpVerify(ctx, add2.ID, "")) // may have reached Asana: checked first
	must(t, s.DeleteCommentLocal(ctx, gid2, testNow))
	if _, err := s.GetOp(ctx, add2.ID); err != nil {
		t.Fatalf("the add_comment being checked was dropped: %v", err)
	}
	must(t, s.CompleteComment(ctx, add2.ID, gid2, "t1", asana.Story{GID: "c-real2", Type: "comment", Text: "消す",
		CreatedBy: &asana.Ref{GID: "me"}, CreatedAt: testNow}))
	if _, _, ok := storyText(t, s, "c-real2"); ok {
		t.Error("a comment deleted while it was being sent came back")
	}
}

// Discarding an add_comment drops the edits and deletes queued for its comment.
func TestDiscardCommentDropsItsEdits(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	gid, err := s.AddCommentLocal(ctx, "t1", "失敗する", UserRef{GID: "me"}, testNow)
	must(t, err)
	add := opsOfKind(t, s, OpAddComment)[0]
	must(t, s.MarkOpFailed(ctx, add.ID, errors.New("400")))
	must(t, s.CountAttempt(ctx, add.ID, testNow))
	must(t, s.EditCommentLocal(ctx, gid, "直した", "", testNow))
	if n := len(opsOfKind(t, s, OpUpdateStory)); n != 1 {
		t.Fatalf("%d update_story entries", n)
	}
	must(t, s.DiscardOp(ctx, add.ID))
	if ops, _ := s.ListOps(ctx); len(ops) != 0 {
		t.Errorf("left %+v", ops)
	}
}

// Asana's emoji reactions are kept with the comment, without terminal control characters.
func TestStoryReactions(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	must(t, s.ReplaceStories(ctx, "t1", []asana.Story{
		{GID: "c1", Type: "comment", Text: "x", ReactionSummary: []asana.Reaction{
			{EmojiBase: "👍", Variant: "👍🏽\x1b[2J", Count: 2, Reacted: true}, {EmojiBase: "🎉", Count: 1}}},
		{GID: "c2", Type: "comment", Text: "y"},
	}, testNow))
	d, err := s.GetTask(ctx, "t1")
	must(t, err)
	byGID := map[string]StoryView{}
	for _, st := range d.Stories {
		byGID[st.GID] = st
	}
	r := byGID["c1"].Reactions
	if len(r) != 2 || r[0].EmojiBase != "👍" || r[0].Variant != "👍🏽[2J" || r[0].Count != 2 || !r[0].Reacted || r[1].Count != 1 {
		t.Errorf("reactions %+v", r)
	}
	if len(byGID["c2"].Reactions) != 0 {
		t.Errorf("c2 has %+v", byGID["c2"].Reactions)
	}
	// Refreshed without reactions: they are gone.
	must(t, s.ReplaceStories(ctx, "t1", []asana.Story{{GID: "c1", Type: "comment", Text: "x"}}, testNow))
	d, _ = s.GetTask(ctx, "t1")
	if len(d.Stories) != 1 || len(d.Stories[0].Reactions) != 0 {
		t.Errorf("after a refresh: %+v", d.Stories)
	}
}

// Deleting an attachment hides it at once and keeps it hidden through refreshes until sent;
// its downloaded file is removed at once. Discarding the delete puts it back as it was,
// without the file (downloaded again when opened). One not uploaded yet is just dropped,
// with its copy.
func TestDeleteAttachmentLocal(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	atts := []asana.Attachment{{GID: "a1", Name: "図面.pdf", Host: "asana", ViewURL: "https://v", Size: 1234, CreatedAt: testNow},
		{GID: "a2", Name: "残す.png"}}
	must(t, s.ReplaceAttachments(ctx, "t1", atts))
	cache := t.TempDir()
	s.SetAttachmentDir(cache)
	downloaded := filepath.Join(cache, "a1-図面.pdf")
	must(t, os.WriteFile(downloaded, []byte("pdf"), 0o600))
	must(t, s.SetAttachmentPath(ctx, "a1", downloaded))
	must(t, s.DeleteAttachmentLocal(ctx, "a1", testNow))
	if _, err := os.Stat(downloaded); !os.IsNotExist(err) {
		t.Errorf("the downloaded file of a deleted attachment is still there: %v", err)
	}
	if _, err := s.GetAttachment(ctx, "a1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted attachment still there: %v", err)
	}
	ops := opsOfKind(t, s, OpDeleteAttachment)
	if len(ops) != 1 || ops[0].TaskGID != "t1" || ops[0].Payload["attachment"] != "a1" || ops[0].Payload["name"] != "図面.pdf" {
		t.Fatalf("entries %+v", ops)
	}
	must(t, s.ReplaceAttachments(ctx, "t1", atts))
	must(t, s.UpsertAttachment(ctx, "t1", atts[0]))
	if _, err := s.GetAttachment(ctx, "a1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a refresh brought the deleted attachment back: %v", err)
	}
	if _, err := s.GetAttachment(ctx, "a2"); err != nil {
		t.Errorf("the other attachment: %v", err)
	}
	must(t, s.DiscardOp(ctx, ops[0].ID))
	a, err := s.GetAttachment(ctx, "a1")
	must(t, err)
	if a.Name != "図面.pdf" || a.Size != 1234 || a.LocalPath != "" || a.ViewURL != "https://v" {
		t.Errorf("put back as %+v (no local file: it was removed)", a)
	}
	if err := s.DeleteAttachmentLocal(ctx, "missing", testNow); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing attachment: err = %v", err)
	}

	src := filepath.Join(t.TempDir(), "メモ.txt")
	must(t, os.WriteFile(src, []byte("x"), 0o600))
	local, err := s.UploadAttachmentLocal(ctx, "t1", src, cache, testNow)
	must(t, err)
	copied, err := s.GetAttachment(ctx, local)
	must(t, err)
	must(t, s.DeleteAttachmentLocal(ctx, local, testNow))
	if _, err := os.Stat(copied.LocalPath); !os.IsNotExist(err) {
		t.Errorf("the copy of an unsent upload is still there: %v", err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Errorf("the user's own file was touched: %v", err)
	}
	if ops := opsOfKind(t, s, OpUploadAttachment); len(ops) != 0 {
		t.Errorf("the unsent upload is still queued: %+v", ops)
	}
	if ops := opsOfKind(t, s, OpDeleteAttachment); len(ops) != 0 {
		t.Errorf("a delete was queued for an unsent upload: %+v", ops)
	}
}

// An attachment deleted while its upload is being sent stays deleted once it is uploaded.
func TestDeleteAttachmentBeingUploaded(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	src := filepath.Join(t.TempDir(), "メモ.txt")
	must(t, os.WriteFile(src, []byte("x"), 0o600))
	local, err := s.UploadAttachmentLocal(ctx, "t1", src, t.TempDir(), testNow)
	must(t, err)
	up := opsOfKind(t, s, OpUploadAttachment)[0]
	if ok, err := s.BeginSend(ctx, up.ID, testNow); err != nil || !ok {
		t.Fatal(ok, err)
	}
	must(t, s.DeleteAttachmentLocal(ctx, local, testNow))
	// The upload may be sent again from its copy: it stays until the delete is sent.
	copyPath, _ := up.Payload["path"].(string)
	if _, err := os.Stat(copyPath); err != nil {
		t.Errorf("the copy an upload being sent needs was removed: %v", err)
	}
	if del := opsOfKind(t, s, OpDeleteAttachment); len(del) != 1 || del[0].Payload["path"] != copyPath {
		t.Errorf("delete %+v does not carry the copy to remove", del)
	}
	must(t, s.CompleteUpload(ctx, up.ID, local, "a-real", "https://v"))
	if _, err := s.GetAttachment(ctx, "a-real"); !errors.Is(err, ErrNotFound) {
		t.Errorf("uploaded attachment shown although deleted: %v", err)
	}
	must(t, s.ReplaceAttachments(ctx, "t1", []asana.Attachment{{GID: "a-real", Name: "メモ.txt"}}))
	if _, err := s.GetAttachment(ctx, "a-real"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a refresh brought it back: %v", err)
	}
	if real, waiting, _ := s.LocalResult(ctx, readmodel.ResultAttachment, local); real != "a-real" || waiting {
		t.Errorf("LocalResult = %q, %v", real, waiting)
	}
}

// A failed edit or delete is not applied again by a refresh: Asana's version shows, as for
// a failed task update (the user sees it did not go through and can retry or discard it).
func TestReplaceStoriesSkipsFailedEdits(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	seedComments(t, s)
	must(t, s.EditCommentLocal(ctx, "c-me", "直した", "", testNow))
	must(t, s.ReplaceAttachments(ctx, "t1", []asana.Attachment{{GID: "a1", Name: "x.png"}}))
	must(t, s.DeleteAttachmentLocal(ctx, "a1", testNow))
	ops, err := s.ListOps(ctx)
	must(t, err)
	for _, op := range ops {
		must(t, s.MarkOpFailed(ctx, op.ID, errors.New("403")))
	}
	must(t, s.ReplaceStories(ctx, "t1", []asana.Story{{GID: "c-me", Type: "comment", Text: "元の文", CreatedBy: &asana.Ref{GID: "me"}}}, testNow))
	must(t, s.ReplaceAttachments(ctx, "t1", []asana.Attachment{{GID: "a1", Name: "x.png"}}))
	if text, _, _ := storyText(t, s, "c-me"); text != "元の文" {
		t.Errorf("a failed edit was applied again: %q", text)
	}
	d, err := s.GetTask(ctx, "t1")
	must(t, err)
	for _, st := range d.Stories {
		if st.GID == "c-me" && st.Pending {
			t.Error("Asana's text of a comment whose edit failed is shown as not sent")
		}
	}
	if _, err := s.GetAttachment(ctx, "a1"); err != nil {
		t.Errorf("a failed delete hid the attachment: %v", err)
	}
}
