package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/domain/plan"
	"github.com/amasok/asagao/internal/readmodel"
)

// sectionStore records the calls of the section use cases. Project 新居 (p1) has the
// sections 家具 (s1) and 完了 (s2); task t1 is in it.
type sectionStore struct {
	relationStore
	marks map[string]string // feature+":"+ws -> RFC3339 timestamp
}

// refuse records a 402 for feature in workspace ws at at, as the sync engine does.
func (f *sectionStore) refuse(feature, ws string, at time.Time) {
	if f.marks == nil {
		f.marks = map[string]string{}
	}
	f.marks[feature+":"+ws] = at.UTC().Format(time.RFC3339)
}

// p1, its sections and task t1 are in workspace w1 (relationStore); the cache also knows w2.
func (f *sectionStore) ProjectWorkspace(_ context.Context, gid string) (string, error) {
	if gid == "p1" {
		return "w1", nil
	}
	return "", nil
}

func (f *sectionStore) SectionWorkspace(_ context.Context, gid string) (string, error) {
	if gid == "s1" || gid == "s2" {
		return "w1", nil
	}
	return "", nil
}

func (f *sectionStore) WorkspaceGIDs(context.Context) ([]string, error) {
	return []string{"w1", "w2"}, nil
}

func (f *sectionStore) ResolveProject(_ context.Context, arg string) (string, error) {
	f.log("ResolveProject %s", arg)
	if arg == "新居" || arg == "p1" {
		return "p1", nil
	}
	return "", fmt.Errorf("project %q: %w", arg, errNotFound)
}

func (f *sectionStore) ResolveSection(_ context.Context, projectGID, arg string) (string, error) {
	f.log("ResolveSection %s %s", projectGID, arg)
	if g, ok := map[string]string{"家具": "s1", "完了": "s2"}[arg]; ok && projectGID == "p1" {
		return g, nil
	}
	return "", fmt.Errorf("section %q: %w", arg, errNotFound)
}

func (f *sectionStore) ResolveMove(_ context.Context, taskGID, project, section string) (string, string, error) {
	f.log("ResolveMove %s %q %s", taskGID, project, section)
	if taskGID != "t1" || (project != "" && project != "新居") || section != "完了" {
		return "", "", fmt.Errorf("move %s: %w", taskGID, errNotFound)
	}
	return "p1", "s2", nil
}

func (f *sectionStore) MoveTaskLocal(_ context.Context, gid, projectGID, sectionGID string, now time.Time) error {
	return f.write("MoveTaskLocal", "%s %s %s at=%s", gid, projectGID, sectionGID, now.Format(time.TimeOnly))
}

func (f *sectionStore) RenameSectionLocal(_ context.Context, sectionGID, name string, now time.Time) error {
	return f.write("RenameSectionLocal", "%s %q at=%s", sectionGID, name, now.Format(time.TimeOnly))
}

func TestSectionsMoveAndRename(t *testing.T) {
	st := &sectionStore{}
	s := &Sections{Store: st, Clock: ticking()}
	ctx := context.Background()
	if err := s.Move(ctx, "t1", "新居", "完了"); err != nil {
		t.Fatal(err)
	}
	if err := s.MoveHere(ctx, "t1", "p1", "s1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Rename(ctx, "新居", "家具", "大型家具"); err != nil {
		t.Fatal(err)
	}
	if err := s.RenameHere(ctx, "s2", "済み"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`ResolveMove t1 "新居" 完了`, "MoveTaskLocal t1 p1 s2 at=10:00:01",
		"MoveTaskLocal t1 p1 s1 at=10:00:02",
		"ResolveProject 新居", "ResolveSection p1 家具", `RenameSectionLocal s1 "大型家具" at=10:00:03`,
		`RenameSectionLocal s2 "済み" at=10:00:04`,
	}
	if !slices.Equal(st.calls, want) {
		t.Errorf("calls\n%s\nwant\n%s", strings.Join(st.calls, "\n"), strings.Join(want, "\n"))
	}
	if p, sec, err := s.ResolveMove(ctx, "t1", "", "完了"); p != "p1" || sec != "s2" || err != nil {
		t.Errorf("ResolveMove = %s, %s, %v", p, sec, err)
	}
}

