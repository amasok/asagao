package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/amasok/asagao/internal/asana"
)

func sectionGIDs(t *testing.T, s *Store, project string) []string {
	t.Helper()
	var out []string
	err := s.tx(context.Background(), func(tx *sql.Tx) error {
		var err error
		out, err = sectionOrder(context.Background(), tx, project)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A task created offline into a section created offline: once the section is created in
// Asana, the task's create names the real section; if the section is discarded instead,
// the task stays (in no section) and its create no longer names it.
func TestLocalSectionInTaskCreate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.ReplaceSections(ctx, "p1", []asana.Section{{GID: "s1", Name: "家具"}}); err != nil {
		t.Fatal(err)
	}
	sec, err := s.CreateSectionLocal(ctx, "p1", "保留", "s1", "", testNow)
	if err != nil {
		t.Fatal(err)
	}
	if got := sectionGIDs(t, s, "p1"); len(got) != 2 || got[0] != sec {
		t.Fatalf("order = %v, want %s first", got, sec)
	}
	task, err := s.CreateTaskLocal(ctx, NewTask{ProjectGID: "p1", SectionGID: sec, Name: "x"}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteCreateSection(ctx, firstOp(t, s, OpCreateSection).ID, sec, "s9"); err != nil {
		t.Fatal(err)
	}
	create := firstOp(t, s, OpCreateTask)
	if create.Payload["section"] != "s9" {
		t.Errorf("create payload = %v, want section s9", create.Payload)
	}
	if d, _ := s.GetTask(ctx, task); d.Memberships[0].SectionGID != "s9" {
		t.Errorf("task is in %q", d.Memberships[0].SectionGID)
	}
	if real, _ := s.RealSectionGID(ctx, sec); real != "s9" {
		t.Errorf("RealSectionGID = %q", real)
	}

	sec2, _ := s.CreateSectionLocal(ctx, "p1", "後で", "", "", testNow)
	task2, _ := s.CreateTaskLocal(ctx, NewTask{ProjectGID: "p1", SectionGID: sec2, Name: "y"}, testNow)
	if err := s.DiscardOp(ctx, firstOp(t, s, OpCreateSection).ID); err != nil {
		t.Fatal(err)
	}
	d, err := s.GetTask(ctx, task2)
	if err != nil || d.Memberships[0].SectionGID != "" {
		t.Fatalf("task2 = %+v, %v; want it kept in no section", d, err)
	}
	ops, _ := s.ListOps(ctx)
	for _, op := range ops {
		if op.TaskGID == task2 && op.Payload["section"] != nil {
			t.Errorf("task2's create still names the discarded section: %v", op.Payload)
		}
	}
}

func TestTagEditFoldsAndResolves(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.UpsertTags(ctx, []asana.Tag{{GID: "g1", Name: "急ぎ"}, {GID: "g2", Name: "重複"}, {GID: "g3", Name: "重複"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveTag(ctx, "重複"); !errors.Is(err, ErrAmbiguous) {
		t.Errorf("ResolveTag of a shared name: err = %v", err)
	}
	if g, err := s.ResolveTag(ctx, "g3"); err != nil || g != "g3" {
		t.Errorf("ResolveTag(g3) = %q, %v", g, err)
	}
	_ = s.UpdateTagLocal(ctx, "g1", map[string]any{"name": "至急"}, testNow)
	_ = s.UpdateTagLocal(ctx, "g1", map[string]any{"color": "dark-blue"}, testNow)
	op := firstOp(t, s, OpUpdateTag)
	if op.Payload["name"] != "至急" || op.Payload["color"] != "dark-blue" || op.Base["name"] != "急ぎ" || op.Base["color"] != "" {
		t.Fatalf("op = %+v", op)
	}
	// Changed back: nothing left to send.
	_ = s.UpdateTagLocal(ctx, "g1", map[string]any{"name": "急ぎ", "color": "none"}, testNow)
	if ops, _ := s.ListOps(ctx); len(ops) != 0 {
		t.Fatalf("ops = %+v", ops)
	}
	for _, bad := range []map[string]any{{"name": "  "}, {"color": "green"}, {"owner": "x"}} {
		if err := s.UpdateTagLocal(ctx, "g1", bad, testNow); err == nil {
			t.Errorf("UpdateTagLocal(%v): no error", bad)
		}
	}
	if err := s.UpdateTagLocal(ctx, "nope", map[string]any{"name": "x"}, testNow); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown tag: err = %v", err)
	}
}

// A tag created offline (its add not sent) and deleted is just forgotten.
func TestDeleteLocalTagForgets(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tag, err := s.AddTagLocal(ctx, "t1", "", "新しいタグ", testNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTagLocal(ctx, tag, testNow); err != nil {
		t.Fatal(err)
	}
	if ops, _ := s.ListOps(ctx); len(ops) != 0 {
		t.Fatalf("ops = %+v", ops)
	}
	if tags, _ := s.TaskTags(ctx, "t1"); len(tags) != 1 || tags[0].GID != "g1" {
		t.Errorf("t1 tags = %v, want only the one it had", tags)
	}
}

func firstOp(t *testing.T, s *Store, kind string) Op {
	t.Helper()
	ops, err := s.ListOps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range ops {
		if op.Kind == kind {
			return op
		}
	}
	t.Fatalf("no %s in %+v", kind, ops)
	return Op{}
}

// Forgetting a new section does not take another new section placed next to it along:
// that one goes last instead.
func TestForgetLocalSectionKeepsNeighbour(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	a, _ := s.CreateSectionLocal(ctx, "p1", "A", "", "", testNow)
	b, err := s.CreateSectionLocal(ctx, "p1", "B", "", a, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSectionLocal(ctx, a, testNow); err != nil {
		t.Fatal(err)
	}
	op := firstOp(t, s, OpCreateSection)
	if op.TaskGID != b || op.Payload["after"] != nil {
		t.Fatalf("B's create = %+v", op)
	}
	if got := sectionGIDs(t, s, "p1"); len(got) == 0 || got[len(got)-1] != b {
		t.Errorf("order = %v, want B kept", got)
	}
}

// A failed create keeps its section through a sync, until it is discarded.
func TestFailedSectionCreateSurvivesPull(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	gid, _ := s.CreateSectionLocal(ctx, "p1", "保留", "", "", testNow)
	if err := s.MarkOpFailed(ctx, firstOp(t, s, OpCreateSection).ID, errors.New("400")); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceSections(ctx, "p1", []asana.Section{{GID: "s1", Name: "家具"}, {GID: "s2", Name: "手続き"}}); err != nil {
		t.Fatal(err)
	}
	if name, err := s.SectionName(ctx, gid); err != nil || name != "保留" {
		t.Errorf("section after a pull: %q, %v", name, err)
	}
}
