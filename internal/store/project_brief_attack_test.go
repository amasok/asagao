package store

import (
	"errors"
	"github.com/amasok/asagao/internal/asana"
	"testing"
	"time"
)

// A failed entry stays apart from a later change. Discarding the failed one must not
// put its (older) base over the cache while the later change is still queued.
func TestBriefAttackDiscardFailedKeepsLaterEdit(t *testing.T) {
	s, ctx := briefStore(t, asanaBrief)
	now := time.Now()
	must(t, s.UpdateProjectBriefLocal(ctx, "p1", BriefEdit{Title: "目的", HTMLText: "<body>春</body>", Text: "春"}, now))
	a := briefEntries(t, s)[0]
	must(t, s.MarkOpFailed(ctx, a.ID, errors.New("500")))
	must(t, s.UpdateProjectBriefLocal(ctx, "p1", BriefEdit{Title: "目的", HTMLText: "<body>夏</body>", Text: "夏"}, now))
	if n := len(briefEntries(t, s)); n != 2 {
		t.Fatalf("entries = %d", n)
	}
	must(t, s.DiscardOp(ctx, a.ID))
	o, _ := s.ProjectOverview(ctx, "p1")
	if o.Brief == nil || o.Brief.Text != "夏" || !o.BriefPending {
		t.Fatalf("cache lost the queued change after discarding the failed one: %+v %+v", o, o.Brief)
	}
}

// Editing back to the base while a failed entry exists must not leave a ghost: the brief
// the cache shows and what will be sent must agree.
func TestBriefAttackEditAfterFailedThenBack(t *testing.T) {
	s, ctx := briefStore(t, asanaBrief)
	now := time.Now()
	must(t, s.UpdateProjectBriefLocal(ctx, "p1", BriefEdit{Title: "目的", HTMLText: "<body>春</body>", Text: "春"}, now))
	a := briefEntries(t, s)[0]
	must(t, s.MarkOpFailed(ctx, a.ID, errors.New("500")))
	must(t, s.UpdateProjectBriefLocal(ctx, "p1", BriefEdit{Title: "目的", HTMLText: "<body>年内</body>", Text: "年内"}, now))
	// the failed entry would still send 春: the cache says 年内
	for _, op := range briefEntries(t, s) {
		if op.State == OpFailed {
			continue
		}
		if op.Payload["text"] == "春" {
			t.Fatalf("pending entry carries the old change: %+v", op)
		}
	}
}

// A change made while the previous one is being sent gets its own entry, based on what
// is being sent; deleting then reverting must not lose either.
func TestBriefAttackDeleteWhileSending(t *testing.T) {
	s, ctx := briefStore(t, asanaBrief)
	now := time.Now()
	must(t, s.UpdateProjectBriefLocal(ctx, "p1", BriefEdit{Title: "目的", HTMLText: "<body>春</body>", Text: "春"}, now))
	_, err := s.db.ExecContext(ctx, "UPDATE outbox SET state='sending'")
	must(t, err)
	must(t, s.UpdateProjectBriefLocal(ctx, "p1", BriefEdit{Delete: true}, now))
	ops := briefEntries(t, s)
	if len(ops) != 2 || ops[1].Payload["delete"] != true || briefStr(ops[1].Base, "text") != "春" {
		t.Fatalf("ops = %+v", ops)
	}
	// revert the delete: back to the base of the second (春): its entry goes
	must(t, s.UpdateProjectBriefLocal(ctx, "p1", BriefEdit{Title: "目的", HTMLText: "<body>春</body>", Text: "春"}, now))
	if ops := briefEntries(t, s); len(ops) != 1 || ops[0].State != OpSending {
		t.Fatalf("ops = %+v", ops)
	}
}

// Empty body and title-only edits are queued as given; a delete of a project's brief and
// recreate with the same content is a no-op.
func TestBriefAttackDeleteThenRecreateSame(t *testing.T) {
	s, ctx := briefStore(t, asanaBrief)
	now := time.Now()
	must(t, s.UpdateProjectBriefLocal(ctx, "p1", BriefEdit{Delete: true}, now))
	must(t, s.UpdateProjectBriefLocal(ctx, "p1", BriefEdit{Title: "目的", HTMLText: "<body>年内</body>", Text: "年内"}, now))
	if ops := briefEntries(t, s); len(ops) != 0 {
		t.Fatalf("left %+v", ops)
	}
	o, _ := s.ProjectOverview(ctx, "p1")
	if o.Brief == nil || o.Brief.Text != "年内" || o.BriefPending {
		t.Fatalf("%+v", o)
	}
}

