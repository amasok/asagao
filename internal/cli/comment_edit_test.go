package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/store"
)

// withComments adds to the store at path t1's comments (c-me mine, c-other someone
// else's, with reactions) and an attachment a1.
func withComments(t *testing.T, path string) {
	t.Helper()
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	at := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	if err := st.ReplaceStories(ctx, "t1", []asana.Story{
		{GID: "c-me", Type: "comment", Text: "元の文", CreatedBy: &asana.Ref{GID: "me", Name: "Me"}, CreatedAt: at,
			ReactionSummary: []asana.Reaction{{EmojiBase: "👍", Count: 2, Reacted: true}, {EmojiBase: "🎉", Count: 1}}},
		{GID: "c-other", Type: "comment", Text: "ほかの人", CreatedBy: &asana.Ref{GID: "u2", Name: "Partner"}, CreatedAt: at},
	}, at); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceAttachments(ctx, "t1", []asana.Attachment{{GID: "a1", Name: "図面.pdf"}}); err != nil {
		t.Fatal(err)
	}
}

func queuedOps(t *testing.T, path string) []store.Op {
	t.Helper()
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ops, err := st.ListOps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return ops
}

// task show lists each comment's gid (to edit or delete it), its reactions, and the
// attachments with their gids.
func TestTaskShowCommentGIDsAndReactions(t *testing.T) {
	path := writeCLIEnv(t)
	withComments(t, path)
	out, err := runCLI(t, "task", "show", "t1", "--offline")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{"[c-me]", "[c-other]", "👍* 2  🎉 1", "添付 (1)", "図面.pdf  [a1]"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
}

// comment edit rewrites my comment and queues it; delete and detach need --yes (they
// cannot be undone once sent) and then queue the delete.
func TestCommentEditDeleteDetach(t *testing.T) {
	path := writeCLIEnv(t)
	withComments(t, path)

	out, err := runCLI(t, "comment", "edit", "c-me", "**直した**", "文")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "未送信") {
		t.Errorf("should say it is not sent yet:\n%s", out)
	}
	ops := queuedOps(t, path)
	if len(ops) != 1 || ops[0].Kind != store.OpUpdateStory || ops[0].Payload["story"] != "c-me" ||
		ops[0].Payload["text"] != "直した 文" || !strings.Contains(ops[0].Payload["html_text"].(string), "<strong>直した</strong>") {
		t.Fatalf("outbox %+v", ops)
	}

	if out, err := runCLI(t, "comment", "edit", "c-other", "乗っ取り"); err == nil || !strings.Contains(err.Error(), "自分のコメント") {
		t.Errorf("someone else's comment: %v\n%s", err, out)
	}

	for _, args := range [][]string{{"comment", "delete", "c-me"}, {"task", "detach", "a1"}} {
		out, err := runCLI(t, args...)
		if err == nil || !strings.Contains(err.Error(), "--yes") {
			t.Errorf("%v without --yes: %v\n%s", args, err, out)
		}
	}
	if n := len(queuedOps(t, path)); n != 1 {
		t.Fatalf("a delete without --yes queued something (%d entries)", n)
	}

	if out, err := runCLI(t, "comment", "delete", "c-me", "--yes"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if out, err := runCLI(t, "task", "detach", "a1", "--yes"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	ops = queuedOps(t, path)
	if len(ops) != 3 || ops[1].Kind != store.OpDeleteStory || ops[2].Kind != store.OpDeleteAttachment ||
		ops[2].Payload["attachment"] != "a1" || ops[1].TaskGID != "t1" || ops[2].TaskGID != "t1" {
		t.Fatalf("outbox %+v", ops)
	}
	out, err = runCLI(t, "task", "show", "t1", "--offline")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "[c-me]") || strings.Contains(out, "図面.pdf") {
		t.Errorf("deleted items still shown:\n%s", out)
	}
}

// With --dry-run, editing or deleting a comment and deleting an attachment change nothing
// and send nothing; a delete needs no --yes then, and says what it would do.
func TestCommentEditDryRun(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"edit", []string{"comment", "edit", "c-me", "直した"}, "コメントを「直した」に編集"},
		{"delete", []string{"comment", "delete", "c-me"}, "コメント「元の文」の削除"},
		{"detach", []string{"task", "detach", "a1"}, "添付「図面.pdf」の削除"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, requests, run := dryRunEnv(t)
			withComments(t, path)
			before := storeState(t, path)
			out, errOut, err := run(append(tc.args, "--dry-run")...)
			if err != nil {
				t.Fatalf("%v\n%s%s", err, out, errOut)
			}
			if !strings.Contains(out, "--dry-run: 次の変更をするところでした") || !strings.Contains(out, tc.want) {
				t.Errorf("should say what it would do (%q):\n%s%s", tc.want, out, errOut)
			}
			if n := requests.Load(); n != 0 {
				t.Errorf("%d requests to Asana", n)
			}
			if after := storeState(t, path); after != before {
				t.Errorf("the store changed:\nbefore %s\nafter  %s", before, after)
			}
		})
	}
}
