package store

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
)

// The order of tasks as Asana has it (#51).

// orderStore has project p1 with sections s1, s2 and tasks a, b, c in s1, d in s2, and a
// parent with subtasks x, y, z. The order used before #51 (and without a known place) is
// a, b, c (by due date, then name) and x, y, z (by creation).
func orderStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	must(t, err)
	t.Cleanup(func() { st.Close() })
	must(t, st.UpsertProjects(ctx, "w1", []asana.Project{{GID: "p1", Name: "P"}}, "u1"))
	must(t, st.ReplaceSections(ctx, "p1", []asana.Section{{GID: "s1", Name: "S1"}, {GID: "s2", Name: "S2"}}))
	in := func(section string) []asana.Membership {
		return []asana.Membership{{Project: &asana.Ref{GID: "p1"}, Section: &asana.Ref{GID: section}}}
	}
	day := func(d int) time.Time { return time.Date(2026, 11, d, 0, 0, 0, 0, time.UTC) }
	parent := &asana.Ref{GID: "parent"}
	must(t, st.UpsertTasks(ctx, []asana.Task{
		{GID: "a", Name: "a", DueOn: "2026-11-01", Memberships: in("s1")},
		{GID: "b", Name: "b", Memberships: in("s1")},
		{GID: "c", Name: "c", Memberships: in("s1")},
		{GID: "d", Name: "d", Memberships: in("s2")},
		{GID: "parent", Name: "parent", NumSubtasks: 3},
		{GID: "x", Name: "x", Parent: parent, CreatedAt: day(1)},
		{GID: "y", Name: "y", Parent: parent, CreatedAt: day(2)},
		{GID: "z", Name: "z", Parent: parent, CreatedAt: day(3)},
	}))
	return st, ctx
}

func gidsOf(ts []TaskSummary) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.GID
	}
	return out
}

func wantOrder(t *testing.T, st *Store, f TaskFilter, want ...string) {
	t.Helper()
	ts, err := st.ListTasks(context.Background(), f)
	must(t, err)
	if got := gidsOf(ts); !slices.Equal(got, want) {
		t.Fatalf("ListTasks(%+v) = %v, want %v", f, got, want)
	}
}

func wantSubtasks(t *testing.T, st *Store, parent string, want ...string) {
	t.Helper()
	d, err := st.GetTask(context.Background(), parent)
	must(t, err)
	if got := gidsOf(d.Subtasks); !slices.Equal(got, want) {
		t.Fatalf("subtasks of %s = %v, want %v", parent, got, want)
	}
	wantOrder(t, st, TaskFilter{ParentGID: parent}, want...)
}

// Without a known place, the order is the one used before.
func TestOrderUnknownKeepsOldOrder(t *testing.T) {
	st, _ := orderStore(t)
	wantOrder(t, st, TaskFilter{SectionGID: "s1"}, "a", "b", "c")
	wantOrder(t, st, TaskFilter{ProjectGID: "p1"}, "a", "b", "c", "d")
	d, err := st.GetTask(context.Background(), "parent")
	must(t, err)
	if got := gidsOf(d.Subtasks); !slices.Equal(got, []string{"x", "y", "z"}) {
		t.Fatalf("subtasks = %v", got)
	}
}

func TestOrderFromListings(t *testing.T) {
	st, ctx := orderStore(t)
	// A full listing of the project: in the project's order (s2's d first only if Asana
	// says so; the sections' order still groups them).
	must(t, st.SetProjectTaskOrder(ctx, "p1", []string{"c", "a", "b", "d"}))
	wantOrder(t, st, TaskFilter{SectionGID: "s1"}, "c", "a", "b")
	wantOrder(t, st, TaskFilter{ProjectGID: "p1"}, "c", "a", "b", "d")
	// Another filter keeps the order of the place.
	done := false
	wantOrder(t, st, TaskFilter{ProjectGID: "p1", Completed: &done}, "c", "a", "b", "d")

	// A section's listing: places within the section only. A listed task the cache has
	// elsewhere (d) is left alone; one not listed (a) comes after.
	must(t, st.SetSectionTaskOrder(ctx, "s1", []string{"b", "d", "c"}))
	wantOrder(t, st, TaskFilter{SectionGID: "s1"}, "b", "c", "a")
	wantOrder(t, st, TaskFilter{ProjectGID: "p1"}, "b", "c", "d", "a")
	// An unknown section is ignored.
	must(t, st.SetSectionTaskOrder(ctx, "nope", []string{"a"}))

	must(t, st.SetSubtaskOrder(ctx, "parent", []string{"z", "x", "y"}))
	wantSubtasks(t, st, "parent", "z", "x", "y")
	must(t, st.SetSubtaskOrder(ctx, "parent", []string{"y", "x"}))
	wantSubtasks(t, st, "parent", "y", "x", "z")
}

// A pull of a task keeps its place while it stays in its section (or under its parent),
// and drops it when Asana has it elsewhere.
func TestOrderSurvivesUpsert(t *testing.T) {
	st, ctx := orderStore(t)
	must(t, st.SetProjectTaskOrder(ctx, "p1", []string{"c", "b", "a", "d"}))
	must(t, st.SetSubtaskOrder(ctx, "parent", []string{"z", "y", "x"}))
	in := func(section string) []asana.Membership {
		return []asana.Membership{{Project: &asana.Ref{GID: "p1"}, Section: &asana.Ref{GID: section}}}
	}
	must(t, st.UpsertTasks(ctx, []asana.Task{
		{GID: "c", Name: "c", Memberships: in("s1")},
		{GID: "z", Name: "z", Parent: &asana.Ref{GID: "parent"}},
	}))
	wantOrder(t, st, TaskFilter{SectionGID: "s1"}, "c", "b", "a")
	wantSubtasks(t, st, "parent", "z", "y", "x")

	must(t, st.UpsertTasks(ctx, []asana.Task{
		{GID: "c", Name: "c", Memberships: in("s2")},
		{GID: "z", Name: "z", Parent: &asana.Ref{GID: "b"}},
	}))
	wantOrder(t, st, TaskFilter{SectionGID: "s2"}, "d", "c")
	must(t, st.UpsertTasks(ctx, []asana.Task{{GID: "z", Name: "z", Parent: &asana.Ref{GID: "parent"}}}))
	wantSubtasks(t, st, "parent", "y", "x", "z")
}

