package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/readmodel"
)

func (f *sectionStore) ResolveMoveNear(_ context.Context, taskGID, project, anchor string) (string, string, error) {
	f.log("ResolveMoveNear %s %q %s", taskGID, project, anchor)
	if taskGID != "t1" || anchor != "t9" {
		return "", "", fmt.Errorf("anchor %s: %w", anchor, errNotFound)
	}
	return "p1", "s1", nil
}

func (f *sectionStore) MoveTaskAtLocal(_ context.Context, gid, projectGID, sectionGID string, pos model.Position, now time.Time) error {
	return f.write("MoveTaskAtLocal", "%s %s %s before=%s after=%s at=%s", gid, projectGID, sectionGID, pos.Before, pos.After, now.Format(time.TimeOnly))
}

func TestSectionsMoveAt(t *testing.T) {
	st := &sectionStore{}
	s := &Sections{Store: st, Clock: ticking()}
	ctx := context.Background()
	if err := s.MoveAt(ctx, "t1", "", "完了", model.Position{}); err != nil { // as Move
		t.Fatal(err)
	}
	if err := s.MoveAt(ctx, "t1", "", "", model.Position{Before: "t9"}); err != nil { // the anchor's section
		t.Fatal(err)
	}
	if err := s.MoveAt(ctx, "t1", "新居", "完了", model.Position{After: "t8"}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`ResolveMove t1 "" 完了`, "MoveTaskLocal t1 p1 s2 at=10:00:01",
		`ResolveMoveNear t1 "" t9`, "MoveTaskAtLocal t1 p1 s1 before=t9 after= at=10:00:02",
		`ResolveMove t1 "新居" 完了`, "MoveTaskAtLocal t1 p1 s2 before= after=t8 at=10:00:03",
	}
	if !slices.Equal(st.calls, want) {
		t.Errorf("calls\n%s\nwant\n%s", strings.Join(st.calls, "\n"), strings.Join(want, "\n"))
	}
	st = &sectionStore{}
	if err := (&Sections{Store: st, Clock: ticking()}).MoveAt(ctx, "t1", "", "", model.Position{}); !errors.Is(err, errNoTarget) || len(st.calls) != 0 {
		t.Errorf("no target: %v %v", err, st.calls)
	}
	if err := (&Sections{Store: st, Clock: ticking()}).MoveAt(ctx, "t1", "", "", model.Position{After: "ない"}); !errors.Is(err, errNotFound) || len(st.writes()) != 0 {
		t.Errorf("bad anchor: %v %v", err, st.calls)
	}
}

type taskOpsStore struct{ relationStore }

func (f *taskOpsStore) PlanDeleteTask(_ context.Context, gid string) (readmodel.DeletePlan, error) {
	f.log("PlanDeleteTask %s", gid)
	return readmodel.DeletePlan{GID: gid, Name: "n"}, nil
}

func (f *taskOpsStore) DeleteTaskLocal(_ context.Context, gid string, now time.Time) (readmodel.DeletePlan, error) {
	return readmodel.DeletePlan{GID: gid, Name: "n"}, f.write("DeleteTaskLocal", "%s at=%s", gid, now.Format(time.TimeOnly))
}

func (f *taskOpsStore) SetParentLocal(_ context.Context, gid, parent string, pos model.Position, now time.Time) error {
	return f.write("SetParentLocal", "%s %q before=%s after=%s at=%s", gid, parent, pos.Before, pos.After, now.Format(time.TimeOnly))
}

func TestTaskOps(t *testing.T) {
	st := &taskOpsStore{}
	o := &TaskOps{Store: st, Clock: ticking()}
	ctx := context.Background()
	if p, err := o.PlanDelete(ctx, "t1"); err != nil || p.GID != "t1" {
		t.Fatal(p, err)
	}
	if p, err := o.Delete(ctx, "t1"); err != nil || p.GID != "t1" {
		t.Fatal(p, err)
	}
	if err := o.SetParent(ctx, "t1", "t2", model.Position{After: "t3"}); err != nil {
		t.Fatal(err)
	}
	if err := o.SetParent(ctx, "t1", "", model.Position{}); err != nil {
		t.Fatal(err)
	}
	want := []string{"PlanDeleteTask t1", "DeleteTaskLocal t1 at=10:00:01",
		`SetParentLocal t1 "t2" before= after=t3 at=10:00:02`, `SetParentLocal t1 "" before= after= at=10:00:03`}
	if !slices.Equal(st.calls, want) {
		t.Errorf("calls\n%s\nwant\n%s", strings.Join(st.calls, "\n"), strings.Join(want, "\n"))
	}
}
