package sync

import (
	"context"

	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/domain/plan"
)

// A 402 (the workspace's plan lacks a feature) is recorded per workspace and hides the
// feature for plan.RecheckAfter. While it does, the feature is not asked for; after it,
// it is tried again, and an answer forgets the 402.

// planHidden reports whether a recent 402 hides feature in a workspace.
func (s *Syncer) planHidden(ctx context.Context, feature, workspaceGID string) (bool, error) {
	at, ok, err := s.Store.PlanRefusedAt(ctx, feature, workspaceGID)
	if err != nil || !ok {
		return false, err
	}
	return plan.OffAt(at, s.clock()), nil
}

// recordPlanRefusal records a 402 for feature in a workspace when err is one, and says
// whether it was.
func (s *Syncer) recordPlanRefusal(ctx context.Context, feature, workspaceGID string, err error) (bool, error) {
	if !model.IsPaymentRequired(err) {
		return false, nil
	}
	return true, s.Store.RecordPlanRefusal(ctx, feature, workspaceGID, s.clock())
}

// clearPlanRefusal forgets an earlier 402 for feature in a workspace (Asana answered).
func (s *Syncer) clearPlanRefusal(ctx context.Context, feature, workspaceGID string) error {
	return s.Store.ClearPlanRefusal(ctx, feature, workspaceGID)
}

// planWorkspace returns the workspace a 402 to entry op is recorded for, as the app reads
// it before queuing one (app.planIn): the one of the project of a section, of the task of
// a move in My Tasks, or of a task carrying a tag; the default workspace when that is not
// known.
func (s *Syncer) planWorkspace(ctx context.Context, op outbox.Op) string {
	str := func(k string) string { v, _ := op.Payload[k].(string); return v }
	var ws string
	switch op.Kind {
	case outbox.KindCreateSection, outbox.KindDeleteSection, outbox.KindInsertSection:
		ws, _ = s.Store.ProjectWorkspace(ctx, str("project"))
	case outbox.KindMoveMySection:
		if ws = str("workspace"); ws == "" {
			ws, _ = s.Store.TaskWorkspace(ctx, op.TaskGID)
		}
	case outbox.KindUpdateTag:
		ws, _ = s.Store.TagWorkspace(ctx, op.TaskGID)
	case outbox.KindDeleteTag:
		// The tag is gone from the cache: its workspace is kept in the entry (or, for an
		// entry queued before #68, the tasks it was on, in the order TagWorkspace reads them).
		if ws = str("workspace"); ws == "" {
			tasks, _ := op.Payload["tasks"].([]any)
			for _, t := range tasks {
				if g, _ := t.(string); g != "" {
					if ws, _ = s.Store.TaskWorkspace(ctx, g); ws != "" {
						break
					}
				}
			}
		}
	}
	if ws == "" {
		ws, _ = s.Store.DefaultWorkspace(ctx)
	}
	return ws
}
