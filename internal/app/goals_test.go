package app_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/domain/plan"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/store"
)

func goalStore(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.UpsertWorkspaces(ctx, []model.Workspace{{GID: "w1", Name: "会社"}}))
	fy := &model.TimePeriod{GID: "fy", DisplayName: "FY26", Period: "FY", StartOn: "2026-01-01", EndOn: "2026-12-31"}
	q3 := &model.TimePeriod{GID: "q3", DisplayName: "Q3 FY26", Period: "Q3", StartOn: "2026-07-01", EndOn: "2026-09-30"}
	q4 := &model.TimePeriod{GID: "q4", DisplayName: "Q4 FY26", Period: "Q4", StartOn: "2026-10-01", EndOn: "2026-12-31"}
	fy27 := &model.TimePeriod{GID: "fy27", DisplayName: "FY27", Period: "FY", StartOn: "2027-01-01", EndOn: "2027-12-31"}
	must(st.ReplaceGoals(ctx, "w1", []model.Goal{
		{GID: "g-year", Name: "年間", TimePeriod: fy},
		{GID: "g-q3", Name: "夏", TimePeriod: q3},
		{GID: "g-q4", Name: "秋", TimePeriod: q4},
		{GID: "g-next", Name: "来年", TimePeriod: fy27},
		{GID: "g-none", Name: "期間なし"},
	}))
	must(st.ReplaceTimePeriods(ctx, "w1", []model.TimePeriod{*fy, *q3, *q4}))
	return st
}

func goalNames(l *readmodel.GoalList) string {
	var names []string
	for _, g := range l.Goals {
		names = append(names, g.Name)
	}
	return strings.Join(names, ",")
}

// A goal list is narrowed to a period (with the periods within it), to the periods that
// include today, or not at all; an unknown period says which there are.
func TestGoalListPeriods(t *testing.T) {
	ctx := context.Background()
	uc := &app.Goals{Store: goalStore(t), Clock: func() time.Time { return time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC) }}
	for _, tc := range []struct{ period, want string }{
		{"", "年間,夏,秋,来年,期間なし"},
		{app.PeriodCurrent, "年間,秋"},
		{"FY26", "年間,夏,秋"},
		{"fy26", "年間,夏,秋"},
		{"Q4 FY26", "秋"},
		{"q3", "夏"},
		{"FY27", "来年"}, // only known from a goal
	} {
		l, err := uc.List(ctx, tc.period)
		if err != nil {
			t.Fatalf("%q: %v", tc.period, err)
		}
		if got := goalNames(l); got != tc.want {
			t.Errorf("%q: %s, want %s", tc.period, got, tc.want)
		}
	}
	if _, err := uc.List(ctx, "Q1 FY30"); err == nil || !strings.Contains(err.Error(), "Q4 FY26") {
		t.Fatalf("unknown period: %v", err)
	}
}

// A 402 hides goals for a day; a value that is not a time does not hide them for good.
func TestGoalsPlanUnavailable(t *testing.T) {
	ctx := context.Background()
	st := goalStore(t)
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	uc := &app.Goals{Store: st, Clock: func() time.Time { return now }}
	if err := st.RecordPlanRefusal(ctx, plan.Goals, "w1", now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if uc.Available(ctx) || !uc.PortfoliosAvailable(ctx) {
		t.Fatal("a 402 of goals")
	}
	if _, err := uc.Get(ctx, "秋"); !app.IsPlanUnavailable(err) || !strings.Contains(err.Error(), "ゴール") {
		t.Fatalf("get: %v", err)
	}
	for _, v := range []string{"", "yes", now.Add(-plan.RecheckAfter).Format(time.RFC3339), now.Add(time.Hour).Format(time.RFC3339)} {
		if err := st.SetSyncState(ctx, "plan_unavailable:goals:w1", v); err != nil {
			t.Fatal(err)
		}
		if !uc.Available(ctx) {
			t.Errorf("hidden by %q", v)
		}
	}
}

// Only the default workspace's goals, periods and portfolios are shown.
func TestGoalsOfTheDefaultWorkspace(t *testing.T) {
	ctx := context.Background()
	st := goalStore(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.UpsertWorkspaces(ctx, []model.Workspace{{GID: "w1", Name: "会社"}, {GID: "w2", Name: "別"}}))
	must(st.ReplaceGoals(ctx, "w2", []model.Goal{{GID: "g-other", Name: "別のゴール"}}))
	must(st.ReplaceTimePeriods(ctx, "w2", []model.TimePeriod{{GID: "p-other", DisplayName: "別の期間"}}))
	must(st.ReplacePortfolios(ctx, "w2", []model.Portfolio{{GID: "pf-other", Name: "別"}}))
	must(st.ReplacePortfolios(ctx, "w1", []model.Portfolio{{GID: "pf1", Name: "こちら"}}))
	uc := &app.Goals{Store: st, Clock: func() time.Time { return time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC) }}
	l, err := uc.List(ctx, "")
	must(err)
	if strings.Contains(goalNames(l), "別") || len(l.Periods) != 3 {
		t.Fatalf("list = %s, %d periods", goalNames(l), len(l.Periods))
	}
	ps, err := uc.Portfolios(ctx)
	if err != nil || len(ps) != 1 || ps[0].GID != "pf1" {
		t.Fatalf("portfolios = %+v, %v", ps, err)
	}
}
