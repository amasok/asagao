package tui

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/store"
	"github.com/amasok/asagao/internal/sync"
)

// goalsStore is overviewStore with goals of this quarter and of an old year (g1 supported
// by subgoal g2, project p1 and a task), and a portfolio holding p1.
func goalsStore(t *testing.T) *store.Store {
	t.Helper()
	st := overviewStore(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.UpsertWorkspaces(ctx, []model.Workspace{{GID: "w1", Name: "会社"}}))
	q := &model.TimePeriod{GID: "tpq", DisplayName: "Q4 FY26", StartOn: "2026-10-01", EndOn: "2026-12-31"}
	old := &model.TimePeriod{GID: "tpo", DisplayName: "FY01", StartOn: "2001-01-01", EndOn: "2001-12-31"}
	g1 := model.Goal{GID: "g1", Name: "売上を伸ばす", Status: "green", TimePeriod: q, Notes: "今期の柱",
		Metric: &model.GoalMetric{TargetNumberValue: ptrF(100), CurrentNumberValue: ptrF(40)}}
	must(st.ReplaceGoals(ctx, "w1", []model.Goal{g1, {GID: "g2", Name: "新規顧客", Status: "red", TimePeriod: q},
		{GID: "g3", Name: "昔のゴール", Status: "achieved", TimePeriod: old}}))
	must(st.SaveGoal(ctx, g1, []model.GoalRelationship{
		{SupportingResource: &model.SupportingWork{GID: "g2", ResourceType: "goal", Name: "新規顧客"}},
		{SupportingResource: &model.SupportingWork{GID: "p1", ResourceType: "project", Name: "新居プロジェクト"}},
	}, testNow))
	must(st.ReplacePortfolios(ctx, "w1", []model.Portfolio{{GID: "pf1", Name: "今期の施策"}}))
	must(st.SavePortfolio(ctx, model.Portfolio{GID: "pf1", Name: "今期の施策"},
		[]model.PortfolioItem{{GID: "p1", ResourceType: "project", Name: "新居プロジェクト", CurrentStatusUpdate: &model.StatusBrief{StatusType: "off_track"}}}, testNow))
	return st
}

func ptrF(v float64) *float64 { return &v }

func goalsHarness(t *testing.T, st *store.Store, now func() time.Time) *harness {
	t.Helper()
	m := New(context.Background(), Deps{Store: st, Now: now})
	h := &harness{t: t, m: m}
	h.send(tea.WindowSizeMsg{Width: 140, Height: 30})
	h.run(m.Init())
	return h
}

func mustShow(t *testing.T, screen string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(screen, w) {
			t.Fatalf("screen lacks %q:\n%s", w, screen)
		}
	}
}

// space g lists the goals; enter opens one with what supports it, and from there a
// subgoal (esc comes back) or a project (the list moves to it); c narrows to the current
// periods; tab shows the portfolios.
func TestGoalsScreen(t *testing.T) {
	h := goalsHarness(t, goalsStore(t), func() time.Time { return testNow })
	m := h.m
	h.key(" ", "Q")
	mustShow(t, h.screen(), "ゴール", "[順調] 売上を伸ばす", "40 / 100（40%）", "Q4 FY26", "[遅れ] 新規顧客", "[達成] 昔のゴール")
	h.key("c")
	if s := h.screen(); strings.Contains(s, "昔のゴール") || !strings.Contains(s, "今日を含む期間") {
		t.Fatalf("current periods:\n%s", s)
	}
	h.key("enter")
	mustShow(t, h.screen(), "ゴール（売上を伸ばす）", "今期の柱", "サブゴール  新規顧客", "プロジェクト  新居プロジェクト")
	h.key("enter") // the subgoal
	mustShow(t, h.screen(), "ゴール（新規顧客）", "オンラインのときに取得します")
	h.key("esc")
	mustShow(t, h.screen(), "ゴール（売上を伸ばす）")
	if m.goals.cursor != 0 {
		t.Fatalf("cursor after esc = %d", m.goals.cursor)
	}
	h.key("j", "enter") // the project
	if m.goals.open || m.currentProject() != "p1" {
		t.Fatalf("not moved to p1: open=%v project=%q", m.goals.open, m.currentProject())
	}
	h.key(" ", "Q", "esc") // esc closes from the list
	if m.goals.open {
		t.Fatalf("esc did not close the list:\n%s", h.screen())
	}
	h.key(" ", "Q", "tab")
	mustShow(t, h.screen(), "ポートフォリオ", "今期の施策")
	h.key("enter")
	mustShow(t, h.screen(), "ポートフォリオ（今期の施策）", "プロジェクト  [遅れ] 新居プロジェクト")
	h.key("esc", "esc", "esc")
	if m.goals.open {
		t.Fatal("still open after esc")
	}
}

