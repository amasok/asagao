package sync

import (
	"context"
	"os"

	"github.com/amasok/asagao/internal/asana/model"
	localgid "github.com/amasok/asagao/internal/domain/gid"
	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/readmodel"
)

// Sending comments and attachments: adding, editing and deleting them.

func init() {
	story := localRef{key: "story", result: readmodel.ResultStory}
	attachment := localRef{key: "attachment", result: readmodel.ResultAttachment}
	register(outbox.KindAddComment, opHandler{send: sends((*Syncer).pushComment), find: findComment})
	register(outbox.KindUpdateStory, opHandler{send: sends((*Syncer).pushUpdateStory), local: story})
	register(outbox.KindDeleteStory, opHandler{send: sends((*Syncer).pushDeleteItem), local: story})
	register(outbox.KindUploadAttachment, opHandler{send: sends((*Syncer).pushUpload), find: findUpload})
	register(outbox.KindDeleteAttachment, opHandler{send: sends((*Syncer).pushDeleteItem), local: attachment})
}

func (s *Syncer) pushComment(ctx context.Context, op outbox.Op) error {
	if localgid.IsLocal(op.TaskGID) {
		return &model.Error{Status: 404} // its create failed
	}
	text, _ := op.Payload["text"].(string)
	htmlText, _ := op.Payload["html_text"].(string)
	localGID, _ := op.Payload["story_gid"].(string)
	st, err := s.API.AddComment(ctx, op.TaskGID, text, htmlText)
	if err != nil {
		return err
	}
	return sentNotSaved(s.Store.CompleteComment(ctx, op.ID, localGID, op.TaskGID, *st))
}

func (s *Syncer) pushUpload(ctx context.Context, op outbox.Op) error {
	task, err := s.Store.RealGID(ctx, op.TaskGID)
	if err != nil {
		return err
	}
	if localgid.IsLocal(task) {
		return &model.Error{Status: 404} // its create failed
	}
	local, _ := op.Payload["attachment"].(string)
	name, _ := op.Payload["name"].(string)
	path, _ := op.Payload["path"].(string)
	data, err := os.ReadFile(path)
	if err != nil {
		return &model.Error{Status: 400} // the copied file is gone: cannot be retried
	}
	att, err := s.API.UploadAttachment(ctx, task, name, data)
	if err != nil {
		return err
	}
	return sentNotSaved(s.Store.CompleteUpload(ctx, op.ID, local, att.GID, att.ViewURL))
}

// pushUpdateStory edits a comment; sending it twice is harmless (PUT). A comment deleted
// in Asana answers 404: the edit is dropped (dropGone).
func (s *Syncer) pushUpdateStory(ctx context.Context, op outbox.Op) error {
	gid, _ := op.Payload["story"].(string)
	real, _, err := s.Store.LocalResult(ctx, readmodel.ResultStory, gid)
	if err != nil {
		return err
	}
	if real == "" {
		return &model.Error{Status: 404} // its add_comment was dropped
	}
	text, _ := op.Payload["text"].(string)
	htmlText, _ := op.Payload["html_text"].(string)
	st, err := s.API.UpdateStory(ctx, real, text, htmlText)
	if err != nil {
		return err
	}
	if err := s.Store.CompleteOp(ctx, op.ID); err != nil {
		return sentNotSaved(err)
	}
	_ = s.Store.ApplyStory(ctx, real, st.Text, st.HTMLText) // the next fetch fixes a failure
	return nil
}

// pushDeleteItem deletes a comment or an attachment. Sending it twice is harmless: a 404 means
// it is gone already (deleted elsewhere, or by an earlier request whose answer was lost),
// which is what was asked.
func (s *Syncer) pushDeleteItem(ctx context.Context, op outbox.Op) error {
	ref := handlers[op.Kind].local
	real, _, err := s.Store.LocalResult(ctx, ref.result, payloadString(op, ref.key))
	if err != nil {
		return err
	}
	if real != "" { // "": its create was dropped, so there is nothing in Asana to delete
		if op.Kind == outbox.KindDeleteStory {
			err = s.API.DeleteStory(ctx, real)
		} else {
			err = s.API.DeleteAttachment(ctx, real)
		}
		if err != nil && !isNotFound(err) {
			return err
		}
	}
	if err := s.Store.CompleteOp(ctx, op.ID); err != nil {
		return sentNotSaved(err)
	}
	// The copy an upload kept until it was sent (store.DeleteAttachmentLocal): no longer needed.
	if path, _ := op.Payload["path"].(string); path != "" {
		_ = s.Store.RemoveAttachmentFile(path) // only inside the attachments folder
	}
	return nil
}

// findComment looks for the comment an add_comment posted (see reconcile).
func findComment(s *Syncer, ctx context.Context, op outbox.Op, f finder) (outcome, match, error) {
	str := func(k string) string { return payloadString(op, k) }
	stories, err := s.API.Stories(ctx, op.TaskGID)
	if err != nil {
		return sent, notFound, err
	}
	used, err := s.taken(ctx, readmodel.ResultStory)
	if err != nil {
		return sent, notFound, err
	}
	st, m := pick(stories, func(st model.Story) bool {
		return !used[st.GID] && st.Type == "comment" && f.byMe(st.CreatedBy) && f.recent(st.CreatedAt) &&
			sameComment(st, str("text"), str("html_text"))
	})
	if m != matched {
		return sent, m, nil
	}
	return sent, matched, s.Store.CompleteComment(ctx, op.ID, str("story_gid"), op.TaskGID, st)
}

// findUpload looks for the attachment an upload_attachment added (see reconcile).
func findUpload(s *Syncer, ctx context.Context, op outbox.Op, f finder) (outcome, match, error) {
	str := func(k string) string { return payloadString(op, k) }
	atts, err := s.API.Attachments(ctx, op.TaskGID)
	if err != nil {
		return sent, notFound, err
	}
	used, err := s.taken(ctx, readmodel.ResultAttachment)
	if err != nil {
		return sent, notFound, err
	}
	a, m := pick(atts, func(a model.Attachment) bool { return !used[a.GID] && a.Name == str("name") && f.recent(a.CreatedAt) })
	if m != matched {
		return sent, m, nil
	}
	return sent, matched, s.Store.CompleteUpload(ctx, op.ID, str("attachment"), a.GID, a.ViewURL)
}
