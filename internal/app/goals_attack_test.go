package app_test

import (
	"context"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/asana/model"
)

func attackUC(t *testing.T, today string) (*app.Goals, context.Context) {
	t.Helper()
	d, err := time.Parse("2006-01-02", today)
	if err != nil {
		t.Fatal(err)
	}
	return &app.Goals{Store: goalStore(t), Clock: func() time.Time { return d.Add(12 * time.Hour) }}, context.Background()
}

// Period boundaries: the first and last day are inside; the day after is not.
func TestAttackCurrentPeriodBoundaries(t *testing.T) {
	for today, want := range map[string]string{
		"2026-09-30": "年間,夏",
		"2026-10-01": "年間,秋",
		"2026-12-31": "年間,秋",
		"2027-01-01": "来年",
		"2025-12-31": "",
	} {
		uc, ctx := attackUC(t, today)
		l, err := uc.List(ctx, app.PeriodCurrent)
		if err != nil {
			t.Fatal(err)
		}
		if got := goalNames(l); got != want {
			t.Errorf("%s: %q, want %q", today, got, want)
		}
	}
}

// A period without a range selects only its own goals; it must not swallow the rest, and
// goals whose period has no range are never "current".
func TestAttackPeriodWithoutRange(t *testing.T) {
	ctx := context.Background()
	st := goalStore(t)
	if err := st.ReplaceGoals(ctx, "w1", []model.Goal{
		{GID: "a", Name: "A", TimePeriod: &model.TimePeriod{GID: "open", DisplayName: "Open"}},
		{GID: "b", Name: "B", TimePeriod: &model.TimePeriod{GID: "q", DisplayName: "Q", StartOn: "2026-01-01", EndOn: "2026-12-31"}},
		{GID: "c", Name: "C", TimePeriod: &model.TimePeriod{GID: "half", DisplayName: "Half", StartOn: "2026-01-01"}},
	}); err != nil {
		t.Fatal(err)
	}
	uc := &app.Goals{Store: st, Clock: func() time.Time { return time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC) }}
	if l, err := uc.List(ctx, "Open"); err != nil || goalNames(l) != "A" {
		t.Fatalf("Open: %v, %v", goalNames(l), err)
	}
	if l, err := uc.List(ctx, "Half"); err != nil || goalNames(l) != "C" {
		t.Fatalf("Half: %v, %v", goalNames(l), err)
	}
	if l, err := uc.List(ctx, app.PeriodCurrent); err != nil || goalNames(l) != "B" {
		t.Fatalf("current: %v, %v", goalNames(l), err)
	}
}

// Odd period arguments: blank means all; whitespace is trimmed; the literal "current" wins;
// regex/LIKE characters are plain text.
func TestAttackPeriodArguments(t *testing.T) {
	uc, ctx := attackUC(t, "2026-11-18")
	for _, arg := range []string{"   ", "\t"} {
		if l, err := uc.List(ctx, arg); err != nil || len(l.Goals) != 5 {
			t.Errorf("%q: %v, %v", arg, l, err)
		}
	}
	if l, err := uc.List(ctx, "  Q4 FY26 "); err != nil || goalNames(l) != "秋" {
		t.Errorf("trimmed: %v", err)
	}
	for _, arg := range []string{"%", ".*", "FY%", "Q_ FY26"} {
		if _, err := uc.List(ctx, arg); err == nil {
			t.Errorf("%q matched a period", arg)
		}
	}
}

// Two periods share a name: the lookup is stable and does not fail.
func TestAttackDuplicatePeriodNames(t *testing.T) {
	ctx := context.Background()
	st := goalStore(t)
	if err := st.ReplaceTimePeriods(ctx, "w1", []model.TimePeriod{
		{GID: "x1", DisplayName: "Dup", StartOn: "2026-01-01", EndOn: "2026-03-31"},
		{GID: "x2", DisplayName: "Dup", StartOn: "2026-04-01", EndOn: "2026-06-30"},
	}); err != nil {
		t.Fatal(err)
	}
	uc := &app.Goals{Store: st, Clock: time.Now}
	l1, err := uc.List(ctx, "Dup")
	if err != nil || l1.Period == nil {
		t.Fatalf("%v", err)
	}
	l2, _ := uc.List(ctx, "Dup")
	if l1.Period.GID != l2.Period.GID {
		t.Fatal("unstable pick")
	}
	if l, err := uc.List(ctx, "x2"); err != nil || l.Period.GID != "x2" {
		t.Fatalf("by gid: %+v, %v", l, err)
	}
}
