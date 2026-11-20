package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/domain/plan"
)

type syncStateMap map[string]string

func (m syncStateMap) PlanRefusedAt(_ context.Context, feature, workspaceGID string) (time.Time, bool, error) {
	if v, ok := m["err"]; ok {
		return time.Time{}, false, errors.New(v)
	}
	v := m[feature+":"+workspaceGID]
	if v == "" {
		return time.Time{}, false, nil
	}
	at, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, false, nil
	}
	return at, true, nil
}

func TestTemplatesOffered(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	key := plan.TaskTemplates + ":w1"
	for _, tc := range []struct {
		state map[string]string
		want  bool
	}{
		{map[string]string{}, true},
		{map[string]string{key: now.Add(-time.Hour).Format(time.RFC3339)}, false},
		{map[string]string{key: now.Add(-plan.RecheckAfter + time.Second).Format(time.RFC3339)}, false},
		{map[string]string{key: now.Add(-plan.RecheckAfter).Format(time.RFC3339)}, true},
		{map[string]string{key: now.Add(time.Minute).Format(time.RFC3339)}, true}, // from the future: tried again
		{map[string]string{key: now.Add(48 * time.Hour).Format(time.RFC3339)}, true},
		{map[string]string{key: "garbage"}, true},
		{map[string]string{plan.ProjectTemplates + ":w1": now.Format(time.RFC3339)}, true},
		{map[string]string{plan.TaskTemplates + ":w2": now.Format(time.RFC3339)}, true}, // another workspace
		{map[string]string{plan.TaskTemplates: now.Format(time.RFC3339)}, true},          // workspace-less
		{map[string]string{"err": "locked"}, true},
	} {
		if got := TemplatesOffered(ctx, syncStateMap(tc.state), plan.TaskTemplates, "w1", now); got != tc.want {
			t.Errorf("%v: offered = %v, want %v", tc.state, got, tc.want)
		}
	}
}
