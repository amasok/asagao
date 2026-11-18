package sync

import (
	"context"
	"net/http"
	"testing"

	"github.com/amasok/asagao/internal/store"
)

func putBodies(bs *briefServer) []map[string]any {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	return bs.bodies["PUT /project_briefs/b1"]
}

// Title changed here, body changed there: both kept, only the title is sent.
func TestBriefAttackTitleHereBodyThere(t *testing.T) {
	ctx := context.Background()
	bs, s, st := setupBrief(t, "目的", "年内")
	if err := st.UpdateProjectBriefLocal(ctx, "p1", edit("新題", "年内"), now); err != nil {
		t.Fatal(err)
	}
	bs.text, bs.html = "夏", "<body>夏</body>"
	res, err := s.Push(ctx)
	if err != nil || res.Sent != 1 || res.Conflicts != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	b := putBodies(bs)
	if len(b) != 1 {
		t.Fatalf("puts = %v", b)
	}
	if _, has := b[0]["html_text"]; has {
		t.Fatalf("body sent too: %v", b[0])
	}
	if bs.title != "新題" || bs.text != "夏" {
		t.Fatalf("title=%q text=%q", bs.title, bs.text)
	}
}

// Body changed here, title changed there: both kept, only the body is sent.
func TestBriefAttackBodyHereTitleThere(t *testing.T) {
	ctx := context.Background()
	bs, s, st := setupBrief(t, "目的", "年内")
	if err := st.UpdateProjectBriefLocal(ctx, "p1", edit("目的", "春"), now); err != nil {
		t.Fatal(err)
	}
	bs.title = "あちらの題"
	res, err := s.Push(ctx)
	if err != nil || res.Sent != 1 || res.Conflicts != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if b := putBodies(bs); len(b) != 1 || b[0]["title"] != nil {
		t.Fatalf("puts = %v", b)
	}
	if bs.title != "あちらの題" || bs.text != "春" {
		t.Fatalf("title=%q text=%q", bs.title, bs.text)
	}
}

// Same part changed on both sides: conflict, nothing sent.
func TestBriefAttackSamePartBothSides(t *testing.T) {
	for _, part := range []string{"title", "body"} {
		ctx := context.Background()
		bs, s, st := setupBrief(t, "目的", "年内")
		e := edit("目的", "年内")
		if part == "title" {
			e.Title = "こちら"
			bs.title = "あちら"
		} else {
			e = edit("目的", "こちら")
			bs.text, bs.html = "あちら", "<body>あちら</body>"
		}
		if err := st.UpdateProjectBriefLocal(ctx, "p1", e, now); err != nil {
			t.Fatal(err)
		}
		res, err := s.Push(ctx)
		if err != nil || res.Conflicts != 1 || len(putBodies(bs)) != 0 {
			t.Fatalf("%s: %+v %v", part, res, err)
		}
	}
}

// A delete conflicts with any change there (title only too).
func TestBriefAttackDeleteVsTitleThere(t *testing.T) {
	ctx := context.Background()
	bs, s, st := setupBrief(t, "目的", "年内")
	if err := st.UpdateProjectBriefLocal(ctx, "p1", store.BriefEdit{Delete: true}, now); err != nil {
		t.Fatal(err)
	}
	bs.title = "あちら"
	res, err := s.Push(ctx)
	if err != nil || res.Conflicts != 1 || bs.calls["DELETE /project_briefs/b1"] != 0 {
		t.Fatalf("%+v %v", res, err)
	}
}

