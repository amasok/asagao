package store

import (
	"context"
	"testing"

	"github.com/amasok/asagao/internal/asana"
)

// The queries that used to pick entries by json_extract on their columns never decoded
// the whole row: a payload that is valid JSON but not an object (json_extract gave NULL)
// or a base/conflict column the decoder cannot read must not make reading a task, the
// dependencies or the queue of tags and sections fail now that the entries are decoded.

func TestPayloadNotAnObjectDoesNotBreakReads(t *testing.T) {
	for _, payload := range []string{`[]`, `"x"`, `7`, `null`} {
		t.Run(payload, func(t *testing.T) {
			ctx := context.Background()
			s := newTestStore(t)
			must(t, s.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "牛乳"}}))
			for _, k := range []Kind{OpUpdateStory, OpAddDependency, OpAddTag, OpCreateSection, OpUploadAttachment} {
				insertRawOp(t, s, k, "t1", payload, `{}`)
			}
			if _, err := s.GetTask(ctx, "t1"); err != nil {
				t.Errorf("GetTask: %v", err)
			}
			if _, err := s.TagCreateQueued(ctx, "g1"); err != nil {
				t.Errorf("TagCreateQueued: %v", err)
			}
			if _, err := s.SectionCreateQueued(ctx, "s1"); err != nil {
				t.Errorf("SectionCreateQueued: %v", err)
			}
			if _, err := s.taskDependencies(ctx, "t1", false); err != nil {
				t.Errorf("TaskDependencies: %v", err)
			}
		})
	}
}

func TestBadBaseColumnDoesNotBreakGetTask(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	must(t, s.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "牛乳"}}))
	insertRawOp(t, s, OpUpdateStory, "t1", `{"story":"c1","text":"x"}`, `not json`)
	if _, err := s.GetTask(ctx, "t1"); err != nil {
		t.Errorf("GetTask: %v (main did not decode base here)", err)
	}
}

// An empty tag in a cancel matches only an entry whose tag is really "": a held add_tag
// without a tag key is not cancelled by RemoveTagLocal(t, "") (json_extract = ” did not
// match a missing key).
func TestCancelWithEmptyTagMatchesNothingMissing(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	must(t, s.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "牛乳"}}))
	insertRawOp(t, s, OpAddTag, "t1", `{"tag_name":"x","workspace":"w1"}`, `{}`)
	_ = s.RemoveTagLocal(ctx, "t1", "", testNow)
	if n := countRows(t, s, "SELECT count(*) FROM outbox WHERE kind = 'add_tag'"); n != 1 {
		t.Errorf("add_tag entries = %d, want 1 (a tag-less entry cancelled by an empty tag)", n)
	}
}
