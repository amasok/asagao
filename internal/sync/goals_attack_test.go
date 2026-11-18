package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	gosync "sync"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/readmodel"
)

// 429 / 403 / 5xx on goals: the error comes back, the cache stays, and nothing is hidden.
func TestAttackGoalsNon402FailuresHideNothing(t *testing.T) {
	for _, code := range []int{http.StatusTooManyRequests, http.StatusForbidden, http.StatusBadGateway, http.StatusUnauthorized} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			s, st, f, now := setupGoals(t)
			ctx := context.Background()
			if err := s.FetchGoals(ctx); err != nil {
				t.Fatal(err)
			}
			f.set(func() { f.status["/goals"] = code })
			if err := s.FetchGoals(ctx); err == nil {
				t.Fatalf("%d is not returned", code)
			}
			uc := goalUC(st, now)
			if !uc.Available(ctx) {
				t.Fatalf("%d hid goals", code)
			}
			if l, err := uc.List(ctx, ""); err != nil || len(l.Goals) != 2 {
				t.Fatalf("cache after %d: %+v, %v", code, l, err)
			}
			if v, _ := st.SyncState(ctx, readmodel.PlanKey(readmodel.PlanGoals, "w1")); v != "" {
				t.Fatalf("%d recorded as a 402: %q", code, v)
			}
		})
	}
}

// A 402 keeps the cached goals in the store (only hides them): when the plan comes back
// without a fetch having replaced them, they show again.
func TestAttackGoals402KeepsCacheAndRecovers(t *testing.T) {
	s, st, f, now := setupGoals(t)
	ctx := context.Background()
	if err := s.FetchGoals(ctx); err != nil {
		t.Fatal(err)
	}
	f.set(func() { f.status["/goals"] = http.StatusPaymentRequired })
	*now = now.Add(readmodel.PlanRecheckAfter + time.Second)
	if err := s.FetchGoals(ctx); err != nil {
		t.Fatal(err)
	}
	uc := goalUC(st, now)
	if uc.Available(ctx) {
		t.Fatal("not hidden")
	}
	*now = now.Add(readmodel.PlanRecheckAfter + time.Second)
	if l, err := uc.List(ctx, ""); err != nil || len(l.Goals) != 2 {
		t.Fatalf("cache lost by a 402: %+v, %v", l, err)
	}
}

// The 402 of goals and of portfolios are kept apart, both ways.
func TestAttackPlanFeaturesAreIndependent(t *testing.T) {
	s, st, f, now := setupGoals(t)
	ctx := context.Background()
	uc := goalUC(st, now)
	f.set(func() { f.status["/goals"] = http.StatusPaymentRequired })
	if err := s.FetchGoals(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.FetchPortfolios(ctx); err != nil {
		t.Fatal(err)
	}
	if uc.Available(ctx) || !uc.PortfoliosAvailable(ctx) {
		t.Fatalf("goals %v portfolios %v", uc.Available(ctx), uc.PortfoliosAvailable(ctx))
	}
	if ps, err := uc.Portfolios(ctx); err != nil || len(ps) != 1 {
		t.Fatalf("portfolios: %+v, %v", ps, err)
	}
}

// A clock that is behind the recorded 402 (a skew) must not hide goals for good.
func TestAttackClockBehindA402(t *testing.T) {
	s, st, f, now := setupGoals(t)
	ctx := context.Background()
	f.set(func() { f.status["/goals"] = http.StatusPaymentRequired })
	if err := s.FetchGoals(ctx); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(-48 * time.Hour)
	f.set(func() { delete(f.status, "/goals") })
	if !goalUC(st, now).Available(ctx) {
		t.Fatal("hidden by a 402 from the future")
	}
	if err := s.FetchGoals(ctx); err != nil {
		t.Fatal(err)
	}
	if l, err := goalUC(st, now).List(ctx, ""); err != nil || len(l.Goals) != 2 {
		t.Fatalf("%+v, %v", l, err)
	}
}

// A broken marker value is not a 402.
func TestAttackGarbageMarker(t *testing.T) {
	s, st, _, now := setupGoals(t)
	ctx := context.Background()
	if err := st.SetSyncState(ctx, readmodel.PlanKey(readmodel.PlanGoals, "w1"), "yesterday"); err != nil {
		t.Fatal(err)
	}
	if err := s.FetchGoals(ctx); err != nil {
		t.Fatal(err)
	}
	if l, err := goalUC(st, now).List(ctx, ""); err != nil || len(l.Goals) != 2 {
		t.Fatalf("%+v, %v", l, err)
	}
}

// Asana lists 250 goals over three pages: all are kept, in order.
func TestAttackGoalsPagination(t *testing.T) {
	s, st, f, now := setupGoals(t)
	var calls int
	var mu gosync.Mutex
	inner := f.handler()
	page := func(from, to int, next string) string {
		var items []map[string]any
		for i := from; i < to; i++ {
			items = append(items, map[string]any{"gid": fmt.Sprintf("g%03d", i), "name": fmt.Sprintf("goal %03d", i)})
		}
		b, _ := json.Marshal(items)
		if next == "" {
			return fmt.Sprintf(`{"data":%s,"next_page":null}`, b)
		}
		return fmt.Sprintf(`{"data":%s,"next_page":{"offset":%q}}`, b, next)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/goals" {
			inner.ServeHTTP(w, r)
			return
		}
		mu.Lock()
		calls++
		mu.Unlock()
		switch r.URL.Query().Get("offset") {
		case "":
			fmt.Fprint(w, page(0, 100, "o1"))
		case "o1":
			fmt.Fprint(w, page(100, 200, "o2"))
		default:
			fmt.Fprint(w, page(200, 250, ""))
		}
	}))
	defer srv.Close()
	repoint(s, srv)
	ctx := context.Background()
	if err := s.FetchGoals(ctx); err != nil {
		t.Fatal(err)
	}
	l, err := goalUC(st, now).List(ctx, "")
	if err != nil || len(l.Goals) != 250 || calls != 3 {
		t.Fatalf("%d goals, %d calls, %v", len(l.Goals), calls, err)
	}
	for i, g := range l.Goals {
		if g.GID != fmt.Sprintf("g%03d", i) {
			t.Fatalf("order broken at %d: %s", i, g.GID)
		}
	}
}