func TestBriefAttackEmptyEdit(t *testing.T) {
	s, ctx := briefStore(t, asanaBrief)
	must(t, s.UpdateProjectBriefLocal(ctx, "p1", BriefEdit{Title: "", HTMLText: "", Text: ""}, time.Now()))
	o, _ := s.ProjectOverview(ctx, "p1")
	if o.Brief == nil {
		t.Fatalf("an empty edit made the brief vanish: %+v", o)
	}
}

// Editing after a delete (queued) edits the (absent) brief: it is a create again, one entry.
func TestBriefAttackEditAfterDelete(t *testing.T) {
	s, ctx := briefStore(t, asanaBrief)
	now := time.Now()
	must(t, s.UpdateProjectBriefLocal(ctx, "p1", BriefEdit{Delete: true}, now))
	must(t, s.UpdateProjectBriefLocal(ctx, "p1", BriefEdit{Title: "新", HTMLText: "<body>x</body>", Text: "x"}, now))
	ops := briefEntries(t, s)
	if len(ops) != 1 || ops[0].Payload["delete"] == true || briefStr(ops[0].Base, "text") != "年内" {
		t.Fatalf("%+v", ops)
	}
}

// Control characters from Asana never reach the cache's readers.
func TestBriefAttackTermsafeCache(t *testing.T) {
	s, ctx := briefStore(t, asanaBrief)
	must(t, s.SaveProjectOverview(ctx, "p1", nil, BriefAvailable, &asanaBriefEsc, time.Now()))
	o, _ := s.ProjectOverview(ctx, "p1")
	for _, v := range []string{o.Brief.Title, o.Brief.Text} {
		for _, r := range v {
			if r == 0x1b || r == 0x07 || r == 0x9b {
				t.Fatalf("control char in %q", v)
			}
		}
	}
}

var asanaBriefEsc = asana.ProjectBrief{GID: "b1", Title: "t\x1b[31m", Text: "x\x07\u009b", HTMLText: "<body>x</body>"}

// Partial payloads: title then body edits fold; changing one back leaves the other;
// both back removes the entry; the cache always shows the whole brief.
func TestBriefAttackPartialFold(t *testing.T) {
	s, ctx := briefStore(t, asanaBrief)
	now := time.Now()
	must(t, s.UpdateProjectBriefLocal(ctx, "p1", BriefEdit{Title: "新", HTMLText: "<body>年内</body>", Text: "年内"}, now))
	op := briefEntries(t, s)[0]
	if _, has := op.Payload["html_text"]; has || op.Payload["title"] != "新" {
		t.Fatalf("payload = %v", op.Payload)
	}
	must(t, s.UpdateProjectBriefLocal(ctx, "p1", BriefEdit{Title: "新", HTMLText: "<body>春</body>", Text: "春"}, now))
	op = briefEntries(t, s)[0]
	if op.Payload["title"] != "新" || op.Payload["text"] != "春" {
		t.Fatalf("payload = %v", op.Payload)
	}
	must(t, s.UpdateProjectBriefLocal(ctx, "p1", BriefEdit{Title: "目的", HTMLText: "<body>春</body>", Text: "春"}, now))
	op = briefEntries(t, s)[0]
	if _, has := op.Payload["title"]; has {
		t.Fatalf("title kept: %v", op.Payload)
	}
	o, _ := s.ProjectOverview(ctx, "p1")
	if o.Brief.Title != "目的" || o.Brief.Text != "春" {
		t.Fatalf("cache = %+v", o.Brief)
	}
	must(t, s.UpdateProjectBriefLocal(ctx, "p1", BriefEdit{Title: "目的", HTMLText: "<body>年内</body>", Text: "年内"}, now))
	if n := len(briefEntries(t, s)); n != 0 {
		t.Fatalf("entries %d", n)
	}
}

// Re-applying (the overview fetched again) a title-only change keeps Asana's body.
func TestBriefAttackReapplyPartial(t *testing.T) {
	s, ctx := briefStore(t, asanaBrief)
	now := time.Now()
	must(t, s.UpdateProjectBriefLocal(ctx, "p1", BriefEdit{Title: "新", HTMLText: "<body>年内</body>", Text: "年内"}, now))
	must(t, s.SaveProjectOverview(ctx, "p1", nil, BriefAvailable,
		&asana.ProjectBrief{GID: "b1", Title: "目的", Text: "夏", HTMLText: "<body>夏</body>"}, now))
	o, _ := s.ProjectOverview(ctx, "p1")
	if o.Brief.Title != "新" || o.Brief.Text != "夏" || !o.BriefPending {
		t.Fatalf("cache = %+v pending=%v", o.Brief, o.BriefPending)
	}
}
