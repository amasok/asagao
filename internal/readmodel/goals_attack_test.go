package readmodel

import (
	"math"
	"testing"
)

func f64(v float64) *float64 { return &v }

func TestAttackMetricProgressEdges(t *testing.T) {
	cases := []struct {
		name string
		m    *GoalMetric
		p    float64
		ok   bool
	}{
		{"decreasing goal", &GoalMetric{Initial: f64(100), Target: f64(0), Current: f64(25)}, 0.75, true},
		{"overshoot", &GoalMetric{Initial: f64(0), Target: f64(10), Current: f64(50)}, 1, true},
		{"wrong direction", &GoalMetric{Initial: f64(0), Target: f64(10), Current: f64(-5)}, 0, true},
		{"target == initial", &GoalMetric{Initial: f64(5), Target: f64(5), Current: f64(5)}, 0, false},
		{"inf target", &GoalMetric{Target: f64(math.Inf(1)), Current: f64(1)}, 0, false},
		{"inf current", &GoalMetric{Target: f64(10), Current: f64(math.Inf(1))}, 1, true},
		{"nan current", &GoalMetric{Target: f64(10), Current: f64(math.NaN())}, 0, false},
		{"no current", &GoalMetric{Target: f64(10)}, 0, false},
		{"nil", nil, 0, false},
	}
	for _, c := range cases {
		p, ok := c.m.Progress()
		if p != c.p || ok != c.ok {
			t.Errorf("%s: (%v, %v), want (%v, %v)", c.name, p, ok, c.p, c.ok)
		}
	}
}

func TestAttackContainsBoundaries(t *testing.T) {
	p := TimePeriodView{StartOn: "2026-10-01", EndOn: "2026-12-31"}
	for day, want := range map[string]bool{"2026-09-30": false, "2026-10-01": true, "2026-12-31": true, "2027-01-01": false, "": false} {
		if p.Contains(day) != want {
			t.Errorf("%q: %v", day, !want)
		}
	}
	if (TimePeriodView{StartOn: "2026-10-01"}).Contains("2026-11-01") {
		t.Error("an open-ended period contains a day")
	}
}
