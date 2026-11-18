package sync

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

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
