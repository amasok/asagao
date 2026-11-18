package sync

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/store"
)

// A section whose only tasks are being deleted can be deleted; its delete goes after
// theirs, and waits while one of them has not gone (Asana deletes only an empty section).
func TestDeleteSectionAfterItsTasksDeletes(t *testing.T) {
	for _, taskDelete := range []int{http.StatusOK, http.StatusForbidden} {
		t.Run(fmt.Sprint(taskDelete), func(t *testing.T) {
			f, s, st := setupSections(t)
			ctx := context.Background()
			f.hook = func(rw http.ResponseWriter, r *http.Request, _ map[string]any) bool {
				if r.Method != http.MethodDelete || r.URL.Path != "/tasks/t1" {
					return false
				}
				if taskDelete != http.StatusOK {
					rw.WriteHeader(taskDelete)
					fmt.Fprint(rw, `{"errors":[{"message":"forbidden"}]}`)
					return true
				}
				delete(f.taskSec, "t1")
				fmt.Fprint(rw, `{"data":{}}`)
				return true
			}
			if _, err := st.DeleteTaskLocal(ctx, "t1", now); err != nil {
				t.Fatal(err)
			}
			if err := st.DeleteSectionLocal(ctx, "s1", now); err != nil {
				t.Fatalf("a section with only a task being deleted: %v", err)
			}
			if _, err := s.Push(ctx); err != nil {
				t.Fatal(err)
			}
			ops, _ := st.ListOps(ctx)
			var deletes []string
			for _, c := range f.calls {
				if strings.HasPrefix(c, "DELETE ") {
					deletes = append(deletes, c)
				}
			}
			if taskDelete == http.StatusOK {
				if !slices.Equal(deletes, []string{"DELETE /tasks/t1", "DELETE /sections/s1"}) || len(ops) != 0 {
					t.Fatalf("deletes %v, outbox %+v", deletes, ops)
				}
				return
			}
			// the task's delete failed: the section's waits, unsent
			if !slices.Equal(deletes, []string{"DELETE /tasks/t1"}) {
				t.Fatalf("deletes %v", deletes)
			}
			if len(ops) != 2 || ops[0].State != store.OpFailed || ops[1].Kind != store.OpDeleteSection || ops[1].State != store.OpPending {
				t.Fatalf("outbox %+v", ops)
			}
		})
	}
}

// A subtask in the section whose parent's delete is queued counts too.
func TestDeleteSectionWaitsForAParentsDelete(t *testing.T) {
	f, s, st := setupSections(t)
	ctx := context.Background()
	f.hook = func(rw http.ResponseWriter, r *http.Request, _ map[string]any) bool {
		if r.Method == http.MethodDelete && r.URL.Path == "/tasks/p0" {
			rw.WriteHeader(http.StatusForbidden)
			fmt.Fprint(rw, `{"errors":[{"message":"forbidden"}]}`)
			return true
		}
		return false
	}
	if err := st.UpsertTasks(ctx, []asana.Task{{GID: "p0", Name: "親"}, {GID: "t1", Name: "A", Workspace: &asana.Ref{GID: "w1"},
		Parent: &asana.Ref{GID: "p0"}, Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}, Section: &asana.Ref{GID: "s1"}}}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DeleteTaskLocal(ctx, "p0", now); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteSectionLocal(ctx, "s1", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Push(ctx); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.calls {
		if strings.HasPrefix(c, "DELETE /sections/") {
			t.Fatalf("the section's delete went before its subtask's parent was deleted: %v", f.calls)
		}
	}
}
