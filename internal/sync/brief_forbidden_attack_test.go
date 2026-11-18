package sync

import (
	"context"
	"net/http"
	"testing"

	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/store"
)

// 403 makes this project's brief unavailable, marks no workspace (also not the old
// global key), keeps the project's other overview data, and leaves a failed entry that
// can be retried (403 again, no crash) or discarded (the brief is back as it was).
func TestBriefAttack403ThisProjectOnly(t *testing.T) {
	ctx := context.Background()
	bs, s, st := setupBrief(t, "目的", "年内")
	if err := st.SaveProjectOverview(ctx, "p1", nil, "", nil, now); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateProjectBriefLocal(ctx, "p1", edit("目的", "春"), now); err != nil {
		t.Fatal(err)
	}
	bs.status["PUT /project_briefs/b1"] = http.StatusForbidden
	res, err := s.Push(ctx)
	if err != nil || res.Failed != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	for _, k := range []string{readmodel.PlanKey(readmodel.PlanProjectBrief, "w1"), readmodel.KeyPlanUnavailable + readmodel.PlanProjectBrief} {
		if v, _ := st.SyncState(ctx, k); v != "" {
			t.Fatalf("%s marked: %q", k, v)
		}
	}
	if hidden, _ := s.briefHidden(ctx, "p1"); hidden {
		t.Fatal("workspace hidden by a 403")
	}
	o, _ := st.ProjectOverview(ctx, "p1")
	if o.BriefState != store.BriefUnavailable {
		t.Fatalf("state = %q", o.BriefState)
	}
	if err := st.UpdateProjectBriefLocal(ctx, "p1", edit("目的", "夏"), now); err == nil {
		t.Fatal("edit of an unavailable brief accepted")
	}
	ops := briefOps(t, st)
	if len(ops) != 1 || ops[0].State != store.OpFailed || ops[0].LastError == "" {
		t.Fatalf("ops = %+v", ops)
	}
	// retry: refused again, still one failed entry
	if err := st.RetryOp(ctx, ops[0].ID); err != nil {
		t.Fatal(err)
	}
	if res, err := s.Push(ctx); err != nil || res.Failed != 1 {
		t.Fatalf("retry: %+v %v", res, err)
	}
	// the overview opened again asks again; Asana now lets us: the brief is back
	delete(bs.status, "PUT /project_briefs/b1")
	if err := s.FetchProjectOverview(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	o, _ = st.ProjectOverview(ctx, "p1")
	if o.BriefState != store.BriefAvailable {
		t.Fatalf("not back: %q", o.BriefState)
	}
	// discard of the failed entry: the cache is a consistent brief
	ops = briefOps(t, st)
	if err := st.DiscardOp(ctx, ops[0].ID); err != nil {
		t.Fatal(err)
	}
	o, _ = st.ProjectOverview(ctx, "p1")
	if o.Brief == nil || o.Brief.Text != "年内" || o.BriefPending {
		t.Fatalf("after discard: %+v", o)
	}
}

// 402 hides the workspace, not a global key, and a later success clears it.
func TestBriefAttack402WorkspaceKey(t *testing.T) {
	ctx := context.Background()
	bs, s, st := setupBrief(t, "目的", "年内")
	if err := st.UpdateProjectBriefLocal(ctx, "p1", edit("目的", "春"), now); err != nil {
		t.Fatal(err)
	}
	bs.status["PUT /project_briefs/b1"] = http.StatusPaymentRequired
	if res, err := s.Push(ctx); err != nil || res.Failed != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if v, _ := st.SyncState(ctx, readmodel.PlanKey(readmodel.PlanProjectBrief, "w1")); !readmodel.PlanOffFor(v, now) {
		t.Fatalf("w1 not marked: %q", v)
	}
	if v, _ := st.SyncState(ctx, readmodel.PlanKey(readmodel.PlanProjectBrief, "w2")); v != "" {
		t.Fatalf("w2 marked")
	}
	if v, _ := st.SyncState(ctx, readmodel.KeyPlanUnavailable+readmodel.PlanProjectBrief); v != "" {
		t.Fatalf("global key marked")
	}
}
