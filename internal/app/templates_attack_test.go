package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/domain/plan"
)

type fakeState map[string]string

func (f fakeState) PlanRefusedAt(_ context.Context, feature, workspaceGID string) (time.Time, bool, error) {
	v := f[feature+":"+workspaceGID]
	if v == "" {
		return time.Time{}, false, nil
	}
	at, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, false, nil
	}
	return at, true, nil
}

type errState struct{}

func (errState) PlanRefusedAt(context.Context, string, string) (time.Time, bool, error) {
	return time.Time{}, false, errors.New("db")
}

func TestAttackTemplatesOfferedBoundaries(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	key := plan.TaskTemplates + ":w1"
	cases := []struct {
		name string
		mark string
		want bool
	}{
		{"none", "", true},
		{"just now", now.Format(time.RFC3339), false},
		{"23h59m59s", now.Add(-24*time.Hour + time.Second).Format(time.RFC3339), false},
		{"exactly 24h", now.Add(-24 * time.Hour).Format(time.RFC3339), true},
		{"garbage", "yesterday", true},
		// The clock went back: a mark from the future must not hide it for more than
		// the recheck time.
		{"future within 4m", now.Add(4 * time.Minute).Format(time.RFC3339), true},
		{"future 6m", now.Add(6 * time.Minute).Format(time.RFC3339), true},
		{"future by 2 days", now.Add(48 * time.Hour).Format(time.RFC3339), true},
	}
	for _, c := range cases {
		if got := TemplatesOffered(context.Background(), fakeState{key: c.mark}, plan.TaskTemplates, "w1", now); got != c.want {
			t.Errorf("%s: offered = %v, want %v", c.name, got, c.want)
		}
	}
	if !TemplatesOffered(context.Background(), errState{}, plan.TaskTemplates, "w1", now) {
		t.Error("a store error hides the menu")
	}
	// The other feature's mark does not hide this one.
	other := fakeState{plan.ProjectTemplates + ":w1": now.Format(time.RFC3339)}
	if !TemplatesOffered(context.Background(), other, plan.TaskTemplates, "w1", now) {
		t.Error("project mark hid task templates")
	}
	// Another workspace's mark does not hide this one's.
	ws2 := fakeState{plan.TaskTemplates + ":w2": now.Format(time.RFC3339)}
	if !TemplatesOffered(context.Background(), ws2, plan.TaskTemplates, "w1", now) {
		t.Error("another workspace's mark hid it")
	}
}
