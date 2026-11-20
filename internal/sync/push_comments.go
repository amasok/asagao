package sync

import (
	"context"
	"github.com/amasok/asagao/internal/asana/model"
	localgid "github.com/amasok/asagao/internal/domain/gid"
	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/readmodel"
	"os"
)

// Sending comments and attachments: adding, editing and deleting them.

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

// storyTarget returns, for an edit or a delete of a comment, attachment or time entry, the payload key
// of its gid and the ResultKind under which a local one's result is recorded ("" otherwise).
func storyTarget(kind outbox.Kind) (key string, result readmodel.ResultKind) {
	switch kind {
	case outbox.KindUpdateStory, outbox.KindDeleteStory:
		return "story", readmodel.ResultStory
	case outbox.KindDeleteAttachment:
		return "attachment", readmodel.ResultAttachment
	case outbox.KindUpdateTimeEntry, outbox.KindDeleteTimeEntry:
		return "entry", readmodel.ResultTimeEntry
	}
	return "", ""
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
	key, prefix := storyTarget(op.Kind)
	gid, _ := op.Payload[key].(string)
	real, _, err := s.Store.LocalResult(ctx, prefix, gid)
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
