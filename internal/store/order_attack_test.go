package store

import (
	"errors"
	"testing"

	"github.com/amasok/asagao/internal/asana"
)

func inSec(section string) []asana.Membership {
	return []asana.Membership{{Project: &asana.Ref{GID: "p1"}, Section: &asana.Ref{GID: section}}}
}

// Two unsent moves in one section: both hold, in the order queued, through any listing.
func TestAttackTwoMovesSameSection(t *testing.T) {
	st, ctx := orderStore(t)
	must(t, st.SetProjectTaskOrder(ctx, "p1", []string{"a", "b", "c", "d"}))
	must(t, st.MoveTaskAtLocal(ctx, "c", "p1", "s1", Position{Before: "a"}, testNow))
	must(t, st.MoveTaskAtLocal(ctx, "b", "p1", "s1", Position{Before: "c"}, testNow))
	wantOrder(t, st, TaskFilter{SectionGID: "s1"}, "b", "c", "a")
	must(t, st.SetSectionTaskOrder(ctx, "s1", []string{"a", "b", "c"}))
	wantOrder(t, st, TaskFilter{SectionGID: "s1"}, "b", "c", "a")
	must(t, st.SetProjectTaskOrder(ctx, "p1", []string{"d", "c", "b", "a"}))
	wantOrder(t, st, TaskFilter{SectionGID: "s1"}, "b", "c", "a")
}

// Discarding one of two moves keeps the other.
func TestAttackDiscardOneOfTwoMoves(t *testing.T) {
	st, ctx := orderStore(t)
	must(t, st.SetProjectTaskOrder(ctx, "p1", []string{"a", "b", "c", "d"}))
	must(t, st.MoveTaskAtLocal(ctx, "c", "p1", "s1", Position{Before: "a"}, testNow))
	must(t, st.MoveTaskAtLocal(ctx, "b", "p1", "s1", Position{After: "c"}, testNow))
	wantOrder(t, st, TaskFilter{SectionGID: "s1"}, "c", "b", "a")
	ops, err := st.ListOps(ctx)
	must(t, err)
	if len(ops) != 2 {
		t.Fatalf("ops = %d", len(ops))
	}
	must(t, st.DiscardOp(ctx, ops[0].ID)) // c's move
	must(t, st.SetSectionTaskOrder(ctx, "s1", []string{"a", "b", "c"}))
	wantOrder(t, st, TaskFilter{SectionGID: "s1"}, "a", "c", "b") // b's move (after c) stays
}

// Listings with duplicates, unknown gids and empty lists must not panic or lose tasks.
func TestAttackOddListings(t *testing.T) {
	st, ctx := orderStore(t)
	must(t, st.SetProjectTaskOrder(ctx, "p1", []string{"b", "b", "zzz", "a"}))
	ts, err := st.ListTasks(ctx, TaskFilter{SectionGID: "s1"})
	must(t, err)
	if len(ts) != 3 {
		t.Fatalf("tasks = %v", gidsOf(ts))
	}
	must(t, st.SetProjectTaskOrder(ctx, "p1", nil))
	wantOrder(t, st, TaskFilter{SectionGID: "s1"}, "a", "b", "c")
	must(t, st.SetSectionTaskOrder(ctx, "s1", nil))
	must(t, st.SetSubtaskOrder(ctx, "parent", nil))
	must(t, st.SetSubtaskOrder(ctx, "nope", []string{"x"}))
	wantSubtasks(t, st, "parent", "x", "y", "z")
	must(t, st.SetSubtaskOrder(ctx, "parent", []string{"y", "y", "x"}))
	wantSubtasks(t, st, "parent", "y", "x", "z")
}

// A move across sections with unsent move, then a pull that places it in the new section
// (Asana sent it): the order stays sane, no duplicates.
func TestAttackMoveThenPullInNewSection(t *testing.T) {
	st, ctx := orderStore(t)
	must(t, st.SetProjectTaskOrder(ctx, "p1", []string{"a", "b", "c", "d"}))
	must(t, st.MoveTaskAtLocal(ctx, "a", "p1", "s2", Position{After: "d"}, testNow))
	must(t, st.UpsertTasks(ctx, []asana.Task{{GID: "a", Name: "a", Memberships: inSec("s2")}}))
	must(t, st.SetSectionTaskOrder(ctx, "s2", []string{"a", "d"}))
	wantOrder(t, st, TaskFilter{SectionGID: "s2"}, "d", "a")
	wantOrder(t, st, TaskFilter{SectionGID: "s1"}, "b", "c")
}

