package readmodel

import (
	"fmt"
	"math"
	"testing"
)

// A percentage is a fraction: every whole percent k/100 is shown as k (no 28.999… floor
// error), as the text and as the progress.
func TestAttackPercentageFractionsAreExact(t *testing.T) {
	for k := 0; k <= 100; k++ {
		m := &GoalMetric{Kind: "percentage", Initial: f(0), Target: f(1), Current: f(float64(k) / 100)}
		if want := fmt.Sprintf("%d%% / 100%%（%d%%）", k, k); m.Text() != want {
			t.Errorf("k=%d: %q, want %q", k, m.Text(), want)
		}
	}
}

// Going down (initial 1 → target 0.5), no span, over / under the range, and the other
// kinds that must not be multiplied.
func TestAttackPercentageShapes(t *testing.T) {
	for _, tc := range []struct {
		name string
		m    *GoalMetric
		want string
	}{
		{"down", &GoalMetric{Kind: "percentage", Initial: f(1), Target: f(0.5), Current: f(0.75)}, "75% / 50%（50%）"},
		{"no span", &GoalMetric{Kind: "percentage", Initial: f(0.5), Target: f(0.5), Current: f(0.5)}, "50% / 50%"},
		{"under", &GoalMetric{Kind: "percentage", Initial: f(0), Target: f(1), Current: f(-0.2)}, "-20% / 100%（0%）"},
		{"over", &GoalMetric{Kind: "percentage", Initial: f(0), Target: f(1), Current: f(3)}, "300% / 100%（100%）"},
		{"no initial", &GoalMetric{Kind: "percentage", Target: f(1), Current: f(0.5)}, "50% / 100%（50%）"},
		{"no target", &GoalMetric{Kind: "percentage", Current: f(0.5)}, "50%"},
		{"nothing", &GoalMetric{Kind: "percentage"}, ""},
		{"precision", &GoalMetric{Kind: "percentage", Precision: 1, Initial: f(0), Target: f(1), Current: f(0.123)}, "12.3% / 100.0%（12%）"},
		{"display wins for current", &GoalMetric{Kind: "percentage", Initial: f(0), Target: f(1), Current: f(0.4), Display: "40%"}, "40% / 100%（40%）"},
		{"currency not multiplied", &GoalMetric{Kind: "currency", Currency: "USD", Target: f(1), Current: f(0.5), Precision: 2}, "USD 0.50 / USD 1.00（50%）"},
		{"number not multiplied", &GoalMetric{Kind: "number", Target: f(1), Current: f(0.5), Precision: 1}, "0.5 / 1.0（50%）"},
		{"unknown kind is a number", &GoalMetric{Kind: "weird", Target: f(1), Current: f(0.5), Precision: 1}, "0.5 / 1.0（50%）"},
		{"empty kind is a number", &GoalMetric{Target: f(2), Current: f(1)}, "1 / 2（50%）"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.m.Text(); got != tc.want {
				t.Errorf("%q, want %q", got, tc.want)
			}
		})
	}
}

// Odd floats do not panic and are not shown as a number that is not one.
func TestAttackPercentageOddFloats(t *testing.T) {
	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), 1e308, -1e308, math.SmallestNonzeroFloat64} {
		m := &GoalMetric{Kind: "percentage", Initial: f(0), Target: f(v), Current: f(v)}
		_ = m.Text()
		m = &GoalMetric{Kind: "percentage", Initial: f(v), Target: f(1), Current: f(0.5), Precision: -3}
		_ = m.Text()
		if p, ok := m.Progress(); ok && (p < 0 || p > 1 || math.IsNaN(p)) {
			t.Errorf("progress %v for %v", p, v)
		}
	}
}
