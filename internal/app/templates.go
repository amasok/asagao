package app

import (
	"context"
	"time"

	"github.com/amasok/asagao/internal/readmodel"
)

// PlanRecheck is readmodel.PlanRecheck: how long after a 402 a feature is offered again.
const PlanRecheck = readmodel.PlanRecheck

// PlanOffered reports whether feature (one with a per-workspace mark, such as
// readmodel.PlanTaskTemplates) is offered in workspace workspaceGID at now: unless Asana
// answered 402 for it there less than PlanRecheck before (readmodel.PlanValueOffered). A
// mark that cannot be read does not hide it (Asana says again if it is unavailable).
func PlanOffered(ctx context.Context, st interface {
	SyncState(ctx context.Context, key string) (string, error)
}, feature, workspaceGID string, now time.Time) bool {
	v, err := st.SyncState(ctx, readmodel.PlanKey(feature, workspaceGID))
	if err != nil {
		return true
	}
	return readmodel.PlanValueOffered(v, now)
}
