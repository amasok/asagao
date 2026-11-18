package mcpserver

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/store"
)

// update_comment / delete_comment change only my own comments; delete_attachment deletes
// an attachment. Each is queued (offline here), and get_task shows the result and the
// comments' reactions. The delete tools are marked destructive.
func TestCommentEditTools(t *testing.T) {
	st := seed(t)
	ctx := context.Background()
	if err := st.ReplaceStories(ctx, "t1", []asana.Story{
		{GID: "c-me", Type: "comment", Text: "元の文", CreatedBy: &asana.Ref{GID: "me", Name: "Me"},
			ReactionSummary: []asana.Reaction{{EmojiBase: "👍", Count: 3}}},
		{GID: "c-me2", Type: "comment", Text: "消す", CreatedBy: &asana.Ref{GID: "me", Name: "Me"}},
		{GID: "c-other", Type: "comment", Text: "ほかの人", CreatedBy: &asana.Ref{GID: "u2", Name: "Partner"}},
	}, testNow); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceAttachments(ctx, "t1", []asana.Attachment{{GID: "a1", Name: "図面.pdf"}}); err != nil {
		t.Fatal(err)
	}
	cs := connect(t, &Server{Store: st})

	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range res.Tools {
		switch tool.Name {
		case "update_comment", "delete_comment", "delete_attachment":
			if tool.Annotations == nil || tool.Annotations.DestructiveHint == nil || !*tool.Annotations.DestructiveHint {
				t.Errorf("%s is not marked destructive", tool.Name)
			}
		}
	}

	before := take(t, st, "t1")
	for _, tc := range []struct {
		tool string
		args map[string]any
		want string
	}{
		{"update_comment", map[string]any{"gid": "c-other", "text": "乗っ取り"}, "自分のコメント"},
		{"delete_comment", map[string]any{"gid": "c-other"}, "自分のコメント"},
		{"update_comment", map[string]any{"gid": "nope", "text": "x"}, "nope"},
		{"delete_attachment", map[string]any{"gid": "nope"}, "nope"},
	} {
		if msg := toolError(call(t, cs, tc.tool, tc.args, nil)); !strings.Contains(msg, tc.want) {
			t.Errorf("%s %v: error %q, want %q", tc.tool, tc.args, msg, tc.want)
		}
	}
	if after := take(t, st, "t1"); !reflect.DeepEqual(before, after) {
		t.Fatalf("a refused change changed the store:\nbefore %+v\nafter  %+v", before, after)
	}

	var w WriteOutput
	call(t, cs, "update_comment", map[string]any{"gid": "c-me", "text": "**直した**"}, &w)
	if w.Sent || w.Note == "" {
		t.Errorf("offline update_comment: %+v", w)
	}
	call(t, cs, "delete_comment", map[string]any{"gid": "c-me2"}, &w)
	call(t, cs, "delete_attachment", map[string]any{"gid": "a1"}, &w)
	ops, err := st.ListOps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 3 || ops[0].Kind != store.OpUpdateStory || ops[1].Kind != store.OpDeleteStory || ops[2].Kind != store.OpDeleteAttachment {
		t.Fatalf("outbox %+v", ops)
	}

	var d GetTaskOutput
	call(t, cs, "get_task", map[string]any{"gid": "t1", "offline": true}, &d)
	var gids []string
	for _, c := range d.Comments {
		gids = append(gids, c.GID)
		switch c.GID {
		case "c-me":
			if c.Markdown != "**直した**" || !c.Pending || !c.Mine || len(c.Reactions) != 1 || c.Reactions[0].Count != 3 {
				t.Errorf("c-me: %+v", c)
			}
		case "c-other":
			if c.Mine {
				t.Errorf("someone else's comment is mine: %+v", c)
			}
		}
	}
	if !reflect.DeepEqual(gids, []string{"c-me", "c-other"}) || d.Task == nil || len(d.Task.Attachments) != 0 {
		t.Errorf("comments %v, task %+v", gids, d.Task)
	}
}
