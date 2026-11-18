package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/readmodel"
)

type syncStateMap map[string]string

func (m syncStateMap) SyncState(_ context.Context, key string) (string, error) {
	if v, ok := m["err"]; ok {
		return "", errors.New(v)
	}
	return m[key], nil
}

func TestPlanOffered(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	key := readmodel.PlanKey(readmodel.PlanTaskTemplates, "w1")
	for _, tc := range []struct {
		state map[string]string
		want  bool
	}{
		{map[string]string{}, true},
		{map[string]string{key: now.Add(-time.Hour).Format(time.RFC3339)}, false},
		{map[string]string{key: now.Add(-PlanRecheck + time.Second).Format(time.RFC3339)}, false},
		{map[string]string{key: now.Add(-PlanRecheck).Format(time.RFC3339)}, true},
		{map[string]string{key: now.Add(time.Minute).Format(time.RFC3339)}, false},
		{map[string]string{key: now.Add(readmodel.PlanSkew + time.Second).Format(time.RFC3339)}, true},
		{map[string]string{key: now.Add(48 * time.Hour).Format(time.RFC3339)}, true},
		{map[string]string{key: "garbage"}, true},
		{map[string]string{readmodel.PlanKey(readmodel.PlanProjectTemplates, "w1"): now.Format(time.RFC3339)}, true},
		{map[string]string{readmodel.PlanKey(readmodel.PlanTaskTemplates, "w2"): now.Format(time.RFC3339)}, true},       // another workspace
		{map[string]string{readmodel.KeyPlanUnavailable + readmodel.PlanTaskTemplates: now.Format(time.RFC3339)}, true}, // the old, workspace-less key
		{map[string]string{"err": "locked"}, true},
	} {
		if got := PlanOffered(ctx, syncStateMap(tc.state), readmodel.PlanTaskTemplates, "w1", now); got != tc.want {
			t.Errorf("%v: offered = %v, want %v", tc.state, got, tc.want)
		}
	}
}