// Goals and portfolios that Asana refused with a 402 are not in the menu.
func TestGoalsMenuHiddenByPlan(t *testing.T) {
	st := goalsStore(t)
	if err := st.SetSyncState(context.Background(), readmodel.PlanKey(readmodel.PlanGoals, "w1"), testNow.Add(-time.Hour).UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	h := goalsHarness(t, st, func() time.Time { return testNow })
	h.key(" ")
	if s := h.screen(); strings.Contains(s, "ゴール（期間・進み具合）") || !strings.Contains(s, "ポートフォリオ（自分がオーナーのもの）") {
		t.Fatalf("menu:\n%s", s)
	}
	h.key("esc", " ", "l")
	if s := h.screen(); strings.Contains(s, "tab ゴール") {
		t.Fatalf("tab offers hidden goals:\n%s", s)
	}
	h.key("tab")
	if h.m.goals.portfolios != true {
		t.Fatal("tab switched to hidden goals")
	}
}

// Online, the list is fetched when opened; a 402 says so and hides goals from the menu.
func TestGoalsScreenOnline402(t *testing.T) {
	st := goalsStore(t)
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/goals" && status != http.StatusOK:
			w.WriteHeader(status)
			fmt.Fprint(w, `{"errors":[{"message":"Payment Required"}]}`)
		case r.URL.Path == "/goals":
			fmt.Fprint(w, `{"data":[{"gid":"g9","name":"取ってきたゴール","status":"yellow"}]}`)
		case r.URL.Path == "/time_periods":
			fmt.Fprint(w, `{"data":[]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"errors":[{"message":"Not Found"}]}`)
		}
	}))
	defer srv.Close()
	api := asana.NewClient(srv.Client())
	api.BaseURL, api.MaxRetries = srv.URL, 0
	h := goalsHarness(t, st, time.Now) // the 402 is recorded with the syncer's clock
	h.m.syncer = &sync.Syncer{API: api, Store: st}
	h.key(" ", "Q")
	if s := h.screen(); !strings.Contains(s, "取ってきたゴール") || strings.Contains(s, "売上を伸ばす") {
		t.Fatalf("not fetched:\n%s", s)
	}
	h.key("esc")
	status = http.StatusPaymentRequired
	h.key(" ", "Q")
	mustShow(t, h.screen(), "ゴールを API で使えません")
	h.key("esc", " ")
	if strings.Contains(h.screen(), "ゴール（期間・進み具合）") {
		t.Fatalf("goals still in the menu:\n%s", h.screen())
	}
}

// An answer of an older load, or the cached answer arriving after the fetched one, does
// not replace what is shown.
func TestGoalsScreenIgnoresStaleAnswers(t *testing.T) {
	h := goalsHarness(t, goalsStore(t), func() time.Time { return testNow })
	m := h.m
	h.key(" ", "Q")
	seq := m.goals.seq
	fetched := &readmodel.GoalList{Goals: []readmodel.GoalView{{GID: "new", Name: "新しい"}}, FetchedAt: "x"}
	cached := &readmodel.GoalList{Goals: []readmodel.GoalView{{GID: "old", Name: "古い"}}, FetchedAt: "x"}
	h.send(goalListMsg{seq: seq, fetched: true, list: fetched})
	h.send(goalListMsg{seq: seq, list: cached})
	h.send(goalListMsg{seq: seq - 1, fetched: true, list: cached})
	if s := h.screen(); !strings.Contains(s, "新しい") || strings.Contains(s, "古い") {
		t.Fatalf("stale answer shown:\n%s", s)
	}
}
