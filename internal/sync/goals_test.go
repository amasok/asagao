package sync

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	gosync "sync"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/domain/plan"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/store"
	"github.com/amasok/asagao/internal/ui/format"
)

// goalsAsana answers the goal, time period and portfolio endpoints as Asana documents
// them. status[path] answers that path with the status instead.
type goalsAsana struct {
	t      *testing.T
	mu     gosync.Mutex
	status map[string]int
	goals  string // the data of GET /goals
	calls  map[string]int
	query  map[string]string // the last query of each path
}

func (f *goalsAsana) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.calls[r.URL.Path]++
		f.query[r.URL.Path] = r.URL.RawQuery
		code := f.status[r.URL.Path]
		goals := f.goals
		f.mu.Unlock()
		if code != 0 {
			w.WriteHeader(code)
			fmt.Fprint(w, `{"errors":[{"message":"Payment Required"}]}`)
			return
		}
		switch r.URL.Path {
		case "/goals":
			if r.URL.Query().Get("workspace") != "w1" {
				f.t.Errorf("goals of workspace %q", r.URL.Query().Get("workspace"))
			}
			fmt.Fprintf(w, `{"data":%s,"next_page":null}`, goals)
		case "/goals/g1":
			fmt.Fprint(w, `{"data":{"gid":"g1","name":"売上を伸ばす","notes":"説明","html_notes":"<body><strong>説明</strong></body>",
				"status":"green","workspace":{"gid":"w1"},"owner":{"gid":"u1","name":"山田"},
				"metric":{"resource_subtype":"number","precision":0,"initial_number_value":0,"target_number_value":100,"current_number_value":40,"current_display_value":"40","progress_source":"manual"},
				"time_period":{"gid":"tp1","display_name":"Q4 FY26","period":"Q4","start_on":"2026-10-01","end_on":"2026-12-31"}}}`)
		case "/goal_relationships":
			if r.URL.Query().Get("supported_goal") != "g1" {
				f.t.Errorf("relationships of %q", r.URL.Query().Get("supported_goal"))
			}
			fmt.Fprint(w, `{"data":[
				{"gid":"r1","resource_subtype":"subgoal","contribution_weight":1,"supporting_resource":{"gid":"g2","resource_type":"goal","name":"新規顧客"}},
				{"gid":"r2","resource_subtype":"supporting_work","contribution_weight":0,"supporting_resource":{"gid":"p1","resource_type":"project","name":"営業"}}],"next_page":null}`)
		case "/time_periods":
			fmt.Fprint(w, `{"data":[
				{"gid":"fy","display_name":"FY26","period":"FY","start_on":"2026-01-01","end_on":"2026-12-31"},
				{"gid":"tp1","display_name":"Q4 FY26","period":"Q4","start_on":"2026-10-01","end_on":"2026-12-31","parent":{"gid":"fy"}}],"next_page":null}`)
		case "/portfolios":
			if q := r.URL.Query(); q.Get("owner") != "me" || q.Get("workspace") != "w1" {
				f.t.Errorf("portfolios query %v", q)
			}
			fmt.Fprint(w, `{"data":[{"gid":"pf1","name":"今期の施策","color":"dark-green","owner":{"gid":"u1","name":"山田"},
				"current_status_update":{"gid":"su1","status_type":"at_risk","title":"遅れ気味"}}],"next_page":null}`)
		case "/portfolios/pf1":
			fmt.Fprint(w, `{"data":{"gid":"pf1","name":"今期の施策","permalink_url":"https://app.asana.com/0/portfolio/pf1"}}`)
		case "/portfolios/pf1/items":
			fmt.Fprint(w, `{"data":[
				{"gid":"p1","resource_type":"project","name":"営業","due_on":"2026-12-01","current_status_update":{"status_type":"on_track","title":"順調"}},
				{"gid":"pf2","resource_type":"portfolio","name":"下位"}],"next_page":null}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"errors":[{"message":"Not Found"}]}`)
		}
	})
}

// set changes what the fake answers.
func (f *goalsAsana) set(change func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change()
}

