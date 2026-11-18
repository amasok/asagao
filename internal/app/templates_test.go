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

func TestTemplatesOffered(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	key := readmodel.KeyPlanUnavailable + readmodel.PlanTaskTemplates
	for _, tc := range []struct {
		state map[string]string
		want  bool
	}{
		{map[string]string{}, true},
		{map[string]string{key: now.Add(-time.Hour).Format(time.RFC3339)}, false},
		{map[string]string{key: now.Add(-TemplateRecheck + time.Second).Format(time.RFC3339)}, false},
		{map[string]string{key: now.Add(-TemplateRecheck).Format(time.RFC3339)}, true},
		{map[string]string{key: now.Add(time.Minute).Format(time.RFC3339)}, false},
		{map[string]string{key: "garbage"}, true},
		{map[string]string{readmodel.KeyPlanUnavailable + readmodel.PlanProjectTemplates: now.Format(time.RFC3339)}, true},
		{map[string]string{"err": "locked"}, true},
	} {
		if got := TemplatesOffered(ctx, syncStateMap(tc.state), readmodel.PlanTaskTemplates, now); got != tc.want {
			t.Errorf("%v: offered = %v, want %v", tc.state, got, tc.want)
		}
	}
}
