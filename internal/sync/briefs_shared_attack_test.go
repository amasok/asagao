package sync

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/domain/plan"
)

// #71 (tester): Pusher and Details share one briefs helper. A 402 that Push records hides
// the brief from the overview (no request for it) until plan.RecheckAfter has passed, by
// the Syncer's clock; then the overview asks again and an answer forgets the 402. And the
// other way: a 402 the overview records is stamped with the Syncer's clock.
func TestBriefRefusalSharedBetweenPushAndOverview(t *testing.T) {
	ctx := context.Background()
	bs, s, st := setupBrief(t, "目的", "年内")
	if err := st.UpdateProjectBriefLocal(ctx, "p1", edit("目的", "春"), now); err != nil {
		t.Fatal(err)
	}
	bs.status["PUT /project_briefs/b1"] = http.StatusPaymentRequired
	if res, err := s.Push(ctx); err != nil || res.Failed != 1 {
		t.Fatalf("push = %+v %v", res, err)
	}
	at, ok, _ := st.PlanRefusedAt(ctx, plan.ProjectBrief, "w1")
	if !ok || !at.Equal(now) {
		t.Fatalf("refusal recorded at %v (%v), want the Syncer's clock %v", at, ok, now)
	}
	delete(bs.status, "PUT /project_briefs/b1")

	before := bs.calls["GET /project_briefs/b1"]
	if err := s.FetchProjectOverview(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	if got := bs.calls["GET /project_briefs/b1"]; got != before {
		t.Fatalf("overview asked for the brief while hidden (%d -> %d)", before, got)
	}

	// Past the window the overview asks again, and the answer forgets the refusal.
	later := now.Add(plan.RecheckAfter + time.Minute)
	s.now = func() time.Time { return later }
	if err := s.FetchProjectOverview(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	if got := bs.calls["GET /project_briefs/b1"]; got != before+1 {
		t.Fatalf("overview did not ask again after the window (%d -> %d)", before, got)
	}
	if at, ok, _ := st.PlanRefusedAt(ctx, plan.ProjectBrief, "w1"); ok && plan.OffAt(at, later) {
		t.Fatalf("refusal not forgotten: %v", at)
	}

	// A 402 met by the overview is recorded at the clock too.
	bs.status["GET /project_briefs/b1"] = http.StatusPaymentRequired
	if err := s.FetchProjectOverview(ctx, "p1"); err != nil {
		t.Logf("overview error (allowed): %v", err)
	}
	if at, ok, _ := st.PlanRefusedAt(ctx, plan.ProjectBrief, "w1"); !ok || !at.Equal(later) {
		t.Fatalf("overview refusal at %v (%v), want %v", at, ok, later)
	}
}
