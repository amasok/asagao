package readmodel

import (
	"math"
	"testing"
	"time"
)

func f(v float64) *float64 { return &v }

func TestGoalMetricProgress(t *testing.T) {
	for _, tc := range []struct {
		m    *GoalMetric
		want string
	}{
		{nil, ""},
		{&GoalMetric{Kind: "number", Initial: f(0), Target: f(100), Current: f(40), Display: "40"}, "40 / 100（40%）"},
		{&GoalMetric{Kind: "number", Target: f(100), Current: f(40)}, "40 / 100（40%）"},
		{&GoalMetric{Kind: "percentage", Initial: f(0), Target: f(1), Current: f(1.5)}, "150% / 100%（100%）"},
		// a new goal's default metric on the real Asana (#31): percentage 0 → 1
		{&GoalMetric{Kind: "percentage", Initial: f(0), Target: f(1), Current: f(0.4)}, "40% / 100%（40%）"},
		{&GoalMetric{Kind: "percentage", Initial: f(0), Target: f(1), Current: f(0), Display: "0%"}, "0% / 100%（0%）"},
		{&GoalMetric{Kind: "number", Initial: f(100), Target: f(50), Current: f(75)}, "75 / 50（50%）"}, // going down
		{&GoalMetric{Kind: "number", Initial: f(10), Target: f(10), Current: f(10)}, "10 / 10"},       // no span
		{&GoalMetric{Kind: "currency", Currency: "JPY", Target: f(1000), Current: f(-5)}, "JPY -5 / JPY 1000（0%）"},
		{&GoalMetric{Kind: "number", Precision: 2, Target: f(1), Current: f(0.333)}, "0.33 / 1.00（33%）"},
		{&GoalMetric{Kind: "number", Target: f(math.Inf(1)), Current: f(1)}, "1 / +Inf"},
		{&GoalMetric{Kind: "number", Current: f(3)}, "3"},
		{&GoalMetric{Kind: "number", Precision: 1000, Target: f(1), Current: f(1)}, "1.000000 / 1.000000（100%）"},
	} {
		if got := tc.m.Text(); got != tc.want {
			t.Errorf("%+v: %q, want %q", tc.m, got, tc.want)
		}
	}
}

func TestPlanOffFor(t *testing.T) {
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	for v, want := range map[string]bool{
		"": false, "x": false,
		now.Format(time.RFC3339): true,
		now.Add(-PlanRecheckAfter + time.Second).Format(time.RFC3339): true,
		now.Add(-PlanRecheckAfter).Format(time.RFC3339):               false,
		now.Add(time.Minute).Format(time.RFC3339):                     false,
	} {
		if got := PlanOffFor(v, now); got != want {
			t.Errorf("%q: %v, want %v", v, got, want)
		}
	}
}

// Every whole percent of 100 is shown as itself (29/100*100 is 28.999… in floating point).
func TestGoalMetricWholePercents(t *testing.T) {
	for i := 0; i <= 100; i++ {
		for _, span := range []float64{100, 1000} {
			cur := float64(i) * span / 100
			m := &GoalMetric{Kind: "number", Initial: f(0), Target: f(span), Current: f(cur)}
			if pct, ok := m.percent(); !ok || pct != i {
				t.Errorf("%v of %v: %d%%", cur, span, pct)
			}
		}
	}
}
