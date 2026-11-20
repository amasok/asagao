package sync

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/store"
)

// Failing statuses on edit and delete keep the entry queued and the local text; the delete
// is not shown as done. After the failure clears, a push finishes the job.
func TestAttackCommentEditFailures(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusPaymentRequired, http.StatusInternalServerError, http.StatusBadGateway} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			ws, _, s, st := setupPush(t)
			ctx := context.Background()
			myComments(t, st, "c1", "c2")
			if err := st.EditCommentLocal(ctx, "c1", "直した", "", now); err != nil {
				t.Fatal(err)
			}
			if err := st.DeleteCommentLocal(ctx, "c2", now); err != nil {
				t.Fatal(err)
			}
			code := status
			inner := storyEditHook(nil)
			ws.hook = func(rw http.ResponseWriter, r *http.Request) bool {
				if code != 0 && strings.HasPrefix(r.URL.Path, "/stories/") {
					rw.WriteHeader(code)
					fmt.Fprint(rw, `{"errors":[{"message":"x"}]}`)
					return true
				}
				return inner(rw, r)
			}
			_, _ = s.Push(ctx)
			if ops, _ := st.ListOps(ctx); len(ops) != 2 {
				t.Fatalf("entries after failure: %+v", ops)
			}
			d, _ := st.GetTask(ctx, "t1")
			var texts []string
			for _, x := range d.Stories {
				texts = append(texts, x.GID+":"+x.Text)
			}
			if len(texts) != 1 || texts[0] != "c1:直した" {
				t.Errorf("local view %v", texts)
			}
			if status == http.StatusPaymentRequired {
				return // 402 is a permanent failure: the entries stay failed for the user to retry or discard
			}
			code = 0
			for i := 0; i < 3; i++ {
				_, _ = s.Push(ctx)
			}
			if ops, _ := st.ListOps(ctx); len(ops) != 0 {
				t.Errorf("entries after recovery: %+v", ops)
			}
			if len(writeCalls(ws, "DELETE /stories/c2")) == 0 {
				t.Errorf("no delete sent: %v", ws.calls)
			}
		})
	}
}

// An edit followed by a delete of an unsent comment sends nothing about it.
func TestAttackUnsentCommentEditDeleteSendsNothing(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	ws.hook = storyEditHook(nil)
	gid, err := st.AddCommentLocal(ctx, "t1", "下書き", store_UserRef(), now)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.EditCommentLocal(ctx, gid, "直す", "", now); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteCommentLocal(ctx, gid, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Push(ctx); err != nil {
		t.Fatal(err)
	}
	if c := writeCalls(ws, "POST /tasks/t1/stories", "PUT /stories", "DELETE /stories"); len(c) != 0 {
		t.Fatalf("sent: %v", c)
	}
}

func store_UserRef() store.UserRef { return store.UserRef{GID: "me", Name: "Me"} }

// After deleting a downloaded attachment and discarding the delete, opening it downloads again.
func TestAttackAttachmentRedownloadsAfterUndo(t *testing.T) {
	s, st, cache, downloads := attachmentAttackSetup(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":{"gid":"a1","name":"x.png","host":"asana","download_url":"https://files.example/a1","parent":{"gid":"t1"}}}`)
	})
	ctx := context.Background()
	st.SetAttachmentDir(cache) // where it downloads
	if err := st.ReplaceAttachments(ctx, "t1", []asana.Attachment{{GID: "a1", Name: "x.png", Host: "asana"}}); err != nil {
		t.Fatal(err)
	}
	if st.SetMeGID(ctx, "me") != nil {
		t.Fatal("me")
	}
	p, err := s.DownloadAttachment(ctx, "t1", "a1")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteAttachmentLocal(ctx, "a1", now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err == nil {
		t.Fatal("file kept")
	}
	ops, _ := st.ListOps(ctx)
	if err := st.DiscardOp(ctx, ops[0].ID); err != nil {
		t.Fatal(err)
	}
	p2, err := s.DownloadAttachment(ctx, "t1", "a1")
	if err != nil {
		t.Fatalf("redownload: %v", err)
	}
	if _, err := os.Stat(p2); err != nil || filepath.Dir(p2) != filepath.Clean(cache) || len(*downloads) != 2 {
		t.Fatalf("path %q %v downloads %v", p2, err, *downloads)
	}
}