// What does not resolve is the store's error, as it is, and nothing is written.
func TestSectionsFailWritesNothing(t *testing.T) {
	ctx := context.Background()
	for name, f := range map[string]func(*Sections) error{
		"move: no section":   func(s *Sections) error { return s.Move(ctx, "t1", "", "ない") },
		"move: no project":   func(s *Sections) error { return s.Move(ctx, "t1", "ない", "完了") },
		"rename: no project": func(s *Sections) error { return s.Rename(ctx, "ない", "家具", "x") },
		"rename: no section": func(s *Sections) error { return s.Rename(ctx, "新居", "ない", "x") },
	} {
		t.Run(name, func(t *testing.T) {
			st := &sectionStore{}
			if err := f(&Sections{Store: st, Clock: ticking()}); !errors.Is(err, errNotFound) {
				t.Fatalf("err = %v", err)
			}
			var fe *FieldError
			if err := f(&Sections{Store: st, Clock: ticking()}); errors.As(err, &fe) {
				t.Errorf("err = %#v, want the store's as it is", err)
			}
			if w := st.writes(); len(w) != 0 {
				t.Errorf("wrote: %v", w)
			}
		})
	}
	st := &sectionStore{}
	st.failWrite = "MoveTaskLocal"
	if err := (&Sections{Store: st, Clock: ticking()}).Move(ctx, "t1", "", "完了"); !errors.Is(err, errWrite) {
		t.Errorf("a failed write: err = %v", err)
	}
}

func (f *sectionStore) CreateSectionLocal(_ context.Context, projectGID, name, before, after string, now time.Time) (string, error) {
	return "local:new", f.write("CreateSectionLocal", "%s %q before=%s after=%s at=%s", projectGID, name, before, after, now.Format(time.TimeOnly))
}

func (f *sectionStore) DeleteSectionLocal(_ context.Context, sectionGID string, now time.Time) error {
	return f.write("DeleteSectionLocal", "%s at=%s", sectionGID, now.Format(time.TimeOnly))
}

func (f *sectionStore) MoveSectionLocal(_ context.Context, sectionGID, before, after string, now time.Time) error {
	return f.write("MoveSectionLocal", "%s before=%s after=%s at=%s", sectionGID, before, after, now.Format(time.TimeOnly))
}

func (f *sectionStore) PlanRefusedAt(_ context.Context, feature, workspaceGID string) (time.Time, bool, error) {
	v := f.marks[feature+":"+workspaceGID]
	if v == "" {
		return time.Time{}, false, nil
	}
	at, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, false, nil
	}
	return at, true, nil
}

func (f *sectionStore) SectionName(_ context.Context, gid string) (string, error) {
	if n, ok := map[string]string{"s1": "家具", "s2": "完了"}[gid]; ok {
		return n, nil
	}
	return "", errNotFound
}

