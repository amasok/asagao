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
	registerOf(outbox.KindAddComment, handlerOf[outbox.AddComment]{send: sends((*Pusher).pushComment), find: findComment})
	registerOf(outbox.KindUpdateStory, handlerOf[outbox.UpdateStory]{send: sends((*Pusher).pushUpdateStory),
		local: func(p outbox.UpdateStory) string { return p.Story }, localResult: readmodel.ResultStory})
	registerOf(outbox.KindDeleteStory, handlerOf[outbox.DeleteStory]{
		send: sends(func(s *Pusher, ctx context.Context, op outbox.Op, p outbox.DeleteStory) error {
			return s.pushDeleteItem(ctx, op, readmodel.ResultStory, p.Story, "")
		}),
		local: func(p outbox.DeleteStory) string { return p.Story }, localResult: readmodel.ResultStory})
	registerOf(outbox.KindUploadAttachment, handlerOf[outbox.UploadAttachment]{send: sends((*Pusher).pushUpload), find: findUpload})
	registerOf(outbox.KindDeleteAttachment, handlerOf[outbox.DeleteAttachment]{
		send: sends(func(s *Pusher, ctx context.Context, op outbox.Op, p outbox.DeleteAttachment) error {
			return s.pushDeleteItem(ctx, op, readmodel.ResultAttachment, p.Attachment, p.Path)
		}),
		local: func(p outbox.DeleteAttachment) string { return p.Attachment }, localResult: readmodel.ResultAttachment})
}

func (s *Pusher) pushComment(ctx context.Context, op outbox.Op, p outbox.AddComment) error {
	if localgid.IsLocal(op.TaskGID) {
		return &model.Error{Status: 404} // its create failed
	}
	st, err := s.API.AddComment(ctx, op.TaskGID, p.Text, p.HTMLText)
	if err != nil {
		return err
	}
	return sentNotSaved(s.Store.CompleteComment(ctx, op.ID, p.StoryGID, op.TaskGID, *st))
}

func (s *Pusher) pushUpload(ctx context.Context, op outbox.Op, p outbox.UploadAttachment) error {
	task, err := s.Store.RealGID(ctx, op.TaskGID)
	if err != nil {
		return err
	}
	if localgid.IsLocal(task) {
		return &model.Error{Status: 404} // its create failed
	}
	data, err := os.ReadFile(p.Path)
	if err != nil {
		return &model.Error{Status: 400} // the copied file is gone: cannot be retried
	}
	att, err := s.API.UploadAttachment(ctx, task, p.Name, data)
	if err != nil {
		return err
	}
	return sentNotSaved(s.Store.CompleteUpload(ctx, op.ID, p.Attachment, att.GID, att.ViewURL))
}

// pushUpdateStory edits a comment; sending it twice is harmless (PUT). A comment deleted
// in Asana answers 404: the edit is dropped (dropGone).
func (s *Pusher) pushUpdateStory(ctx context.Context, op outbox.Op, p outbox.UpdateStory) error {
	real, _, err := s.Store.LocalResult(ctx, readmodel.ResultStory, p.Story)
	if err != nil {
		return err
	}
	if real == "" {
		return &model.Error{Status: 404} // its add_comment was dropped
	}
	st, err := s.API.UpdateStory(ctx, real, p.Text, p.HTMLText)
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
// which is what was asked. gid is the item's gid (local until its create is recorded, as
// result), path the copy of a local attachment's file to remove once it is deleted ("": none).
func (s *Pusher) pushDeleteItem(ctx context.Context, op outbox.Op, result readmodel.ResultKind, gid, path string) error {
	real, _, err := s.Store.LocalResult(ctx, result, gid)
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
	if path != "" {
		_ = s.Store.RemoveAttachmentFile(path) // only inside the attachments folder
	}
	return nil
}

// findComment looks for the comment an add_comment posted (see reconcile).
func findComment(s *Pusher, ctx context.Context, op outbox.Op, p outbox.AddComment, f finder) (outcome, match, error) {
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
			sameComment(st, p.Text, p.HTMLText)
	})
	if m != matched {
		return sent, m, nil
	}
	return sent, matched, s.Store.CompleteComment(ctx, op.ID, p.StoryGID, op.TaskGID, st)
}

// findUpload looks for the attachment an upload_attachment added (see reconcile).
func findUpload(s *Pusher, ctx context.Context, op outbox.Op, p outbox.UploadAttachment, f finder) (outcome, match, error) {
	atts, err := s.API.Attachments(ctx, op.TaskGID)
	if err != nil {
		return sent, notFound, err
	}
	used, err := s.taken(ctx, readmodel.ResultAttachment)
	if err != nil {
		return sent, notFound, err
	}
	a, m := pick(atts, func(a model.Attachment) bool { return !used[a.GID] && a.Name == p.Name && f.recent(a.CreatedAt) })
	if m != matched {
		return sent, m, nil
	}
	return sent, matched, s.Store.CompleteUpload(ctx, op.ID, p.Attachment, a.GID, a.ViewURL)
}
