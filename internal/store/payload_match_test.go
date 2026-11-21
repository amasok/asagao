package store

import (
	"context"
	"testing"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/asana/model"
)

// The entries picked by what their payload holds are read as their kind's struct, as far
// as it reads (payload_match.go), not with json_extract in SQL.

// An unsent tag change whose tag does not read (absent: json_extract gave NULL, which
// failed the scan and with it the whole pull of the task) applies nothing; the others are
// applied again.
func TestReapplyTagOpsSkipsATagThatDoesNotRead(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	insertRawOp(t, s, OpAddTag, "t1", `{"tag_name":"x","workspace":"w1"}`, `{}`)
	insertRawOp(t, s, OpAddTag, "t1", `{"tag":"g7","tag_name":"y","workspace":"w1"}`, `{}`)
	if err := s.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "牛乳を買う"}}); err != nil {
		t.Fatalf("UpsertTasks: %v", err)
	}
	if n := countRows(t, s, "SELECT count(*) FROM task_tags WHERE task_gid = 't1' AND tag_gid = 'g7'"); n != 1 {
		t.Errorf("t1/g7 tagged %d times, want 1 (the readable add_tag applied again)", n)
	}
	if n := countRows(t, s, "SELECT count(*) FROM task_tags WHERE task_gid = 't1' AND tag_gid = ''"); n != 0 {
		t.Errorf("t1 tagged with an empty tag")
	}
}

// An unsent move in My Tasks whose section does not read moves nothing (json_extract gave
// NULL, which failed the pull of the task).
func TestUnsentMySectionMoveThatDoesNotRead(t *testing.T) {
	ctx := context.Background()
	s := newMyTasksStore(t)
	insertRawOp(t, s, OpMoveMySection, "mt", `{"section_name":"今日やる","workspace":"w1"}`, `{"section":"m2"}`)
	if err := s.UpsertTasks(ctx, []asana.Task{{GID: "mt", Name: "私の", Workspace: &asana.Ref{GID: "w1"}, Assignee: &asana.Ref{GID: "me"},
		AssigneeSection: model.OptionalRef{Set: true, Ref: &asana.Ref{GID: "m2", Name: "あとで"}}}}); err != nil {
		t.Fatalf("UpsertTasks: %v", err)
	}
	if d, _ := s.GetTask(ctx, "mt"); d.MySection == nil || d.MySection.GID != "m2" {
		t.Errorf("mt in %+v, want m2 (Asana's)", d.MySection)
	}
}

// An entry held because it has a key this version does not know is still found by what
// it holds that reads, as json_extract found it: undoing it cancels it.
func TestHeldEntryIsStillPickedByWhatReads(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	insertRawOp(t, s, OpAddTag, "t2", `{"tag":"g1","tag_name":"買い物","workspace":"","from_future":1}`, `{}`)
	must(t, s.RemoveTagLocal(ctx, "t2", "g1", testNow))
	if n := countRows(t, s, "SELECT count(*) FROM outbox"); n != 0 {
		t.Errorf("outbox has %d entries: the held add_tag was not cancelled", n)
	}
}

// A value of another type matches no gid (json_extract compared it as a number, and found
// nothing either).
func TestMistypedValueMatchesNothing(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	insertRawOp(t, s, OpAddTag, "t2", `{"tag":7,"tag_name":"x","workspace":""}`, `{}`)
	queued, err := s.TagCreateQueued(ctx, "7")
	must(t, err)
	if queued {
		t.Error("a tag 7 (a number) taken for the tag \"7\"")
	}
}
