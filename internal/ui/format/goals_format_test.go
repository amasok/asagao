package format

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/readmodel"
)

func f64(v float64) *float64 { return &v }

func TestGoalMetricProgress(t *testing.T) {
	for _, tc := range []struct {
		m    *readmodel.GoalMetric
		want string
	}{
		{nil, ""},
		{&readmodel.GoalMetric{Kind: "number", Initial: f64(0), Target: f64(100), Current: f64(40), Display: "40"}, "40 / 100（40%）"},
		{&readmodel.GoalMetric{Kind: "number", Target: f64(100), Current: f64(40)}, "40 / 100（40%）"},
		{&readmodel.GoalMetric{Kind: "percentage", Initial: f64(0), Target: f64(1), Current: f64(1.5)}, "150% / 100%（100%）"},
		// a new goal's default metric on the real Asana (#31): percentage 0 → 1
		{&readmodel.GoalMetric{Kind: "percentage", Initial: f64(0), Target: f64(1), Current: f64(0.4)}, "40% / 100%（40%）"},
		{&readmodel.GoalMetric{Kind: "percentage", Initial: f64(0), Target: f64(1), Current: f64(0), Display: "0%"}, "0% / 100%（0%）"},
		{&readmodel.GoalMetric{Kind: "percentage", Initial: f64(0), Target: f64(1), Current: f64(0), Display: "0.00"}, "0% / 100%（0%）"}, // POST's answer
		{&readmodel.GoalMetric{Kind: "percentage", Target: f64(1), Display: "40%"}, "40% / 100%"},                                  // no value: the display
		{&readmodel.GoalMetric{Kind: "number", Initial: f64(100), Target: f64(50), Current: f64(75)}, "75 / 50（50%）"},             // going down
		{&readmodel.GoalMetric{Kind: "number", Initial: f64(10), Target: f64(10), Current: f64(10)}, "10 / 10"},                   // no span
		{&readmodel.GoalMetric{Kind: "currency", Currency: "JPY", Target: f64(1000), Current: f64(-5)}, "JPY -5 / JPY 1000（0%）"},
		{&readmodel.GoalMetric{Kind: "number", Precision: 2, Target: f64(1), Current: f64(0.333)}, "0.33 / 1.00（33%）"},
		{&readmodel.GoalMetric{Kind: "number", Target: f64(math.Inf(1)), Current: f64(1)}, "1 / +Inf"},
		{&readmodel.GoalMetric{Kind: "number", Current: f64(3)}, "3"},
		{&readmodel.GoalMetric{Kind: "number", Precision: 1000, Target: f64(1), Current: f64(1)}, "1.000000 / 1.000000（100%）"},
	} {
		if got := GoalMetricText(tc.m); got != tc.want {
			t.Errorf("%+v: %q, want %q", tc.m, got, tc.want)
		}
	}
}

// Every whole percent of 100 is shown as itself (29/100*100 is 28.999… in floating point).
func TestGoalMetricWholePercents(t *testing.T) {
	for i := 0; i <= 100; i++ {
		for _, span := range []float64{100, 1000} {
			cur := float64(i) * span / 100
			m := &readmodel.GoalMetric{Kind: "number", Initial: f64(0), Target: f64(span), Current: f64(cur)}
			if pct, ok := goalMetricPercent(m); !ok || pct != i {
				t.Errorf("%v of %v: %d%%", cur, span, pct)
			}
		}
	}
}

// 29 of 100 is 29%, not 28% (29/100*100 is 28.999999999999996 in floats).
func TestAttackMetricTextDoesNotLoseAPercent(t *testing.T) {
	for _, cur := range []float64{29, 57, 58, 14, 7} {
		m := &readmodel.GoalMetric{Initial: f64(0), Target: f64(100), Current: f64(cur)}
		want := "（" + strconv.Itoa(int(cur)) + "%）"
		if got := GoalMetricText(m); !strings.HasSuffix(got, want) {
			t.Errorf("current %v: %q, want suffix %q", cur, got, want)
		}
	}
}

func TestAttackMetricTextOddInputs(t *testing.T) {
	for _, m := range []*readmodel.GoalMetric{
		{Precision: -3, Current: f64(1.5), Target: f64(3)},
		{Precision: 1 << 30, Current: f64(1), Target: f64(3)},
		{Kind: "currency", Current: f64(1), Target: f64(0)},
		{Kind: "percentage", Target: f64(math.NaN()), Current: f64(math.NaN())},
		{},
		nil,
	} {
		_ = GoalMetricText(m) // must not panic
	}
	if got := GoalMetricText(&readmodel.GoalMetric{Kind: "currency", Currency: "JPY", Current: f64(5), Target: f64(10)}); got != "JPY 5 / JPY 10（50%）" {
		t.Errorf("currency text = %q", got)
	}
}

