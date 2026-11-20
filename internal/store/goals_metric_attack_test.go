package store

import (
	"context"
	"testing"

	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/ui/format"
)

// The kind comes from the unit; resource_subtype is only a fallback. Empty, unknown,
// differently-cased and contradicting combinations never panic and never invent a kind.
func TestAttackMetricKind(t *testing.T) {
	for _, tc := range []struct{ subtype, unit, want string }{
		{"number", "percentage", "percentage"},
		{"number", "currency", "currency"},
		{"number", "none", "number"},
		{"number", "", "number"},
		{"", "", "number"},
		{"", "percentage", "percentage"},
		{"percentage", "", "percentage"},         // the reference's way
		{"currency", "none", "currency"},         // the reference's way
		{"currency", "percentage", "percentage"}, // the unit wins
		{"percentage", "currency", "currency"},
		{"percentage", "number", "percentage"},
		{"weird", "weird", "number"},
		{"number", "PERCENTAGE", "number"}, // not what Asana sends; not guessed
		{"number", " percentage", "number"},
	} {
		if got := metricKind(model.GoalMetric{ResourceSubtype: tc.subtype, Unit: tc.unit}); got != tc.want {
			t.Errorf("subtype %q unit %q: %q, want %q", tc.subtype, tc.unit, got, tc.want)
		}
	}
}

// Stored and read back: precision of a currency goal, a percentage goal with a display
// value, and a goal with no metric at all.
func TestAttackGoalMetricsRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := projectStore(t)
	f := func(v float64) *float64 { return &v }
	must(t, s.ReplaceGoals(ctx, "w1", []model.Goal{
		{GID: "g1", Name: "売上", Metric: &model.GoalMetric{ResourceSubtype: "number", Unit: "currency", CurrencyCode: "EUR", Precision: 2,
			TargetNumberValue: f(1234.5), CurrentNumberValue: f(0.1 + 0.2)}},
		{GID: "g2", Name: "割合", Metric: &model.GoalMetric{ResourceSubtype: "number", Unit: "percentage", Precision: 1,
			InitialNumberValue: f(0), TargetNumberValue: f(1), CurrentNumberValue: f(0.333)}},
		{GID: "g3", Name: "なし"},
		{GID: "g4", Name: "空", Metric: &model.GoalMetric{}},
	}))
	for gid, want := range map[string]string{
		"g1": "EUR 0.30 / EUR 1234.50（0%）",
		"g2": "33.3% / 100.0%（33%）",
		"g3": "",
		"g4": "",
	} {
		g, err := s.GoalDetail(ctx, gid)
		must(t, err)
		if got := format.GoalMetricText(g.Metric); got != want {
			t.Errorf("%s: %q, want %q", gid, got, want)
		}
	}
}
