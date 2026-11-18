package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/store"
)

// storyEditHook answers PUT / DELETE /stories/{gid} and DELETE /attachments/{gid} like
// Asana: an edit returns the comment with the new text; gone (a gid) answers 404.
func storyEditHook(gone map[string]bool) func(rw http.ResponseWriter, r *http.Request) bool {
	return func(rw http.ResponseWriter, r *http.Request) bool {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) != 2 || (parts[0] != "stories" && parts[0] != "attachments") || r.Method == http.MethodGet {
			return false
		}
		if gone[parts[1]] {
			rw.WriteHeader(http.StatusNotFound)
			fmt.Fprint(rw, `{"errors":[{"message":"Not Found"}]}`)
			return true
		}
		if r.Method == http.MethodDelete {
			fmt.Fprint(rw, `{"data":{}}`)
			return true
		}
		b, _ := io.ReadAll(r.Body)
		var body struct{ Data map[string]string }
		_ = json.Unmarshal(b, &body)
		text, html := body.Data["text"], body.Data["html_text"]
		if html != "" {
			text = strings.NewReplacer("<body>", "", "</body>", "").Replace(html)
		}
		out, _ := json.Marshal(map[string]any{"data": map[string]any{"gid": parts[1], "type": "comment", "text": text, "html_text": html}})
		rw.Write(out)
		return true
	}
}

func myComments(t *testing.T, st *store.Store, gids ...string) {
	t.Helper()
	var stories []asana.Story
	for _, g := range gids {
		stories = append(stories, asana.Story{GID: g, Type: "comment", Text: "元 " + g, CreatedBy: &asana.Ref{GID: "me", Name: "Me"}, CreatedAt: now})
	}
	if err := st.ReplaceStories(context.Background(), "t1", stories, now); err != nil {
		t.Fatal(err)
	}
}

func writeCalls(ws *writeServer, prefix ...string) []string {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	var out []string
	for _, c := range ws.calls {
		for _, p := range prefix {
			if strings.HasPrefix(c, p) {
				out = append(out, c)
			}
		}
	}
	return out
}

