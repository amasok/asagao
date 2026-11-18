package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
)

func briefStore(t *testing.T, brief *asana.ProjectBrief) (*Store, context.Context) {
	t.Helper()
	s := newTestStore(t)
	ctx := context.Background()
	must(t, s.UpsertProjects(ctx, "w1", []asana.Project{{GID: "p1", Name: "新居"}}, "me"))
	state := BriefNone
	if brief != nil {
		state = BriefAvailable
	}
	must(t, s.SaveProjectOverview(ctx, "p1", nil, state, brief, time.Now()))
	return s, ctx
}

func briefEntries(t *testing.T, s *Store) []Op {
	t.Helper()
	ops, err := s.ListOps(context.Background())
	must(t, err)
	return ops
}

var asanaBrief = &asana.ProjectBrief{GID: "b1", Title: "目的", Text: "年内", HTMLText: "<body>年内</body>"}

// Consecutive changes are one entry whose base is the brief before the first; changing it
// back removes the entry and shows the brief as it was (#60).
func TestBriefEditsFoldAndRevert(t *testing.T) {
	s, ctx := briefStore(t, asanaBrief)
	now := time.Now()
	must(t, s.UpdateProjectBriefLocal(ctx, "p1", BriefEdit{Title: "目的", HTMLText: "<body>春</body>", Text: "春"}, now))
	must(t, s.UpdateProjectBriefLocal(ctx, "p1", BriefEdit{Title: "目的2", HTMLText: "<body>夏</body>", Text: "夏"}, now))
	ops := briefEntries(t, s)
	if len(ops) != 1 || ops[0].Base["text"] != "年内" || ops[0].Payload["text"] != "夏" || ops[0].TaskName != "新居" {
		t.Fatalf("ops = %+v", ops)
	}
	must(t, s.UpdateProjectBriefLocal(ctx, "p1", BriefEdit{Title: "目的", HTMLText: "<body>年内</body>", Text: "年内"}, now))
	if ops := briefEntries(t, s); len(ops) != 0 {
		t.Fatalf("reverted, left %+v", ops)
	}
	if o, _ := s.ProjectOverview(ctx, "p1"); o.Brief == nil || o.Brief.Text != "年内" || o.BriefPending {
		t.Fatalf("overview = %+v", o)
	}
}

// Deleting a brief made here (not sent) removes the entry: nothing to send.
func TestBriefMadeHereDeleted(t *testing.T) {
	s, ctx := briefStore(t, nil)
	now := time.Now()
	must(t, s.UpdateProjectBriefLocal(ctx, "p1", BriefEdit{Title: "t", HTMLText: "<body>x</body>", Text: "x"}, now))
	must(t, s.UpdateProjectBriefLocal(ctx, "p1", BriefEdit{Delete: true}, now))
	if ops := briefEntries(t, s); len(ops) != 0 {
		t.Fatalf("left %+v", ops)
	}
	if o, _ := s.ProjectOverview(ctx, "p1"); o.Brief != nil || o.BriefState != BriefNone {
		t.Fatalf("overview = %+v", o)
	}
	// and there is nothing to delete now
	if err := s.UpdateProjectBriefLocal(ctx, "p1", BriefEdit{Delete: true}, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete of none: %v", err)
	}
}

// Discarding a change puts the brief back as it was.
func TestBriefDiscardRestores(t *testing.T) {
	s, ctx := briefStore(t, asanaBrief)
	must(t, s.UpdateProjectBriefLocal(ctx, "p1", BriefEdit{Delete: true}, time.Now()))
	ops := briefEntries(t, s)
	must(t, s.DiscardOp(ctx, ops[0].ID))
	o, _ := s.ProjectOverview(ctx, "p1")
	if o.Brief == nil || o.Brief.Title != "目的" || o.Brief.HTMLText != "<body>年内</body>" || o.BriefPending {
		t.Fatalf("overview = %+v", o)
	}
}