// A move puts the task where Asana will, before it is sent; a listing of Asana's old
// order, or a pull of the task, does not undo it while it is unsent.
func TestOrderOfUnsentMove(t *testing.T) {
	st, ctx := orderStore(t)
	must(t, st.SetProjectTaskOrder(ctx, "p1", []string{"a", "b", "c", "d"}))
	must(t, st.MoveTaskAtLocal(ctx, "c", "p1", "s1", Position{Before: "a"}, testNow))
	wantOrder(t, st, TaskFilter{SectionGID: "s1"}, "c", "a", "b")
	must(t, st.MoveTaskAtLocal(ctx, "c", "p1", "s1", Position{After: "a"}, testNow))
	wantOrder(t, st, TaskFilter{SectionGID: "s1"}, "a", "c", "b")

	must(t, st.SetSectionTaskOrder(ctx, "s1", []string{"a", "b", "c"}))
	wantOrder(t, st, TaskFilter{SectionGID: "s1"}, "a", "c", "b")
	must(t, st.SetProjectTaskOrder(ctx, "p1", []string{"a", "b", "c", "d"}))
	wantOrder(t, st, TaskFilter{SectionGID: "s1"}, "a", "c", "b")
	must(t, st.UpsertTasks(ctx, []asana.Task{{GID: "c", Name: "c",
		Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}, Section: &asana.Ref{GID: "s1"}}}}}))
	wantOrder(t, st, TaskFilter{SectionGID: "s1"}, "a", "c", "b")

	// Discarding the move gives the task its place back.
	must(t, st.DiscardOp(ctx, onlyOp(t, st).ID))
	wantOrder(t, st, TaskFilter{SectionGID: "s1"}, "a", "b", "c")
}

// A move to another section without a place goes to its top (addTask does so); moving it
// back gives it its old place.
func TestOrderOfMoveToOtherSection(t *testing.T) {
	st, ctx := orderStore(t)
	must(t, st.SetProjectTaskOrder(ctx, "p1", []string{"a", "b", "c", "d"}))
	must(t, st.MoveTaskLocal(ctx, "b", "p1", "s2", testNow))
	wantOrder(t, st, TaskFilter{SectionGID: "s2"}, "b", "d")
	wantOrder(t, st, TaskFilter{ProjectGID: "p1"}, "a", "c", "b", "d")
	// Asana's listing does not have it there yet (a pull of it neither).
	must(t, st.SetSectionTaskOrder(ctx, "s2", []string{"d"}))
	wantOrder(t, st, TaskFilter{SectionGID: "s2"}, "b", "d")
	must(t, st.UpsertTasks(ctx, []asana.Task{{GID: "b", Name: "b",
		Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}, Section: &asana.Ref{GID: "s1"}}}}}))
	wantOrder(t, st, TaskFilter{SectionGID: "s2"}, "b", "d")
	// With a place.
	must(t, st.MoveTaskAtLocal(ctx, "b", "p1", "s2", Position{After: "d"}, testNow))
	wantOrder(t, st, TaskFilter{SectionGID: "s2"}, "d", "b")

	must(t, st.MoveTaskLocal(ctx, "b", "p1", "s1", testNow))
	if ops, _ := st.ListOps(ctx); len(ops) != 0 {
		t.Fatalf("moved back, still queued: %+v", ops)
	}
	wantOrder(t, st, TaskFilter{SectionGID: "s1"}, "a", "b", "c")
}

// A parent change with a place puts the subtask there, and keeps it there through a
// listing of Asana's old order and a pull of the subtask; one back gives the old place.
func TestOrderOfUnsentSetParent(t *testing.T) {
	st, ctx := orderStore(t)
	must(t, st.SetSubtaskOrder(ctx, "parent", []string{"x", "y", "z"}))
	must(t, st.SetParentLocal(ctx, "z", "parent", Position{Before: "x"}, testNow))
	wantSubtasks(t, st, "parent", "z", "x", "y")

	must(t, st.SetSubtaskOrder(ctx, "parent", []string{"x", "y", "z"}))
	wantSubtasks(t, st, "parent", "z", "x", "y")
	must(t, st.UpsertTasks(ctx, []asana.Task{{GID: "z", Name: "z", Parent: &asana.Ref{GID: "parent"}}}))
	wantSubtasks(t, st, "parent", "z", "x", "y")

	// Under another parent and back: the old place.
	must(t, st.SetParentLocal(ctx, "a", "parent", Position{After: "x"}, testNow))
	wantSubtasks(t, st, "parent", "z", "x", "a", "y")
	// a's parent change, pulled before it is sent (Asana still has a at the top level).
	must(t, st.UpsertTasks(ctx, []asana.Task{{GID: "a", Name: "a"}}))
	wantSubtasks(t, st, "parent", "z", "x", "a", "y")

	must(t, st.SetParentLocal(ctx, "y", "a", Position{}, testNow))
	wantSubtasks(t, st, "parent", "z", "x", "a")
	must(t, st.SetParentLocal(ctx, "y", "parent", Position{}, testNow))
	wantSubtasks(t, st, "parent", "z", "x", "a", "y")
}