func (f *goalsAsana) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[path]
}

func setupGoals(t *testing.T) (*Syncer, *store.Store, *goalsAsana, *time.Time) {
	t.Helper()
	f := &goalsAsana{t: t, status: map[string]int{}, calls: map[string]int{}, query: map[string]string{},
		goals: `[{"gid":"g1","name":"売上を伸ばす","status":"green","workspace":{"gid":"w1"},
			"time_period":{"gid":"tp1","display_name":"Q4 FY26","period":"Q4","start_on":"2026-10-01","end_on":"2026-12-31"}},
			{"gid":"g2","name":"新規顧客","status":"red"}]`}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.UpsertWorkspaces(ctx, []model.Workspace{{GID: "w1", Name: "会社"}}); err != nil {
		t.Fatal(err)
	}
	api := asana.NewClient(srv.Client())
	api.BaseURL = srv.URL
	api.MaxRetries = 0
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	return &Syncer{API: api, Store: st, now: func() time.Time { return now }}, st, f, &now
}

func goalUC(st *store.Store, now *time.Time) *app.Goals {
	return &app.Goals{Store: st, Clock: func() time.Time { return *now }}
}

// Goals are fetched with their time periods and kept in Asana's order; a goal fetched by
// itself gets its rich notes and what supports it.
func TestFetchGoalsAndGoal(t *testing.T) {
	s, st, f, now := setupGoals(t)
	ctx := context.Background()
	if err := s.FetchGoals(ctx); err != nil {
		t.Fatal(err)
	}
	l, err := goalUC(st, now).List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Goals) != 2 || l.Goals[0].GID != "g1" || l.Goals[1].GID != "g2" || l.Goals[0].TimePeriod == nil ||
		l.Goals[0].TimePeriod.DisplayName != "Q4 FY26" || len(l.Periods) != 2 || l.FetchedAt == "" {
		t.Fatalf("list = %+v", l)
	}
	if err := s.FetchGoal(ctx, "g1"); err != nil {
		t.Fatal(err)
	}
	g, err := goalUC(st, now).Get(ctx, "g1")
	if err != nil {
		t.Fatal(err)
	}
	if g.HTMLNotes == "" || g.Owner == nil || g.Owner.Name != "山田" || g.Metric == nil || format.GoalMetricText(g.Metric) != "40 / 100（40%）" ||
		len(g.Supporting) != 2 || g.Supporting[0].Type != "goal" || g.Supporting[1].Name != "営業" || g.FetchedAt == "" {
		t.Fatalf("goal = %+v", g)
	}
	// The list again keeps what only the goal itself gives.
	if err := s.FetchGoals(ctx); err != nil {
		t.Fatal(err)
	}
	if g, err := goalUC(st, now).Get(ctx, "g1"); err != nil || g.HTMLNotes == "" || len(g.Supporting) != 2 {
		t.Fatalf("after the list: %+v, %v", g, err)
	}
	// A goal no longer listed is gone.
	f.set(func() { f.goals = `[{"gid":"g2","name":"新規顧客"}]` })
	if err := s.FetchGoals(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := goalUC(st, now).Get(ctx, "g1"); !errors.Is(err, readmodel.ErrNotFound) {
		t.Fatalf("g1 after it left the list: %v", err)
	}
}

