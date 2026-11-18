package readmodel

import (
	"math"
	"strconv"
	"strings"
	"testing"
)

func f64(v float64) *float64 { return &v }

// 29 of 100 is 29%, not 28% (29/100*100 is 28.999999999999996 in floats).
func TestAttackMetricTextDoesNotLoseAPercent(t *testing.T) {
	for _, cur := range []float64{29, 57, 58, 14, 7} {
		m := &GoalMetric{Initial: f64(0), Target: f64(100), Current: f64(cur)}
		want := "（" + strconv.Itoa(int(cur)) + "%）"
		if got := m.Text(); !strings.HasSuffix(got, want) {
			t.Errorf("current %v: %q, want suffix %q", cur, got, want)
		}
	}
}

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

func TestAttackMetricTextOddInputs(t *testing.T) {
	for _, m := range []*GoalMetric{
		{Precision: -3, Current: f64(1.5), Target: f64(3)},
		{Precision: 1 << 30, Current: f64(1), Target: f64(3)},
		{Kind: "currency", Current: f64(1), Target: f64(0)},
		{Kind: "percentage", Target: f64(math.NaN()), Current: f64(math.NaN())},
		{},
		nil,
	} {
		_ = m.Text() // must not panic
	}
	if got := (&GoalMetric{Kind: "currency", Currency: "JPY", Current: f64(5), Target: f64(10)}).Text(); got != "JPY 5 / JPY 10（50%）" {
		t.Errorf("currency text = %q", got)
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