// Edits go out as PUT /stories/{gid} with the rich text only (Asana takes one of text and
// html_text) and the text Asana returns is kept; deletes go out as DELETE.
func TestPushEditAndDeleteComment(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ws.hook = storyEditHook(nil)
	ctx := context.Background()
	myComments(t, st, "c1", "c2", "c3")
	if err := st.EditCommentLocal(ctx, "c1", "直した", "<body>直した</body>", now); err != nil {
		t.Fatal(err)
	}
	if err := st.EditCommentLocal(ctx, "c3", "平文", "", now); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteCommentLocal(ctx, "c2", now); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceAttachments(ctx, "t1", []asana.Attachment{{GID: "a1", Name: "x.png"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteAttachmentLocal(ctx, "a1", now); err != nil {
		t.Fatal(err)
	}
	res, err := s.Push(ctx)
	if err != nil || res.Sent != 4 || res.Failed != 0 {
		t.Fatalf("Push = %+v, %v", res, err)
	}
	got := writeCalls(ws, "PUT /stories", "DELETE ")
	want := []string{
		`PUT /stories/c1 {"data":{"html_text":"\u003cbody\u003e直した\u003c/body\u003e"}}`,
		`PUT /stories/c3 {"data":{"text":"平文"}}`,
		"DELETE /stories/c2 ",
		"DELETE /attachments/a1 ",
	}
	if !slices.Equal(got, want) {
		t.Errorf("requests\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if p, c, f := pendingCount(t, st); p+c+f != 0 {
		t.Errorf("left %d/%d/%d", p, c, f)
	}
	d, err := st.GetTask(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	for _, sv := range d.Stories {
		if sv.GID == "c1" && (sv.Text != "直した" || sv.Pending) {
			t.Errorf("c1 after the push: %+v", sv)
		}
		if sv.GID == "c2" {
			t.Error("the deleted comment is back")
		}
	}
}

// A delete answered 404 is what was asked (the comment or attachment is gone already): it
// is done, not failed and not reported as dropped. An edit of a comment deleted in Asana
// is dropped and reported.
func TestPushDeleteGoneIsDone(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ws.hook = storyEditHook(map[string]bool{"c1": true, "c2": true, "a1": true})
	ctx := context.Background()
	myComments(t, st, "c1", "c2")
	if err := st.DeleteCommentLocal(ctx, "c1", now); err != nil {
		t.Fatal(err)
	}
	if err := st.EditCommentLocal(ctx, "c2", "直す", "", now); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceAttachments(ctx, "t1", []asana.Attachment{{GID: "a1", Name: "x.png"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteAttachmentLocal(ctx, "a1", now); err != nil {
		t.Fatal(err)
	}
	res, err := s.Push(ctx)
	if err != nil || res.Failed != 0 || res.Sent != 2 {
		t.Fatalf("Push = %+v, %v", res, err)
	}
	if len(res.Discarded) != 1 || res.Discarded[0].Kind != store.OpUpdateStory {
		t.Errorf("discarded %+v, want the edit only", res.Discarded)
	}
	if p, c, f := pendingCount(t, st); p+c+f != 0 {
		t.Errorf("left %d/%d/%d", p, c, f)
	}
}

// An edit of a comment whose add_comment has not reached Asana waits for it (it is not
// sent while the add_comment failed), and then goes to the comment Asana created.
func TestPushEditWaitsForComment(t *testing.T) {
	ws, _, s, st := setupPush(t)
	reject := true
	edits := storyEditHook(nil)
	ws.hook = func(rw http.ResponseWriter, r *http.Request) bool {
		if reject && r.Method == http.MethodPost && r.URL.Path == "/tasks/t1/stories" {
			rw.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(rw, `{"errors":[{"message":"bad"}]}`)
			return true
		}
		return edits(rw, r)
	}
	ctx := context.Background()
	local, err := st.AddCommentLocal(ctx, "t1", "最初", store.UserRef{GID: "me", Name: "Me"}, now)
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.Push(ctx)
	if err != nil || res.Failed != 1 {
		t.Fatalf("Push = %+v, %v", res, err)
	}
	if err := st.EditCommentLocal(ctx, local, "直した", "", now); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteCommentLocal(ctx, local, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Push(ctx); err != nil {
		t.Fatal(err)
	}
	if got := writeCalls(ws, "PUT /stories", "DELETE "); len(got) != 0 {
		t.Fatalf("sent before the comment exists: %q", got)
	}
	ops, _ := st.ListOps(ctx)
	if len(ops) != 3 {
		t.Fatalf("outbox %+v", ops)
	}

	reject = false
	if err := st.RetryOp(ctx, ops[0].ID); err != nil {
		t.Fatal(err)
	}
	res, err = s.Push(ctx)
	if err != nil || res.Failed != 0 {
		t.Fatalf("Push = %+v, %v", res, err)
	}
	got := writeCalls(ws, "POST /tasks/t1/stories", "PUT /stories", "DELETE ")
	if len(got) != 4 || !strings.HasPrefix(got[2], "PUT /stories/story") || !strings.HasPrefix(got[3], "DELETE /stories/story") {
		t.Errorf("requests %q", got)
	}
	if p, c, f := pendingCount(t, st); p+c+f != 0 {
		t.Errorf("left %d/%d/%d", p, c, f)
	}
}

// A comment edit or delete may be sent twice (Asana's answer was lost): both are harmless
// to repeat, so they are sent again rather than checked.
func TestStoryEditsSafeToResend(t *testing.T) {
	_, _, _, st := setupPush(t)
	ctx := context.Background()
	for _, kind := range []string{store.OpUpdateStory, store.OpDeleteStory, store.OpDeleteAttachment} {
		safe, err := st.SafeToResend(ctx, store.Op{Kind: kind})
		if err != nil || !safe {
			t.Errorf("%s: SafeToResend = %v, %v", kind, safe, err)
		}
	}
}
