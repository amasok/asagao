package sync

import (
	"context"
	"time"

	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/readmodel"
)

// A 402 (the workspace's plan lacks a feature) is recorded per workspace and hides the
// feature for readmodel.PlanRecheckAfter (readmodel.PlanKey). While it does, the feature
// is not asked for; after it, it is tried again, and an answer forgets the 402.

// planHidden reports whether a recent 402 hides feature in a workspace.
func (s *Syncer) planHidden(ctx context.Context, feature, workspaceGID string) (bool, error) {
	v, err := s.Store.SyncState(ctx, readmodel.PlanKey(feature, workspaceGID))
	if err != nil {
		return false, err
	}
	return readmodel.PlanOffFor(v, s.clock()), nil
}

// recordPlanRefusal records a 402 for feature in a workspace when err is one, and says
// whether it was.
func (s *Syncer) recordPlanRefusal(ctx context.Context, feature, workspaceGID string, err error) (bool, error) {
	if !model.IsPaymentRequired(err) {
		return false, nil
	}
	return true, s.Store.SetSyncState(ctx, readmodel.PlanKey(feature, workspaceGID), s.clock().UTC().Format(time.RFC3339))
}

// clearPlanRefusal forgets an earlier 402 for feature in a workspace (Asana answered).
func (s *Syncer) clearPlanRefusal(ctx context.Context, feature, workspaceGID string) error {
	key := readmodel.PlanKey(feature, workspaceGID)
	v, err := s.Store.SyncState(ctx, key)
	if err != nil || v == "" {
		return err
	}
	return s.Store.SetSyncState(ctx, key, "")
}