// A 402 hides goals (they are not asked for again for a day); then they are tried again,
// and once Asana answers, offered again.
func TestGoals402HidesThemForADay(t *testing.T) {
	s, st, f, now := setupGoals(t)
	ctx := context.Background()
	f.set(func() { f.status["/goals"] = http.StatusPaymentRequired })
	if err := s.FetchGoals(ctx); err != nil {
		t.Fatalf("a 402 is not an error of the fetch: %v", err)
	}
	uc := goalUC(st, now)
	if uc.Available(ctx) {
		t.Fatal("goals offered after a 402")
	}
	if _, err := uc.List(ctx, ""); !app.IsPlanUnavailable(err) {
		t.Fatalf("list after a 402: %v", err)
	}
	if err := s.FetchGoals(ctx); err != nil || f.count("/goals") != 1 {
		t.Fatalf("asked again within the day: %d calls, %v", f.count("/goals"), err)
	}
	if err := s.FetchGoal(ctx, "g1"); err != nil || f.count("/goals/g1") != 0 {
		t.Fatalf("a goal asked for while hidden: %d calls, %v", f.count("/goals/g1"), err)
	}
	*now = now.Add(plan.RecheckAfter + time.Minute)
	if !uc.Available(ctx) {
		t.Fatal("still hidden after a day")
	}
	f.set(func() { delete(f.status, "/goals") })
	if err := s.FetchGoals(ctx); err != nil || f.count("/goals") != 2 {
		t.Fatalf("not tried again: %d calls, %v", f.count("/goals"), err)
	}
	if _, ok, _ := st.PlanRefusedAt(ctx, plan.Goals, "w1"); ok {
		t.Fatal("the 402 is still recorded")
	}
	if l, err := uc.List(ctx, ""); err != nil || len(l.Goals) != 2 {
		t.Fatalf("list once offered: %+v, %v", l, err)
	}
}

// A 402 for one goal hides goals too.
func TestGoal402(t *testing.T) {
	s, st, f, now := setupGoals(t)
	ctx := context.Background()
	f.set(func() { f.status["/goals/g1"] = http.StatusPaymentRequired })
	if err := s.FetchGoal(ctx, "g1"); err != nil {
		t.Fatal(err)
	}
	if goalUC(st, now).Available(ctx) {
		t.Fatal("goals offered after a 402")
	}
}

// Failures other than a 402 keep the cache, are returned, and hide nothing; time periods
// that cannot be fetched keep the goals.
func TestGoalsFailuresKeepTheCache(t *testing.T) {
	s, st, f, now := setupGoals(t)
	ctx := context.Background()
	if err := s.FetchGoals(ctx); err != nil {
		t.Fatal(err)
	}
	f.set(func() { f.status["/goals"] = http.StatusInternalServerError })
	if err := s.FetchGoals(ctx); err == nil {
		t.Fatal("a 500 is not returned")
	}
	uc := goalUC(st, now)
	if l, err := uc.List(ctx, ""); err != nil || len(l.Goals) != 2 || !uc.Available(ctx) {
		t.Fatalf("after a 500: %+v, %v", l, err)
	}
	f.set(func() { delete(f.status, "/goals") })
	f.set(func() { f.status["/time_periods"] = http.StatusInternalServerError })
	f.set(func() { f.goals = `[{"gid":"g3","name":"新しい"}]` })
	if err := s.FetchGoals(ctx); err == nil {
		t.Fatal("the periods' failure is not returned")
	}
	if l, err := uc.List(ctx, ""); err != nil || len(l.Goals) != 1 || l.Goals[0].GID != "g3" || len(l.Periods) != 2 {
		t.Fatalf("goals not stored when the periods failed: %+v, %v", l, err)
	}
	// A 402 for the periods alone is not an error (the goals carry their own).
	f.set(func() { f.status["/time_periods"] = http.StatusPaymentRequired })
	if err := s.FetchGoals(ctx); err != nil || !uc.Available(ctx) {
		t.Fatalf("periods 402: %v", err)
	}
}

