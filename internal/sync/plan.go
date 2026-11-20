package sync

import (
	"context"
	"time"

	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/domain/plan"
)

// A 402 (the workspace's plan lacks a feature) is recorded per workspace and hides the
// feature for plan.RecheckAfter. While it does, the feature is not asked for; after it,
// it is tried again, and an answer forgets the 402.

// PlanStore is where the 402s are recorded (and forgotten once Asana answers).
type PlanStore interface {
	PlanRefusedAt(ctx context.Context, feature, workspaceGID string) (time.Time, bool, error)
	RecordPlanRefusal(ctx context.Context, feature, workspaceGID string, at time.Time) error
	ClearPlanRefusal(ctx context.Context, feature, workspaceGID string) error
}

// planHidden reports whether a recent 402 (as of now) hides feature in a workspace.
func planHidden(ctx context.Context, st PlanStore, now time.Time, feature, workspaceGID string) (bool, error) {
	at, ok, err := st.PlanRefusedAt(ctx, feature, workspaceGID)
	if err != nil || !ok {
		return false, err
	}
	return plan.OffAt(at, now), nil
}

// recordPlanRefusal records a 402 for feature in a workspace (at now) when err is one, and
// says whether it was.
func recordPlanRefusal(ctx context.Context, st PlanStore, now time.Time, feature, workspaceGID string, err error) (bool, error) {
	if !model.IsPaymentRequired(err) {
		return false, nil
	}
	return true, st.RecordPlanRefusal(ctx, feature, workspaceGID, now)
}

// planWorkspace returns the workspace a 402 to entry op is recorded for, as the app reads
// it before queuing one (app.planIn): the one of the project of a section, of the task of
// a move in My Tasks, or of a task carrying a tag; the default workspace when that is not
// known.
func (s *Pusher) planWorkspace(ctx context.Context, op outbox.Op) string {
	var ws string
	if f := handlers[op.Kind].planWorkspace; f != nil {
		ws = f(s, ctx, op)
	}
	if ws == "" {
		ws, _ = s.Store.DefaultWorkspace(ctx)
	}
	return ws
}