// The 403 Asana gives for a brief that is gone: PUT → conflict, DELETE → done. The same
// 403 while the brief is still there is a refusal (this project's brief unavailable).
func TestBriefAttackGone403(t *testing.T) {
	ctx := context.Background()
	// PUT, brief deleted there meanwhile (server answers 403 for the dead gid)
	bs, s, st := setupBrief(t, "目的", "年内")
	if err := st.UpdateProjectBriefLocal(ctx, "p1", edit("目的", "春"), now); err != nil {
		t.Fatal(err)
	}
	bs.gid, bs.title, bs.text, bs.html = "", "", "", ""
	if res, err := s.Push(ctx); err != nil || res.Conflicts != 1 {
		t.Fatalf("put gone: %+v %v", res, err)
	}
	// refusal with the brief still there
	bs2, s2, st2 := setupBrief(t, "目的", "年内")
	if err := st2.UpdateProjectBriefLocal(ctx, "p1", edit("目的", "春"), now); err != nil {
		t.Fatal(err)
	}
	bs2.status["PUT /project_briefs/b1"] = http.StatusForbidden
	if res, err := s2.Push(ctx); err != nil || res.Failed != 1 || res.Conflicts != 0 {
		t.Fatalf("refused: %+v %v", res, err)
	}
	if o, _ := st2.ProjectOverview(ctx, "p1"); o.BriefState != store.BriefUnavailable {
		t.Fatalf("state %q", o.BriefState)
	}
	// DELETE refused with the brief still there stays a failure
	bs3, s3, st3 := setupBrief(t, "目的", "年内")
	if err := st3.UpdateProjectBriefLocal(ctx, "p1", store.BriefEdit{Delete: true}, now); err != nil {
		t.Fatal(err)
	}
	bs3.status["DELETE /project_briefs/b1"] = http.StatusForbidden
	if res, err := s3.Push(ctx); err != nil || res.Failed != 1 || res.Sent != 0 {
		t.Fatalf("delete refused: %+v %v", res, err)
	}
}

// After keep_local on a body conflict, a title changed there is not overwritten by a
// later edit of the body here.
func TestBriefAttackKeepLocalThenEditKeepsServerTitle(t *testing.T) {
	ctx := context.Background()
	bs, s, st := setupBrief(t, "目的", "年内")
	if err := st.UpdateProjectBriefLocal(ctx, "p1", edit("目的", "春"), now); err != nil {
		t.Fatal(err)
	}
	bs.title, bs.text, bs.html = "あちら", "夏", "<body>夏</body>"
	if res, err := s.Push(ctx); err != nil || res.Conflicts != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	ops := briefOps(t, st)
	if err := st.ResolveKeepLocal(ctx, ops[0].ID); err != nil {
		t.Fatal(err)
	}
	// the brief shown is Asana's with the change over it: the next edit (as app.Briefs makes
	// it, from what is shown) keeps Asana's title
	o, _ := st.ProjectOverview(ctx, "p1")
	if o.Brief == nil || o.Brief.Title != "あちら" || o.Brief.Text != "春" {
		t.Fatalf("shown after keep_local: %+v", o.Brief)
	}
	if err := st.UpdateProjectBriefLocal(ctx, "p1", edit(o.Brief.Title, "秋"), now); err != nil {
		t.Fatal(err)
	}
	if ops := briefOps(t, st); ops[0].Payload["title"] != nil {
		t.Fatalf("the title is queued: %+v", ops[0].Payload)
	}
	if res, err := s.Push(ctx); err != nil || res.Sent != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if bs.title != "あちら" || bs.text != "秋" {
		t.Fatalf("server title overwritten: title=%q text=%q", bs.title, bs.text)
	}
}

// Discarding a conflict shows Asana's brief (what the conflict found), also when Asana
// deleted it, and a later edit is a plain one again.
func TestBriefAttackDiscardConflictShowsAsana(t *testing.T) {
	ctx := context.Background()
	bs, s, st := setupBrief(t, "目的", "年内")
	if err := st.UpdateProjectBriefLocal(ctx, "p1", edit("目的", "春"), now); err != nil {
		t.Fatal(err)
	}
	bs.text, bs.html = "夏", "<body>夏</body>"
	if res, err := s.Push(ctx); err != nil || res.Conflicts != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if err := st.DiscardOp(ctx, briefOps(t, st)[0].ID); err != nil {
		t.Fatal(err)
	}
	o, _ := st.ProjectOverview(ctx, "p1")
	if o.Brief == nil || o.Brief.Text != "夏" || o.BriefPending {
		t.Fatalf("after discard: %+v", o)
	}
	// deleted there
	if err := st.UpdateProjectBriefLocal(ctx, "p1", edit("目的", "冬"), now); err != nil {
		t.Fatal(err)
	}
	bs.gid, bs.title, bs.text, bs.html = "", "", "", ""
	if res, err := s.Push(ctx); err != nil || res.Conflicts != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if err := st.DiscardOp(ctx, briefOps(t, st)[0].ID); err != nil {
		t.Fatal(err)
	}
	o, _ = st.ProjectOverview(ctx, "p1")
	if o.Brief != nil || o.BriefState != store.BriefNone {
		t.Fatalf("deleted there, discard: %+v", o)
	}
}