// A percentage is a fraction: every whole percent k/100 is shown as k (no 28.999… floor
// error), as the text and as the progress.
func TestAttackPercentageFractionsAreExact(t *testing.T) {
	for k := 0; k <= 100; k++ {
		m := &readmodel.GoalMetric{Kind: "percentage", Initial: f64(0), Target: f64(1), Current: f64(float64(k) / 100)}
		if want := fmt.Sprintf("%d%% / 100%%（%d%%）", k, k); GoalMetricText(m) != want {
			t.Errorf("k=%d: %q, want %q", k, GoalMetricText(m), want)
		}
	}
}

// Going down (initial 1 → target 0.5), no span, over / under the range, and the other
// kinds that must not be multiplied.
func TestAttackPercentageShapes(t *testing.T) {
	for _, tc := range []struct {
		name string
		m    *readmodel.GoalMetric
		want string
	}{
		{"down", &readmodel.GoalMetric{Kind: "percentage", Initial: f64(1), Target: f64(0.5), Current: f64(0.75)}, "75% / 50%（50%）"},
		{"no span", &readmodel.GoalMetric{Kind: "percentage", Initial: f64(0.5), Target: f64(0.5), Current: f64(0.5)}, "50% / 50%"},
		{"under", &readmodel.GoalMetric{Kind: "percentage", Initial: f64(0), Target: f64(1), Current: f64(-0.2)}, "-20% / 100%（0%）"},
		{"over", &readmodel.GoalMetric{Kind: "percentage", Initial: f64(0), Target: f64(1), Current: f64(3)}, "300% / 100%（100%）"},
		{"no initial", &readmodel.GoalMetric{Kind: "percentage", Target: f64(1), Current: f64(0.5)}, "50% / 100%（50%）"},
		{"no target", &readmodel.GoalMetric{Kind: "percentage", Current: f64(0.5)}, "50%"},
		{"nothing", &readmodel.GoalMetric{Kind: "percentage"}, ""},
		{"precision", &readmodel.GoalMetric{Kind: "percentage", Precision: 1, Initial: f64(0), Target: f64(1), Current: f64(0.123)}, "12.3% / 100.0%（12%）"},
		{"display wins for current", &readmodel.GoalMetric{Kind: "percentage", Initial: f64(0), Target: f64(1), Current: f64(0.4), Display: "40%"}, "40% / 100%（40%）"},
		{"currency not multiplied", &readmodel.GoalMetric{Kind: "currency", Currency: "USD", Target: f64(1), Current: f64(0.5), Precision: 2}, "USD 0.50 / USD 1.00（50%）"},
		{"number not multiplied", &readmodel.GoalMetric{Kind: "number", Target: f64(1), Current: f64(0.5), Precision: 1}, "0.5 / 1.0（50%）"},
		{"unknown kind is a number", &readmodel.GoalMetric{Kind: "weird", Target: f64(1), Current: f64(0.5), Precision: 1}, "0.5 / 1.0（50%）"},
		{"empty kind is a number", &readmodel.GoalMetric{Target: f64(2), Current: f64(1)}, "1 / 2（50%）"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := GoalMetricText(tc.m); got != tc.want {
				t.Errorf("%q, want %q", got, tc.want)
			}
		})
	}
}

// Odd floats do not panic and are not shown as a number that is not one.
func TestAttackPercentageOddFloats(t *testing.T) {
	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), 1e308, -1e308, math.SmallestNonzeroFloat64} {
		m := &readmodel.GoalMetric{Kind: "percentage", Initial: f64(0), Target: f64(v), Current: f64(v)}
		_ = GoalMetricText(m)
		m = &readmodel.GoalMetric{Kind: "percentage", Initial: f64(v), Target: f64(1), Current: f64(0.5), Precision: -3}
		_ = GoalMetricText(m)
		if p, ok := m.Progress(); ok && (p < 0 || p > 1 || math.IsNaN(p)) {
			t.Errorf("progress %v for %v", p, v)
		}
	}
}

func TestProjectGroupAndGoalStatusLabel(t *testing.T) {
	if got := ProjectGroup(readmodel.ProjectView{Favorite: true, TeamName: "Design"}); got != "★ お気に入り" {
		t.Errorf("favorite ProjectGroup = %q", got)
	}
	if got := ProjectGroup(readmodel.ProjectView{TeamName: "Eng\x1b[31m", TeamGID: "t1"}); got != "Eng[31m" {
		t.Errorf("team ProjectGroup = %q", got)
	}
	if got := ProjectGroup(readmodel.ProjectView{TeamGID: "t1"}); got != "t1" {
		t.Errorf("team gid ProjectGroup = %q", got)
	}
	if got := ProjectGroup(readmodel.ProjectView{}); got != "チームなし" {
		t.Errorf("empty ProjectGroup = %q", got)
	}
	if got := GoalStatusLabel("green"); got != "順調" {
		t.Errorf("GoalStatusLabel(green) = %q", got)
	}
	if got := GoalStatusLabel(""); got != "未設定" {
		t.Errorf("GoalStatusLabel('') = %q", got)
	}
	if got := GoalStatusLabel("bad\x1b[0m"); got != "bad[0m" {
		t.Errorf("GoalStatusLabel(control) = %q", got)
	}
}
