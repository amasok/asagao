package app

import (
	"context"
	"time"
)

// TemplatesOffered reports whether making tasks or projects from templates (feature is
// plan.TaskTemplates or plan.ProjectTemplates) is offered in workspace
// workspaceGID at now: unless a recent 402 hides it there (planOffered). A record that
// cannot be read does not hide it (Asana says again if it is unavailable).
func TemplatesOffered(ctx context.Context, st interface {
	PlanRefusedAt(ctx context.Context, feature, workspaceGID string) (time.Time, bool, error)
}, feature, workspaceGID string, now time.Time) bool {
	return !IsPlanUnavailable(planOffered(ctx, st, feature, workspaceGID, now))
}
