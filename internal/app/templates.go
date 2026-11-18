package app

import (
	"context"
	"time"

	"github.com/amasok/asagao/internal/readmodel"
)

// TemplateRecheck is how long after Asana refused templates (402) they are offered
// again: a trial or an upgrade may give them, a trial's end may take them away.
const TemplateRecheck = 24 * time.Hour

// TemplatesOffered reports whether making tasks or projects from templates (feature is
// readmodel.PlanTaskTemplates or PlanProjectTemplates) is offered: unless Asana answered
// 402 for it less than TemplateRecheck before now (by this machine's clock). A mark that cannot be read or parsed
// does not hide it (Asana says again if it is unavailable).
func TemplatesOffered(ctx context.Context, st interface {
	SyncState(ctx context.Context, key string) (string, error)
}, feature string, now time.Time) bool {
	v, err := st.SyncState(ctx, readmodel.KeyPlanUnavailable+feature)
	if err != nil || v == "" {
		return true
	}
	at, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return true
	}
	d := now.Sub(at)
	// A mark more than templateSkew in the future: the clock went back, and the mark would
	// hide templates for longer than TemplateRecheck. It is ignored.
	return d >= TemplateRecheck || d < -templateSkew
}

// templateSkew is how far in the future a mark may be (another process's clock, a
// second's rounding) and still count.
const templateSkew = 5 * time.Minute