// Moved task, then the move is moved back and a stale listing arrives: no leftover.
func TestAttackMoveBackThenListing(t *testing.T) {
	st, ctx := orderStore(t)
	must(t, st.SetProjectTaskOrder(ctx, "p1", []string{"a", "b", "c", "d"}))
	must(t, st.MoveTaskLocal(ctx, "b", "p1", "s2", testNow))
	must(t, st.MoveTaskLocal(ctx, "b", "p1", "s1", testNow))
	must(t, st.SetSectionTaskOrder(ctx, "s2", []string{"d"}))
	must(t, st.SetSectionTaskOrder(ctx, "s1", []string{"a", "b", "c"}))
	wantOrder(t, st, TaskFilter{SectionGID: "s1"}, "a", "b", "c")
	wantOrder(t, st, TaskFilter{SectionGID: "s2"}, "d")
}

// Reparent into a parent and out again; the old parent's order is not corrupted.
func TestAttackReparentRoundTrip(t *testing.T) {
	st, ctx := orderStore(t)
	must(t, st.SetSubtaskOrder(ctx, "parent", []string{"x", "y", "z"}))
	must(t, st.SetParentLocal(ctx, "y", "a", Position{}, testNow))
	wantSubtasks(t, st, "parent", "x", "z")
	must(t, st.SetSubtaskOrder(ctx, "parent", []string{"x", "y", "z"})) // stale listing
	wantSubtasks(t, st, "parent", "x", "z")
	must(t, st.SetParentLocal(ctx, "y", "parent", Position{}, testNow))
	wantSubtasks(t, st, "parent", "x", "y", "z")
	must(t, st.SetParentLocal(ctx, "x", "parent", Position{After: "z"}, testNow))
	wantSubtasks(t, st, "parent", "y", "z", "x")
	must(t, st.SetSubtaskOrder(ctx, "parent", []string{"x", "y", "z"}))
	wantSubtasks(t, st, "parent", "y", "z", "x")
}

// A task in two projects keeps separate places per project.
func TestAttackTwoProjects(t *testing.T) {
	st, ctx := orderStore(t)
	must(t, st.UpsertProjects(ctx, "w1", []asana.Project{{GID: "p2", Name: "P2"}}, "u1"))
	must(t, st.ReplaceSections(ctx, "p2", []asana.Section{{GID: "q1", Name: "Q1"}}))
	both := append(inSec("s1"), asana.Membership{Project: &asana.Ref{GID: "p2"}, Section: &asana.Ref{GID: "q1"}})
	must(t, st.UpsertTasks(ctx, []asana.Task{{GID: "a", Name: "a", Memberships: both}, {GID: "b", Name: "b", Memberships: both}}))
	must(t, st.SetProjectTaskOrder(ctx, "p1", []string{"c", "b", "a", "d"}))
	must(t, st.SetProjectTaskOrder(ctx, "p2", []string{"a", "b"}))
	wantOrder(t, st, TaskFilter{SectionGID: "s1"}, "c", "b", "a")
	wantOrder(t, st, TaskFilter{SectionGID: "q1"}, "a", "b")
}

// A move that failed for good is not put back by a listing.
func TestAttackFailedMoveNotReapplied(t *testing.T) {
	st, ctx := orderStore(t)
	must(t, st.SetProjectTaskOrder(ctx, "p1", []string{"a", "b", "c", "d"}))
	must(t, st.MoveTaskAtLocal(ctx, "c", "p1", "s1", Position{Before: "a"}, testNow))
	must(t, st.MarkOpFailed(ctx, onlyOp(t, st).ID, errors.New("boom")))
	must(t, st.SetSectionTaskOrder(ctx, "s1", []string{"a", "b", "c"}))
	wantOrder(t, st, TaskFilter{SectionGID: "s1"}, "a", "b", "c")
}
