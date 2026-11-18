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

// StartDateOffered reports whether a start date can be set on task gid at now: unless a
// recent 402 hides start dates in its workspace (readmodel.PlanStartOn). A task whose
// workspace cannot be read is offered them (the store checks again when one is set).
func StartDateOffered(ctx context.Context, st interface {
	SyncState(ctx context.Context, key string) (string, error)
	TaskWorkspace(ctx context.Context, taskGID string) (string, error)
}, gid string, now time.Time) bool {
	ws, err := st.TaskWorkspace(ctx, gid)
	if err != nil {
		return true
	}
	return !IsPlanUnavailable(planOffered(ctx, st, readmodel.PlanStartOn, ws, now))
}