// A goal or portfolio Asana no longer has, or no longer lets me see, is forgotten.
func TestFetchGoneGoal(t *testing.T) {
	s, st, f, now := setupGoals(t)
	ctx := context.Background()
	if err := s.FetchGoals(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.FetchGoal(ctx, "g2"); err == nil { // the fake has no /goals/g2
		t.Fatal("a 404 is not returned")
	}
	if _, err := goalUC(st, now).Get(ctx, "g2"); !errors.Is(err, readmodel.ErrNotFound) {
		t.Fatalf("g2 kept: %v", err)
	}
	// Asana answers a goal or portfolio I cannot see with 403 ("You do not have access").
	f.set(func() {
		f.status["/goals/g1"] = http.StatusForbidden
		f.status["/portfolios/pf1"] = http.StatusForbidden
	})
	if err := s.FetchPortfolios(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.FetchGoal(ctx, "g1"); err == nil {
		t.Fatal("a 403 is not returned")
	}
	if err := s.FetchPortfolio(ctx, "pf1"); err == nil {
		t.Fatal("a 403 is not returned")
	}
	uc := goalUC(st, now)
	if _, err := uc.Get(ctx, "g1"); !errors.Is(err, readmodel.ErrNotFound) {
		t.Fatalf("g1 kept after a 403: %v", err)
	}
	if _, err := uc.Portfolio(ctx, "pf1"); !errors.Is(err, readmodel.ErrNotFound) {
		t.Fatalf("pf1 kept after a 403: %v", err)
	}
	if !uc.Available(ctx) || !uc.PortfoliosAvailable(ctx) {
		t.Fatal("a 403 hides the feature")
	}
}

// My portfolios are listed (owner=me) and a portfolio's items fetched; a 402 hides
// portfolios but not goals.
func TestFetchPortfolios(t *testing.T) {
	s, st, f, now := setupGoals(t)
	ctx := context.Background()
	uc := goalUC(st, now)
	if err := s.FetchPortfolios(ctx); err != nil {
		t.Fatal(err)
	}
	ps, err := uc.Portfolios(ctx)
	if err != nil || len(ps) != 1 || ps[0].Status == nil || ps[0].Status.StatusType != "at_risk" || ps[0].Owner.Name != "山田" {
		t.Fatalf("portfolios = %+v, %v", ps, err)
	}
	if err := s.FetchPortfolio(ctx, "pf1"); err != nil {
		t.Fatal(err)
	}
	p, err := uc.Portfolio(ctx, "今期")
	if err != nil || len(p.Items) != 2 || p.Items[0].Status.StatusType != "on_track" || p.Items[1].Type != "portfolio" ||
		p.PermalinkURL == "" || p.FetchedAt == "" {
		t.Fatalf("portfolio = %+v, %v", p, err)
	}
	if err := s.FetchPortfolio(ctx, "pf2"); err == nil {
		t.Fatal("a 404 is not returned")
	}
	f.set(func() { f.status["/portfolios"] = http.StatusPaymentRequired })
	if err := s.FetchPortfolios(ctx); err != nil {
		t.Fatal(err)
	}
	if uc.PortfoliosAvailable(ctx) || !uc.Available(ctx) {
		t.Fatal("a 402 of portfolios hides the wrong feature")
	}
}

// Offline, nothing is fetched.
func TestGoalsOffline(t *testing.T) {
	s := &Syncer{}
	ctx := context.Background()
	for _, err := range []error{s.FetchGoals(ctx), s.FetchGoal(ctx, "g1"), s.FetchPortfolios(ctx), s.FetchPortfolio(ctx, "p")} {
		if !errors.Is(err, ErrOffline) {
			t.Fatalf("offline: %v", err)
		}
	}
}

// A 402 is recorded for the workspace it came from: one of another workspace (or an old
// record without a workspace) does not hide goals here.
func TestGoals402IsPerWorkspace(t *testing.T) {
	s, st, f, now := setupGoals(t)
	ctx := context.Background()
	fresh := now.UTC().Format(time.RFC3339)
	if err := st.RecordPlanRefusal(ctx, plan.Goals, "w9", *now); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSyncState(ctx, "plan_unavailable:"+plan.Goals, fresh); err != nil {
		t.Fatal(err)
	}
	if !goalUC(st, now).Available(ctx) {
		t.Fatal("hidden by another workspace's 402")
	}
	if err := s.FetchGoals(ctx); err != nil || f.count("/goals") != 1 {
		t.Fatalf("not asked: %d calls, %v", f.count("/goals"), err)
	}
	f.set(func() { f.status["/goals"] = http.StatusPaymentRequired })
	if err := s.FetchGoals(ctx); err != nil {
		t.Fatal(err)
	}
	if at, ok, _ := st.PlanRefusedAt(ctx, plan.Goals, "w1"); !ok || !at.Equal(*now) {
		t.Fatalf("402 recorded as (%v, %v) for w1", at, ok)
	}
}
