package sync

import (
	"context"

	"github.com/amasok/asagao/internal/asana/model"
	localgid "github.com/amasok/asagao/internal/domain/gid"
	"github.com/amasok/asagao/internal/domain/outbox"
)

// Sending changes to tags themselves (#33).

func init() {
	waits := func(s *Pusher, ctx context.Context, op outbox.Op) (bool, error) {
		return s.waitsForTag(ctx, op.TaskGID)
	}
	register(outbox.KindUpdateTag, opHandler{send: (*Pusher).pushUpdateTag, waits: waits,
		planWorkspace: func(s *Pusher, ctx context.Context, op outbox.Op) string {
			ws, _ := s.Store.TagWorkspace(ctx, op.TaskGID)
			return ws
		}})
	register(outbox.KindDeleteTag, opHandler{send: sends((*Pusher).pushDeleteTag), waits: waits, planWorkspace: deletedTagWorkspace})
}

// deletedTagWorkspace is the workspace of a tag being deleted. The tag is gone from the
// cache: its workspace is kept in the entry (or, for an entry queued before #68, the tasks
// it was on, in the order TagWorkspace reads them).
func deletedTagWorkspace(s *Pusher, ctx context.Context, op outbox.Op) string {
	if ws := payloadString(op, "workspace"); ws != "" {
		return ws
	}
	tasks, _ := op.Payload["tasks"].([]any)
	for _, t := range tasks {
		if g, _ := t.(string); g != "" {
			if ws, _ := s.Store.TaskWorkspace(ctx, g); ws != "" {
				return ws
			}
		}
	}
	return ""
}

// pushUpdateTag renames and/or recolors a tag. A value changed in Asana since the local
// edit (to something else) is a conflict.
func (s *Pusher) pushUpdateTag(ctx context.Context, op outbox.Op) (outcome, error) {
	gid, err := s.Store.RealTagGID(ctx, op.TaskGID)
	if err != nil {
		return sent, err
	}
	if localgid.IsLocal(gid) {
		return sent, &model.Error{Status: 404} // the tag was never created
	}
	server, err := s.API.Tag(ctx, gid)
	if err != nil {
		return sent, err
	}
	current := map[string]string{"name": server.Name, "color": server.Color}
	changes := map[string]any{}
	conflict := map[string]any{}
	for k, v := range op.Payload {
		want, _ := v.(string)
		cur, base := any(current[k]), op.Base[k]
		cmpWant := any(want)
		if k == "color" { // "none" ≡ "", and Asana renames the written names, like a project's
			cur, base, cmpWant = canonColor(cur), canonColor(base), canonColor(want)
			if cmpWant == "" {
				want = ""
			}
		}
		if !same(cur, base) && cur != cmpWant {
			conflict[k] = current[k]
		}
		if k == "color" && want == "" {
			changes[k] = nil // no color
		} else {
			changes[k] = want
		}
	}
	if len(conflict) > 0 {
		return conflicted, s.Store.MarkOpConflict(ctx, op.ID, conflict)
	}
	updated, err := s.API.UpdateTag(ctx, gid, changes)
	if err != nil {
		return sent, err
	}
	if err := s.Store.CompleteOp(ctx, op.ID); err != nil {
		return sent, sentNotSaved(err)
	}
	if updated.GID == "" {
		updated.GID = gid
	}
	if updated.Workspace == nil || updated.Workspace.GID == "" {
		updated.Workspace = server.Workspace
	}
	_ = s.Store.ApplyTag(ctx, *updated) // the next sync fixes a failure
	return sent, nil
}

// pushDeleteTag deletes a tag. One that is already gone (or was never created) is done.
func (s *Pusher) pushDeleteTag(ctx context.Context, op outbox.Op) error {
	gid, err := s.Store.RealTagGID(ctx, op.TaskGID)
	if err != nil {
		return err
	}
	if !localgid.IsLocal(gid) {
		if err := s.API.DeleteTag(ctx, gid); err != nil && !isNotFound(err) {
			return err
		}
	}
	return sentNotSaved(s.Store.CompleteOp(ctx, op.ID))
}

// waitsForTag reports whether a change to a tag created offline waits for the tag to be
// created (an add_tag that creates it is still queued).
func (s *Pusher) waitsForTag(ctx context.Context, tag string) (bool, error) {
	if !localgid.IsLocal(tag) {
		return false, nil
	}
	real, err := s.Store.RealTagGID(ctx, tag)
	if err != nil || !localgid.IsLocal(real) {
		return false, err
	}
	return s.Store.TagCreateQueued(ctx, tag)
}
