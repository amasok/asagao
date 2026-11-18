package sync

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/store"
)

// A title-only change (same plain text) is sent.
func TestBriefAttackTitleOnlySent(t *testing.T) {
	ctx := context.Background()
	bs, s, st := setupBrief(t, "目的", "年内")
	if err := st.UpdateProjectBriefLocal(ctx, "p1", edit("新題", "年内"), now); err != nil {
		t.Fatal(err)
	}
	if res, err := s.Push(ctx); err != nil || res.Sent != 1 || bs.title != "新題" {
		t.Fatalf("%+v %v title=%q", res, err, bs.title)
	}
}

// A change of the HTML only (a table cell's formatting, same text) is sent.
func TestBriefAttackHTMLOnlySent(t *testing.T) {
	ctx := context.Background()
	bs, s, st := setupBrief(t, "目的", "年内")
	e := store.BriefEdit{Title: "目的", HTMLText: "<body><strong>年内</strong></body>", Text: "年内"}
	if err := st.UpdateProjectBriefLocal(ctx, "p1", e, now); err != nil {
		t.Fatal(err)
	}
	if res, err := s.Push(ctx); err != nil || res.Sent != 1 || bs.html != e.HTMLText {
		t.Fatalf("%+v %v html=%q", res, err, bs.html)
	}
}

// DELETE answering 404 is done (deleted meanwhile), not a failure.
func TestBriefAttackDelete404(t *testing.T) {
	ctx := context.Background()
	bs, s, st := setupBrief(t, "目的", "年内")
	if err := st.UpdateProjectBriefLocal(ctx, "p1", store.BriefEdit{Delete: true}, now); err != nil {
		t.Fatal(err)
	}
	bs.status["DELETE /project_briefs/b1"] = http.StatusNotFound
	res, err := s.Push(ctx)
	if err != nil || res.Sent != 1 || res.Failed != 0 || len(briefOps(t, st)) != 0 {
		t.Fatalf("%+v %v %+v", res, err, briefOps(t, st))
	}
	if o, _ := st.ProjectOverview(ctx, "p1"); o.Brief != nil {
		t.Fatalf("cache still has the brief: %+v", o.Brief)
	}
}

// PUT answering 404 is a conflict (deleted there), and keeping mine makes it again.
func TestBriefAttackPut404(t *testing.T) {
	ctx := context.Background()
	bs, s, st := setupBrief(t, "目的", "年内")
	if err := st.UpdateProjectBriefLocal(ctx, "p1", edit("目的", "春"), now); err != nil {
		t.Fatal(err)
	}
	bs.status["PUT /project_briefs/b1"] = http.StatusNotFound
	res, err := s.Push(ctx)
	if err != nil || res.Conflicts != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	ops := briefOps(t, st)
	if len(ops) != 1 || ops[0].State != store.OpConflict {
		t.Fatalf("%+v", ops)
	}
}

// Delete: someone changed the brief there meanwhile → conflict, not deleted.
func TestBriefAttackDeleteConflictsWithServerChange(t *testing.T) {
	ctx := context.Background()
	bs, s, st := setupBrief(t, "目的", "年内")
	if err := st.UpdateProjectBriefLocal(ctx, "p1", store.BriefEdit{Delete: true}, now); err != nil {
		t.Fatal(err)
	}
	bs.text, bs.html = "夏", "<body>夏</body>"
	res, err := s.Push(ctx)
	if err != nil || res.Conflicts != 1 || bs.calls["DELETE /project_briefs/b1"] != 0 {
		t.Fatalf("%+v %v", res, err)
	}
}

// A conflict in Asana's text with control characters shows none.
func TestBriefAttackConflictLinesTermsafe(t *testing.T) {
	ctx := context.Background()
	bs, s, st := setupBrief(t, "目的", "年内")
	if err := st.UpdateProjectBriefLocal(ctx, "p1", edit("目的", "春"), now); err != nil {
		t.Fatal(err)
	}
	bs.title, bs.text, bs.html = "T\x1b]0;pwn\x07", "夏\x1b[2J\u009b", "<body>x</body>"
	if _, err := s.Push(ctx); err != nil {
		t.Fatal(err)
	}
	ops := briefOps(t, st)
	for _, l := range readmodel.ConflictLines(ops[0]) {
		if strings.ContainsAny(l, "\x1b\x07\u009b") {
			t.Fatalf("control char in %q", l)
		}
	}
	for _, v := range ops[0].Conflict {
		if str, _ := v.(string); strings.ContainsAny(str, "\x1b\x07\u009b") {
			t.Fatalf("control char kept in conflict: %q", str)
		}
	}
}

