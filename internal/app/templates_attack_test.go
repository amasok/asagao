package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/readmodel"
)

type fakeState map[string]string

func (f fakeState) SyncState(_ context.Context, k string) (string, error) {
	if k == "ERR" {
		return "", errors.New("db")
	}
	return f[k], nil
}

type errState struct{}

func (errState) SyncState(context.Context, string) (string, error) { return "", errors.New("db") }

func TestAttackTemplatesOfferedBoundaries(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	key := readmodel.PlanKey(readmodel.PlanTaskTemplates, "w1")
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
		{"future within skew (4m)", now.Add(4 * time.Minute).Format(time.RFC3339), false},
		{"future beyond skew (6m)", now.Add(6 * time.Minute).Format(time.RFC3339), true},
		{"future by 2 days", now.Add(48 * time.Hour).Format(time.RFC3339), true},
	}
	for _, c := range cases {
		if got := PlanOffered(context.Background(), fakeState{key: c.mark}, readmodel.PlanTaskTemplates, "w1", now); got != c.want {
			t.Errorf("%s: offered = %v, want %v", c.name, got, c.want)
		}
	}
	if !PlanOffered(context.Background(), errState{}, readmodel.PlanTaskTemplates, "w1", now) {
		t.Error("a store error hides the menu")
	}
	// The other feature's mark does not hide this one.
	other := fakeState{readmodel.PlanKey(readmodel.PlanProjectTemplates, "w1"): now.Format(time.RFC3339)}
	if !PlanOffered(context.Background(), other, readmodel.PlanTaskTemplates, "w1", now) {
		t.Error("project mark hid task templates")
	}
	// Another workspace's mark does not hide this one's.
	ws2 := fakeState{readmodel.PlanKey(readmodel.PlanTaskTemplates, "w2"): now.Format(time.RFC3339)}
	if !PlanOffered(context.Background(), ws2, readmodel.PlanTaskTemplates, "w1", now) {
		t.Error("another workspace's mark hid it")
	}
}