func TestSectionsCreateDeleteReorder(t *testing.T) {
	st := &sectionStore{}
	s := &Sections{Store: st, Clock: ticking()}
	ctx := context.Background()
	if gid, err := s.Create(ctx, "新居", "保留", SectionPlace{After: "家具"}); err != nil || gid != "local:new" {
		t.Fatalf("Create = %q, %v", gid, err)
	}
	if err := s.Reorder(ctx, "新居", "完了", SectionPlace{Before: "家具"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "p1", "完了"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		// (each change reads the clock for its plan check first)
		`CreateSectionLocal p1 "保留" before= after=s1 at=10:00:02`,
		"MoveSectionLocal s2 before=s1 after= at=10:00:04",
		"DeleteSectionLocal s2 at=10:00:06",
	}
	if w := st.writes(); !slices.Equal(w, want) {
		t.Errorf("writes\n%s\nwant\n%s", strings.Join(w, "\n"), strings.Join(want, "\n"))
	}
	if _, err := s.Create(ctx, "新居", "x", SectionPlace{Before: "家具", After: "完了"}); err == nil {
		t.Error("Create with both before and after: no error")
	}
	if _, err := s.Create(ctx, "新居", "x", SectionPlace{After: "ない"}); !errors.Is(err, errNotFound) {
		t.Errorf("Create after a missing section: err = %v", err)
	}
	if len(st.writes()) != len(want) {
		t.Errorf("a failed call wrote: %v", st.writes())
	}
}

// After Asana refused the feature with a 402 in the project's workspace, none of it is
// offered there (nothing is written) for a day.
func TestSectionsPlanUnavailable(t *testing.T) {
	st := &sectionStore{}
	clock := ticking()
	now := clock()
	st.refuse(plan.Sections, "w1", now)
	s := &Sections{Store: st, Clock: clock}
	ctx := context.Background()
	for name, f := range map[string]func() error{
		"create":       func() error { _, err := s.Create(ctx, "新居", "x", SectionPlace{}); return err },
		"here":         func() error { _, err := s.CreateHere(ctx, "p1", "x", "", ""); return err },
		"delete":       func() error { return s.Delete(ctx, "新居", "完了") },
		"delete here":  func() error { return s.DeleteHere(ctx, "s2") },
		"reorder":      func() error { return s.Reorder(ctx, "新居", "完了", SectionPlace{After: "家具"}) },
		"reorder here": func() error { return s.ReorderHere(ctx, "s2", "s1", "") },
	} {
		if err := f(); !IsPlanUnavailable(err) || !strings.Contains(err.Error(), "402") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	// Renaming and moving tasks are not part of it.
	if err := s.Rename(ctx, "新居", "家具", "大型家具"); err != nil {
		t.Errorf("rename: %v", err)
	}
	if w := st.writes(); len(w) != 1 {
		t.Errorf("writes = %v", w)
	}
	// The menu: w2 still offers it; refused in every workspace, it is not offered.
	if !s.Available(ctx) {
		t.Error("Available = false with a 402 in w1 only")
	}
	st.refuse(plan.Sections, "w2", now)
	if s.Available(ctx) {
		t.Error("Available = true with a 402 in every workspace")
	}
	if !(&Sections{Store: &sectionStore{}, Clock: ticking()}).Available(ctx) {
		t.Error("Available = false without a 402")
	}
}

// #50: a 402 in another workspace (a free one) does not hide sections in this one (a paid
// organization), and a 402 is tried again after readmodel.PlanRecheckAfter.
func TestSectionsPlanPerWorkspace(t *testing.T) {
	ctx := context.Background()
	st := &sectionStore{}
	clock := ticking()
	st.refuse(plan.Sections, "w2", clock())
	s := &Sections{Store: st, Clock: clock}
	if _, err := s.Create(ctx, "新居", "保留", SectionPlace{}); err != nil {
		t.Errorf("Create with a 402 in w2 only: %v", err)
	}
	if err := s.ReorderHere(ctx, "s2", "s1", ""); err != nil {
		t.Errorf("ReorderHere with a 402 in w2 only: %v", err)
	}

	st = &sectionStore{}
	start := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	st.refuse(plan.Sections, "w1", start)
	at := start.Add(plan.RecheckAfter - time.Second)
	s = &Sections{Store: st, Clock: func() time.Time { return at }}
	if _, err := s.CreateHere(ctx, "p1", "x", "", ""); !IsPlanUnavailable(err) {
		t.Errorf("CreateHere just before a day: err = %v", err)
	}
	at = start.Add(plan.RecheckAfter)
	if _, err := s.CreateHere(ctx, "p1", "x", "", ""); err != nil {
		t.Errorf("CreateHere a day after the 402: %v", err)
	}
}

// #50: the records before it (plan_unavailable:<feature>, for every workspace and for
// good) do not hide anything any more.
func TestSectionsOldGlobalPlanRecordIgnored(t *testing.T) {
	ctx := context.Background()
	st := &sectionStore{marks: map[string]string{
		"plan_unavailable:" + plan.Sections:  "2026-11-17T09:59:00Z",
		"plan_unavailable:" + plan.MySection: "2026-11-17T09:59:00Z",
	}}
	s := &Sections{Store: st, Clock: ticking()}
	if !s.Available(ctx) {
		t.Error("Available = false with an old record")
	}
	if _, err := s.Create(ctx, "新居", "保留", SectionPlace{}); err != nil {
		t.Errorf("Create with an old record: %v", err)
	}
	if secs, err := s.MySections(ctx, ""); err != nil || len(secs) != 2 {
		t.Errorf("MySections with an old record = %v, %v", secs, err)
	}
	if err := s.MoveMine(ctx, "t1", "あとで"); err != nil {
		t.Errorf("MoveMine with an old record: %v", err)
	}
}

func (f *sectionStore) ListMyTaskSections(_ context.Context, workspaceGID string) ([]readmodel.SectionView, error) {
	return []readmodel.SectionView{{GID: "m1", Name: "今日やる"}, {GID: "m2", Name: "あとで"}}, nil
}

func (f *sectionStore) ResolveMySection(_ context.Context, taskGID, arg string) (string, error) {
	f.log("ResolveMySection %s %s", taskGID, arg)
	if g, ok := map[string]string{"今日やる": "m1", "あとで": "m2"}[arg]; ok {
		return g, nil
	}
	return "", errNotFound
}

func (f *sectionStore) MoveMySectionLocal(_ context.Context, gid, sectionGID string, now time.Time) error {
	return f.write("MoveMySectionLocal", "%s %s at=%s", gid, sectionGID, now.Format(time.TimeOnly))
}

func TestSectionsMoveMine(t *testing.T) {
	st := &sectionStore{}
	s := &Sections{Store: st, Clock: ticking()}
	ctx := context.Background()
	if err := s.MoveMine(ctx, "t1", "あとで"); err != nil {
		t.Fatal(err)
	}
	if err := s.MoveMine(ctx, "t1", "ない"); !errors.Is(err, errNotFound) {
		t.Errorf("unknown section: err = %v", err)
	}
	if w := st.writes(); !slices.Equal(w, []string{"MoveMySectionLocal t1 m2 at=10:00:03"}) {
		t.Errorf("writes = %v", w)
	}
	if secs, _ := s.MySections(ctx, ""); len(secs) != 2 {
		t.Errorf("MySections = %v", secs)
	}
	// A 402 in another workspace hides nothing here.
	st.refuse(plan.MySection, "w2", time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC))
	if secs, err := s.MySections(ctx, "w1"); err != nil || len(secs) != 2 {
		t.Errorf("MySections(w1) after a 402 in w2 = %v, %v", secs, err)
	}
	if err := s.MoveMine(ctx, "t1", "今日やる"); err != nil {
		t.Errorf("MoveMine after a 402 in w2: %v", err)
	}
	if secs, err := s.MySections(ctx, "w2"); err != nil || len(secs) != 0 {
		t.Errorf("MySections(w2) after a 402 there = %v, %v", secs, err)
	}
	// Not on this plan in the task's (and the default) workspace: nothing is offered.
	st.refuse(plan.MySection, "w1", time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC))
	if secs, err := s.MySections(ctx, ""); err != nil || len(secs) != 0 {
		t.Errorf("MySections after a 402 = %v, %v", secs, err)
	}
	if err := s.MoveMine(ctx, "t1", "今日やる"); !IsPlanUnavailable(err) {
		t.Errorf("MoveMine after a 402: err = %v", err)
	}
}