// A brief Asana does not give cannot be changed; nor one of a project not in the cache.
func TestBriefUnavailableRefused(t *testing.T) {
	s, ctx := briefStore(t, nil)
	must(t, s.SaveProjectOverview(ctx, "p1", nil, BriefUnavailable, nil, time.Now()))
	if err := s.UpdateProjectBriefLocal(ctx, "p1", BriefEdit{Title: "t"}, time.Now()); !errors.Is(err, ErrBriefUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if err := s.UpdateProjectBriefLocal(ctx, "nope", BriefEdit{Title: "t"}, time.Now()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	if ops := briefEntries(t, s); len(ops) != 0 {
		t.Fatalf("queued %+v", ops)
	}
}

// Completing a sent change stores Asana's brief, but keeps a newer change queued meanwhile.
func TestCompleteBriefKeepsNewerChange(t *testing.T) {
	s, ctx := briefStore(t, asanaBrief)
	now := time.Now()
	must(t, s.UpdateProjectBriefLocal(ctx, "p1", BriefEdit{Title: "目的", HTMLText: "<body>春</body>", Text: "春"}, now))
	first := briefEntries(t, s)[0]
	_, err := s.db.ExecContext(ctx, "UPDATE outbox SET state = 'sending' WHERE id = ?", first.ID)
	must(t, err)
	must(t, s.UpdateProjectBriefLocal(ctx, "p1", BriefEdit{Title: "目的", HTMLText: "<body>夏</body>", Text: "夏"}, now))
	if ops := briefEntries(t, s); len(ops) != 2 {
		t.Fatalf("a change while sending folded into it: %+v", ops)
	}
	must(t, s.CompleteBriefOp(ctx, first.ID, "p1", &asana.ProjectBrief{GID: "b1", Title: "目的", Text: "春", HTMLText: "<body>春</body>"}))
	if o, _ := s.ProjectOverview(ctx, "p1"); o.Brief == nil || o.Brief.Text != "夏" || !o.BriefPending {
		t.Fatalf("overview = %+v", o)
	}
}

// Discarding a failed change keeps a later one queued for the brief in view (#60, found
// by the tester): the brief shown is the later change, not the base of the failed one.
func TestBriefDiscardKeepsLaterChange(t *testing.T) {
	s, ctx := briefStore(t, asanaBrief)
	now := time.Now()
	must(t, s.UpdateProjectBriefLocal(ctx, "p1", BriefEdit{Title: "目的", HTMLText: "<body>A</body>", Text: "A"}, now))
	a := briefEntries(t, s)[0]
	must(t, s.MarkOpFailed(ctx, a.ID, errors.New("402")))
	must(t, s.UpdateProjectBriefLocal(ctx, "p1", BriefEdit{Title: "目的", HTMLText: "<body>B</body>", Text: "B"}, now))
	if n := len(briefEntries(t, s)); n != 2 {
		t.Fatalf("entries = %d", n)
	}
	must(t, s.DiscardOp(ctx, a.ID))
	if o, _ := s.ProjectOverview(ctx, "p1"); o.Brief == nil || o.Brief.Text != "B" || !o.BriefPending {
		t.Fatalf("overview = %+v", o)
	}
}

// "Keep mine" on a brief change in conflict shows Asana's brief with the change over it,
// and for a brief Asana deleted meanwhile, queues the whole brief to make it again (a
// change of the body only would make one without its title).
func TestBriefKeepLocal(t *testing.T) {
	s, ctx := briefStore(t, asanaBrief)
	now := time.Now()
	must(t, s.UpdateProjectBriefLocal(ctx, "p1", BriefEdit{Title: "目的", HTMLText: "<body>春</body>", Text: "春"}, now))
	op := briefEntries(t, s)[0]
	must(t, s.MarkOpConflict(ctx, op.ID, map[string]any{"state": BriefAvailable, "title": "あちら", "text": "夏", "html_text": "<body>夏</body>"}))
	must(t, s.ResolveKeepLocal(ctx, op.ID))
	if o, _ := s.ProjectOverview(ctx, "p1"); o.Brief == nil || o.Brief.Title != "あちら" || o.Brief.Text != "春" {
		t.Fatalf("shown = %+v", o.Brief)
	}

	s2, ctx := briefStore(t, asanaBrief)
	must(t, s2.UpdateProjectBriefLocal(ctx, "p1", BriefEdit{Title: "目的", HTMLText: "<body>春</body>", Text: "春"}, now))
	op = briefEntries(t, s2)[0]
	must(t, s2.MarkOpConflict(ctx, op.ID, map[string]any{"state": BriefNone, "title": "", "text": "", "html_text": ""}))
	must(t, s2.ResolveKeepLocal(ctx, op.ID))
	op = briefEntries(t, s2)[0]
	if op.Payload["title"] != "目的" || op.Payload["html_text"] != "<body>春</body>" || op.Base["state"] != BriefNone {
		t.Fatalf("op = %+v", op)
	}
}

// Discarding a change in conflict ("keep Asana's") shows Asana's brief as the conflict
// found it, so that the next change is made from it (found on the real Asana, #60: a
// delete after that was a conflict again, its base the old brief).
func TestBriefDiscardConflictShowsAsana(t *testing.T) {
	s, ctx := briefStore(t, asanaBrief)
	now := time.Now()
	must(t, s.UpdateProjectBriefLocal(ctx, "p1", BriefEdit{Title: "目的", HTMLText: "<body>春</body>", Text: "春"}, now))
	op := briefEntries(t, s)[0]
	must(t, s.MarkOpConflict(ctx, op.ID, map[string]any{"state": BriefAvailable, "title": "目的", "text": "夏", "html_text": "<body>夏</body>"}))
	must(t, s.DiscardOp(ctx, op.ID))
	if o, _ := s.ProjectOverview(ctx, "p1"); o.Brief == nil || o.Brief.Text != "夏" || o.BriefPending {
		t.Fatalf("shown = %+v", o.Brief)
	}
	must(t, s.UpdateProjectBriefLocal(ctx, "p1", BriefEdit{Delete: true}, now))
	if op := briefEntries(t, s)[0]; op.Base["text"] != "夏" {
		t.Fatalf("base = %+v", op.Base)
	}
}