// A page that fails in the middle must not wipe the goals already cached (no partial replace).
func TestAttackGoalsFailureOnSecondPageKeepsCache(t *testing.T) {
	s, st, f, now := setupGoals(t)
	ctx := context.Background()
	if err := s.FetchGoals(ctx); err != nil {
		t.Fatal(err)
	}
	inner := f.handler()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/goals" {
			if r.URL.Query().Get("offset") == "" {
				fmt.Fprint(w, `{"data":[{"gid":"x1","name":"x"}],"next_page":{"offset":"o"}}`)
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		inner.ServeHTTP(w, r)
	}))
	defer srv.Close()
	repoint(s, srv)
	if err := s.FetchGoals(ctx); err == nil {
		t.Fatal("no error")
	}
	if l, err := goalUC(st, now).List(ctx, ""); err != nil || len(l.Goals) != 2 || l.Goals[0].GID != "g1" {
		t.Fatalf("%+v, %v", l, err)
	}
}

// Goals fetched at the same time from several places do not race or corrupt the cache.
func TestAttackConcurrentFetches(t *testing.T) {
	s, st, _, now := setupGoals(t)
	ctx := context.Background()
	var wg gosync.WaitGroup
	errs := make(chan error, 40)
	for i := 0; i < 8; i++ {
		wg.Add(5)
		go func() { defer wg.Done(); errs <- s.FetchGoals(ctx) }()
		go func() { defer wg.Done(); errs <- s.FetchGoal(ctx, "g1") }()
		go func() { defer wg.Done(); errs <- s.FetchPortfolios(ctx) }()
		go func() { defer wg.Done(); errs <- s.FetchPortfolio(ctx, "pf1") }()
		go func() { defer wg.Done(); _, err := goalUC(st, now).List(ctx, "current"); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("%v", err)
		}
	}
	if l, err := goalUC(st, now).List(ctx, ""); err != nil || len(l.Goals) != 2 {
		t.Fatalf("%+v, %v", l, err)
	}
}

// A cancelled context returns an error and leaves the cache as it was.
func TestAttackCancelledContext(t *testing.T) {
	s, st, _, now := setupGoals(t)
	if err := s.FetchGoals(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, err := range []error{s.FetchGoals(ctx), s.FetchGoal(ctx, "g1"), s.FetchPortfolios(ctx), s.FetchPortfolio(ctx, "pf1")} {
		if err == nil {
			t.Error("a cancelled fetch succeeded")
		}
	}
	if l, err := goalUC(st, now).List(context.Background(), ""); err != nil || len(l.Goals) != 2 {
		t.Fatalf("%+v, %v", l, err)
	}
}

// If what supports a goal cannot be fetched, the goal is not half saved (and a 402 there
// does not hide anything: it is not the plan check's business).
func TestAttackGoalRelationshipsFail(t *testing.T) {
	s, st, f, now := setupGoals(t)
	ctx := context.Background()
	if err := s.FetchGoals(ctx); err != nil {
		t.Fatal(err)
	}
	f.set(func() { f.status["/goal_relationships"] = http.StatusInternalServerError })
	if err := s.FetchGoal(ctx, "g1"); err == nil {
		t.Fatal("no error")
	}
	g, err := goalUC(st, now).Get(ctx, "g1")
	if err != nil || g.HTMLNotes != "" || len(g.Supporting) != 0 {
		t.Fatalf("half saved: %+v, %v", g, err)
	}
}

// Asana sends a goal without a gid or a name in the list: nothing breaks, the rest is kept.
func TestAttackOddGoalsInList(t *testing.T) {
	s, st, f, now := setupGoals(t)
	ctx := context.Background()
	f.set(func() {
		f.goals = `[{"name":"no gid"},{"gid":"g9"},{"gid":"g9","name":"dup"},null,{"gid":"g8","name":"ok","metric":{"target_number_value":0,"current_number_value":0}}]`
	})
	if err := s.FetchGoals(ctx); err != nil {
		t.Fatal(err)
	}
	l, err := goalUC(st, now).List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, g := range l.Goals {
		seen[g.GID]++
	}
	if seen["g9"] != 1 || seen["g8"] != 1 || seen[""] != 0 {
		t.Fatalf("goals = %+v", l.Goals)
	}
}

// Asana answers an empty list: the cached goals go (they are gone from my view).
func TestAttackEmptyListClears(t *testing.T) {
	s, st, f, now := setupGoals(t)
	ctx := context.Background()
	if err := s.FetchGoals(ctx); err != nil {
		t.Fatal(err)
	}
	f.set(func() { f.goals = `[]` })
	if err := s.FetchGoals(ctx); err != nil {
		t.Fatal(err)
	}
	if l, err := goalUC(st, now).List(ctx, ""); err != nil || len(l.Goals) != 0 {
		t.Fatalf("%+v, %v", l, err)
	}
}

// repoint sends the syncer's API calls to srv.
func repoint(s *Syncer, srv *httptest.Server) {
	api := asana.NewClient(srv.Client())
	api.BaseURL = srv.URL
	api.MaxRetries = 0
	s.API = api
}
