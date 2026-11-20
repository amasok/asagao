package app

import (
	"context"
	"time"

	"github.com/amasok/asagao/internal/domain/plan"
)

// A 402 (the workspace's plan lacks a feature) hides the feature in that workspace for
// plan.RecheckAfter. The records of sections, tags and My Tasks' sections before #50
// (workspace-less) are not read any more: such a feature is offered again and, if the
// plan still lacks it, Asana's next 402 records it for its workspace.

// planOffered returns ErrPlanUnavailable while a recent 402 hides feature in a workspace,
// and nil otherwise.
func planOffered(ctx context.Context, st interface {
	PlanRefusedAt(ctx context.Context, feature, workspaceGID string) (time.Time, bool, error)
}, feature, workspaceGID string, now time.Time) error {
	at, ok, err := st.PlanRefusedAt(ctx, feature, workspaceGID)
	if err != nil {
		return err
	}
	if ok && plan.OffAt(at, now) {
		return &ErrPlanUnavailable{Feature: feature}
	}
	return nil
}

// planInStore is what planIn needs of the local cache.
type planInStore interface {
	PlanRefusedAt(ctx context.Context, feature, workspaceGID string) (time.Time, bool, error)
	DefaultWorkspace(ctx context.Context) (string, error)
}

// planIn is planOffered for the workspace ws of what is changed, as a lookup of the store
// returned it (wsErr is returned as it is). A workspace that is not known ("") is the
// default one, as the sync engine records it (sync.planWorkspace).
func planIn(ctx context.Context, st planInStore, feature, ws string, wsErr error, now time.Time) error {
	if wsErr != nil {
		return wsErr
	}
	if ws == "" {
		ws, _ = st.DefaultWorkspace(ctx) // none: nothing was recorded for one either
	}
	return planOffered(ctx, st, feature, ws, now)
}

// planAnywhere reports whether feature can be offered where no workspace is chosen yet (a
// menu, a tool list): unless a recent 402 hides it in every workspace of the cache. A
// cache that knows no workspace (or cannot say) offers it.
func planAnywhere(ctx context.Context, st interface {
	PlanRefusedAt(ctx context.Context, feature, workspaceGID string) (time.Time, bool, error)
	WorkspaceGIDs(ctx context.Context) ([]string, error)
}, feature string, now time.Time) bool {
	wss, err := st.WorkspaceGIDs(ctx)
	if err != nil || len(wss) == 0 {
		return true // not known yet: try
	}
	for _, ws := range wss {
		if planOffered(ctx, st, feature, ws, now) == nil {
			return true
		}
	}
	return false
}

// planNow is a use case's clock, or the time now for one made without (a menu check).
func planNow(c Clock) time.Time {
	if c == nil {
		return time.Now()
	}
	return c()
}

// StartDateOffered reports whether a start date can be set on task gid at now: unless a
// recent 402 hides start dates in its workspace (plan.StartOn). A task whose
// workspace cannot be read is offered them (the store checks again when one is set).
func StartDateOffered(ctx context.Context, st interface {
	PlanRefusedAt(ctx context.Context, feature, workspaceGID string) (time.Time, bool, error)
	TaskWorkspace(ctx context.Context, taskGID string) (string, error)
}, gid string, now time.Time) bool {
	ws, err := st.TaskWorkspace(ctx, gid)
	if err != nil {
		return true
	}
	return !IsPlanUnavailable(planOffered(ctx, st, plan.StartOn, ws, now))
}