// 402 on the first GET (not a write) also marks the workspace only, and a later success
// clears it. (A 403 marks the project only: TestPushBriefForbiddenIsThisProjectOnly.)
func TestBriefAttackRefusalOnGetAndClear(t *testing.T) {
	ctx := context.Background()
	bs, s, st := setupBrief(t, "目的", "年内")
	if err := st.UpdateProjectBriefLocal(ctx, "p1", edit("目的", "春"), now); err != nil {
		t.Fatal(err)
	}
	bs.status["GET /project_briefs/b1"] = http.StatusPaymentRequired
	if res, err := s.Push(ctx); err != nil || res.Failed != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if v, _ := st.SyncState(ctx, readmodel.PlanKey(readmodel.PlanProjectBrief, "w1")); !readmodel.PlanOffFor(v, now) {
		t.Fatalf("not marked: %q", v)
	}
	if v, _ := st.SyncState(ctx, readmodel.PlanKey(readmodel.PlanProjectBrief, "w2")); v != "" {
		t.Fatalf("w2 marked: %q", v)
	}
	delete(bs.status, "GET /project_briefs/b1")
	ops := briefOps(t, st)
	if err := st.RetryOp(ctx, ops[0].ID); err != nil {
		t.Fatal(err)
	}
	if res, err := s.Push(ctx); err != nil || res.Sent != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if v, _ := st.SyncState(ctx, readmodel.PlanKey(readmodel.PlanProjectBrief, "w1")); readmodel.PlanOffFor(v, now) {
		t.Fatalf("mark not cleared: %q", v)
	}
}

// 500 and 429 are failures without marking the workspace.
func TestBriefAttackServerErrorsDoNotMark(t *testing.T) {
	for _, code := range []int{500, 429, 404} {
		ctx := context.Background()
		bs, s, st := setupBrief(t, "目的", "年内")
		if err := st.UpdateProjectBriefLocal(ctx, "p1", edit("目的", "春"), now); err != nil {
			t.Fatal(err)
		}
		bs.status["GET /project_briefs/b1"] = code
		_, _ = s.Push(ctx)
		if v, _ := st.SyncState(ctx, readmodel.PlanKey(readmodel.PlanProjectBrief, "w1")); readmodel.PlanOffFor(v, now) {
			t.Fatalf("%d marked the workspace", code)
		}
	}
}

// A create that is refused (POST 403) fails; nothing is created twice on retry.
func TestBriefAttackCreateRetryNoDouble(t *testing.T) {
	ctx := context.Background()
	bs, s, st := setupBrief(t, "", "")
	if err := st.UpdateProjectBriefLocal(ctx, "p1", edit("目的", "年内"), now); err != nil {
		t.Fatal(err)
	}
	bs.status["POST /projects/p1/project_briefs"] = 500
	_, _ = s.Push(ctx)
	delete(bs.status, "POST /projects/p1/project_briefs")
	for _, o := range briefOps(t, st) {
		_ = st.RetryOp(ctx, o.ID)
	}
	for i := 0; i < 3; i++ {
		if _, err := s.Push(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := bs.nextGID; n != 2 { // started at 1: exactly one brief made
		t.Fatalf("briefs made: %d", n-1)
	}
}

// Two pushes at once send one change once.
func TestBriefAttackConcurrentPush(t *testing.T) {
	ctx := context.Background()
	bs, s, st := setupBrief(t, "目的", "年内")
	if err := st.UpdateProjectBriefLocal(ctx, "p1", edit("目的", "春"), now); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{}, 4)
	for i := 0; i < 4; i++ {
		go func() { _, _ = s.Push(ctx); done <- struct{}{} }()
	}
	for i := 0; i < 4; i++ {
		<-done
	}
	bs.mu.Lock()
	defer bs.mu.Unlock()
	if n := bs.calls["PUT /project_briefs/b1"]; n != 1 {
		t.Fatalf("PUT %d times", n)
	}
	if bs.text != "春" {
		t.Fatalf("text %q", bs.text)
	}
}

// A failed entry then a later edit: pushes in order, the latest wins on Asana.
func TestBriefAttackFailedThenEditLatestWins(t *testing.T) {
	ctx := context.Background()
	bs, s, st := setupBrief(t, "目的", "年内")
	if err := st.UpdateProjectBriefLocal(ctx, "p1", edit("目的", "春"), now); err != nil {
		t.Fatal(err)
	}
	bs.status["PUT /project_briefs/b1"] = 500
	_, _ = s.Push(ctx)
	delete(bs.status, "PUT /project_briefs/b1")
	if err := st.UpdateProjectBriefLocal(ctx, "p1", edit("目的", "夏"), now); err != nil {
		t.Fatal(err)
	}
	for _, o := range briefOps(t, st) {
		if o.State == store.OpFailed {
			_ = st.RetryOp(ctx, o.ID)
		}
	}
	res, err := s.Push(ctx)
	if err != nil || res.Conflicts != 0 || bs.text != "夏" {
		t.Fatalf("%+v %v text=%q ops=%+v", res, err, bs.text, briefOps(t, st))
	}
}

// A very long body goes through whole.
func TestBriefAttackHugeBody(t *testing.T) {
	ctx := context.Background()
	bs, s, st := setupBrief(t, "目的", "年内")
	big := strings.Repeat("あ", 300000)
	if err := st.UpdateProjectBriefLocal(ctx, "p1", edit("目的", big), now); err != nil {
		t.Fatal(err)
	}
	if res, err := s.Push(ctx); err != nil || res.Sent != 1 || bs.text != big {
		t.Fatalf("%+v %v len=%d", res, err, len(bs.text))
	}
}
