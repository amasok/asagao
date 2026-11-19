package readmodel

import (
	"testing"
	"time"
)

func TestPlanOffForBoundaries(t *testing.T) {
	at := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	v := at.Format(time.RFC3339)
	jst := time.FixedZone("JST", 9*3600)
	cases := []struct {
		name  string
		value string
		now   time.Time
		want  bool
	}{
		{"same instant", v, at, true},
		{"1ns before the day", v, at.Add(PlanRecheckAfter - time.Nanosecond), true},
		{"exactly the day", v, at.Add(PlanRecheckAfter), false},
		{"after the day", v, at.Add(PlanRecheckAfter + time.Second), false},
		{"clock behind by 1s", v, at.Add(-time.Second), false},
		{"clock far behind", v, at.Add(-30 * 24 * time.Hour), false},
		{"empty", "", at, false},
		{"garbage", "yes", at, false},
		{"date only", "2026-11-17", at, false},
		{"other zone, same instant", at.In(jst).Format(time.RFC3339), at.Add(time.Hour), true},
		{"now in another zone", v, at.Add(time.Hour).In(jst), true},
	}
	for _, c := range cases {
		if got := PlanOffFor(c.value, c.now); got != c.want {
			t.Errorf("%s: PlanOffFor = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestPlanKeyDistinct(t *testing.T) {
	if PlanKey(PlanTags, "w1") == PlanKey(PlanTags, "w2") || PlanKey(PlanTags, "w1") == PlanKey(PlanSections, "w1") {
		t.Fatal("keys collide")
	}
	if PlanKey(PlanTags, "w1") == KeyPlanUnavailable+PlanTags {
		t.Fatal("new key equals the old global one")
	}
}
