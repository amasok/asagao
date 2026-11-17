package sync

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/richtext"
	"github.com/amasok/asagao/internal/store"
)

// Comments and status updates queued before #63 hold headings, rules and images (they were
// converted like task notes), which Asana rejects there with 400. Push sends them as they are
// converted now, whether still pending or already left failed by that 400, so they reach
// Asana without the user doing anything (#71).
func TestPushFitsRichTextQueuedBefore63(t *testing.T) {
	ctx := context.Background()
	fake := &layoutAsana{sent: map[string]string{}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "T"}, {GID: "t2", Name: "U"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertProjects(ctx, "w1", []asana.Project{{GID: "p1", Name: "P"}, {GID: "p2", Name: "Q"}}, "me"); err != nil {
		t.Fatal(err)
	}
	md := "# 予定\n\n明日やる\n\n---\n\n## 準備\n\n![図](asana:42)"
	// what the versions before #63 queued: the conversion of task notes
	oldHTML, oldPlain := richtext.TaskNotes.FromMarkdown(md), richtext.TaskNotes.PlainFromMarkdown(md)
	if !layoutTag.MatchString(oldHTML) {
		t.Fatalf("not the old form: %q", oldHTML)
	}
	me := store.UserRef{GID: "me"}
	if _, err := st.AddRichCommentLocal(ctx, "t1", oldPlain, oldHTML, me, now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddRichCommentLocal(ctx, "t2", oldPlain, oldHTML, me, now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateStatusLocal(ctx, "p1", "on_track", "週報", oldPlain, oldHTML, me, now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateStatusLocal(ctx, "p2", "on_track", "月報", oldPlain, oldHTML, me, now); err != nil {
		t.Fatal(err)
	}
	// the second comment and the second status update were already sent and rejected
	ops, err := st.ListOps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range ops {
		if op.TaskGID == "t2" || op.TaskGID == "p2" {
			if err := st.MarkOpFailed(ctx, op.ID, &asana.Error{Status: 400}); err != nil {
				t.Fatal(err)
			}
		}
	}

	api := asana.NewClient(srv.Client())
	api.BaseURL = srv.URL
	res, err := (&Syncer{API: api, Store: st}).Push(ctx)
	if len(fake.rejected) > 0 {
		t.Fatalf("Asana rejected: %v", fake.rejected)
	}
	if err != nil || res.Sent != 4 || res.Failed != 0 || res.Remaining != 0 {
		t.Fatalf("push = %+v %v (sent %q)", res, err, fake.sent)
	}
	if ops, _ := st.ListOps(ctx); len(ops) != 0 {
		t.Fatalf("left in the outbox: %+v", ops)
	}
	for _, k := range []string{"POST /tasks/t1/stories html_text", "POST /tasks/t2/stories html_text", "POST /status_updates html_text"} {
		for _, want := range []string{"<strong>予定</strong>", "明日やる", "---", "<strong>準備</strong>", "](asana:42)"} {
			if !strings.Contains(fake.sent[k], want) {
				t.Errorf("%s lost %q: %q", k, want, fake.sent[k])
			}
		}
	}
}

// What Push does not touch: entries that are not comments or status updates, rich text with
// nothing Asana rejects, and entries in conflict.
func TestFitQueuedRichTextLeavesOthers(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "T", HTMLNotes: "<body>old</body>"}}); err != nil {
		t.Fatal(err)
	}
	md := "# 予定\n\n---"
	notes := richtext.TaskNotes.FromMarkdown(md)
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"html_notes": notes}, now); err != nil {
		t.Fatal(err)
	}
	plain := "<body><strong>太字</strong>だけ</body>"
	if _, err := st.AddRichCommentLocal(ctx, "t1", "太字だけ", plain, store.UserRef{GID: "me"}, now); err != nil {
		t.Fatal(err)
	}
	before, err := st.ListOps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	n, err := st.FitQueuedRichText(ctx)
	if err != nil || n != 0 {
		t.Fatalf("FitQueuedRichText = %d, %v", n, err)
	}
	after, _ := st.ListOps(ctx)
	if len(after) != len(before) {
		t.Fatalf("ops = %+v", after)
	}
	for i := range before {
		if !mapsEqualJSON(before[i].Payload, after[i].Payload) || before[i].State != after[i].State {
			t.Fatalf("changed: %+v -> %+v", before[i], after[i])
		}
	}
}

func mapsEqualJSON(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}
