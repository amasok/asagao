package app

import (
	"context"
	"time"

	"github.com/amasok/asagao/internal/readmodel"
)

// planOffered returns ErrPlanUnavailable while a recent 402 hides feature in a workspace
// (readmodel.PlanKey), and nil otherwise.
func planOffered(ctx context.Context, st interface {
	SyncState(ctx context.Context, key string) (string, error)
}, feature, workspaceGID string, now time.Time) error {
	v, err := st.SyncState(ctx, readmodel.PlanKey(feature, workspaceGID))
	if err != nil {
		return err
	}
	if readmodel.PlanOffFor(v, now) {
		return &ErrPlanUnavailable{Feature: feature}
	}
	return nil
}
