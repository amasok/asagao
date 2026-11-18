package sync

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	asana "github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/store"
)

// taskOpsHook answers the requests of #32 the fake server does not know: DELETE /tasks/{gid}
// (deleteStatus, 0 for 200) and POST /tasks/{gid}/setParent.
func taskOpsHook(ws *writeServer, deleteStatus *int) func(rw http.ResponseWriter, r *http.Request) bool {
	return func(rw http.ResponseWriter, r *http.Request) bool {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		switch {
		case r.Method == http.MethodDelete && len(parts) == 2 && parts[0] == "tasks":
			if *deleteStatus != 0 {
				rw.WriteHeader(*deleteStatus)
				fmt.Fprint(rw, `{"errors":[{"message":"x"}]}`)
				return true
			}
			delete(ws.tasks, parts[1])
			fmt.Fprint(rw, `{"data":{}}`)
			return true
		case r.Method == http.MethodPost && len(parts) == 3 && parts[0] == "tasks" && parts[2] == "setParent":
			fmt.Fprint(rw, `{"data":{}}`)
			return true
		}
		return false
	}
}

func hasCall(ws *writeServer, call string) bool {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	return slices.Contains(ws.calls, call)
}

// #32: a queued delete is sent, and the task leaves the store. A 404 (deleted already, by
// this entry sent before or by someone else) is done too, and not reported as dropped.
func TestPushDeleteTask(t *testing.T) {
	for _, status := range []int{0, http.StatusNotFound} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			ws, _, s, st := setupPush(t)
			ws.hook = taskOpsHook(ws, &status)
			ctx := context.Background()
			if _, err := st.DeleteTaskLocal(ctx, "t1", now); err != nil {
				t.Fatal(err)
			}
			res, err := s.Push(ctx)
			if err != nil || res.Sent != 1 || res.Failed != 0 || len(res.Discarded) != 0 || res.Remaining != 0 {
				t.Fatalf("push = %+v %v\n%v", res, err, ws.calls)
			}
			if !hasCall(ws, "DELETE /tasks/t1 ") {
				t.Errorf("calls %v", ws.calls)
			}
			if _, err := st.GetTask(ctx, "t1"); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("task still stored: %v", err)
			}
			if ds, _ := st.PendingDiscards(ctx); len(ds) != 0 {
				t.Errorf("discards %+v", ds)
			}
		})
	}
}

// A 5xx may have deleted it: the entry is checked and sent again (a delete is harmless to
// repeat), and the 404 it then gets completes it.
func TestPushDeleteTaskAfterA5xx(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	if _, err := st.DeleteTaskLocal(ctx, "t1", now); err != nil {
		t.Fatal(err)
	}
	// the first delete is applied, but answered 500
	first := true
	ws.hook = func(rw http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodDelete && first {
			first = false
			delete(ws.tasks, "t1")
			rw.WriteHeader(http.StatusInternalServerError)
			return true
		}
		if r.Method == http.MethodDelete {
			rw.WriteHeader(http.StatusNotFound)
			fmt.Fprint(rw, `{"errors":[{"message":"x"}]}`)
			return true
		}
		return false
	}
	for i := 0; i < 2; i++ {
		if _, err := s.Push(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if ops, _ := st.ListOps(ctx); len(ops) != 0 {
		t.Fatalf("outbox %+v", ops)
	}
	if _, err := st.GetTask(ctx, "t1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("task still stored: %v", err)
	}
	if ds, _ := st.PendingDiscards(ctx); len(ds) != 0 {
		t.Errorf("discards %+v", ds)
	}
}

func TestPushSetParent(t *testing.T) {
	ws, _, s, st := setupPush(t)
	status := 0
	ws.hook = taskOpsHook(ws, &status)
	ctx := context.Background()
	ws.addTask("t2", "B", "p1")
	ws.addTask("t3", "C", "")
	if err := st.UpsertTasks(ctx, []asana.Task{{GID: "t2", Name: "B"}, {GID: "t3", Name: "C", Parent: &asana.Ref{GID: "t1"}}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetParentLocal(ctx, "t2", "t1", store.Position{Before: "t3"}, now); err != nil {
		t.Fatal(err)
	}
	if err := st.SetParentLocal(ctx, "t3", "", store.Position{}, now); err != nil {
		t.Fatal(err)
	}
	res, err := s.Push(ctx)
	if err != nil || res.Sent != 2 || res.Failed != 0 {
		t.Fatalf("push = %+v %v\n%v", res, err, ws.calls)
	}
	for _, c := range []string{
		`POST /tasks/t2/setParent {"data":{"insert_before":"t3","parent":"t1"}}`,
		`POST /tasks/t3/setParent {"data":{"parent":null}}`,
	} {
		if !hasCall(ws, c) {
			t.Errorf("no %s in\n%s", c, strings.Join(ws.calls, "\n"))
		}
	}
}

// A place in the section goes with the move, alone or in a batch.
func TestPushMoveToAPlace(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	ws.addTask("t2", "B", "p1")
	ws.tasks["t2"].Section = "s1"
	if err := st.ReplaceSections(ctx, "p1", []asana.Section{{GID: "s1", Name: "A"}, {GID: "s2", Name: "B"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertTasks(ctx, []asana.Task{{GID: "t2", Name: "B",
		Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}, Section: &asana.Ref{GID: "s1"}}}}}); err != nil {
		t.Fatal(err)
	}
	if err := st.MoveTaskAtLocal(ctx, "t1", "p1", "s1", store.Position{After: "t2"}, now); err != nil {
		t.Fatal(err)
	}
	res, err := s.Push(ctx)
	if err != nil || res.Sent != 1 {
		t.Fatalf("push = %+v %v\n%v", res, err, ws.calls)
	}
	if c := `POST /sections/s1/addTask {"data":{"insert_after":"t2","task":"t1"}}`; !hasCall(ws, c) {
		t.Errorf("no %s in\n%s", c, strings.Join(ws.calls, "\n"))
	}
	// two moves next to each other go in one batch
	ws.calls = nil
	if err := st.MoveTaskAtLocal(ctx, "t1", "p1", "s2", store.Position{}, now); err != nil {
		t.Fatal(err)
	}
	if err := st.MoveTaskAtLocal(ctx, "t2", "p1", "s2", store.Position{Before: "t1"}, now); err != nil {
		t.Fatal(err)
	}
	if res, err := s.Push(ctx); err != nil || res.Sent != 2 {
		t.Fatalf("push = %+v %v\n%v", res, err, ws.calls)
	}
	if !slices.ContainsFunc(ws.outer, func(c string) bool { return c == "POST /batch" }) {
		t.Errorf("not batched: %v", ws.outer)
	}
	if c := `POST /sections/s2/addTask {"data":{"insert_before":"t1","task":"t2"}}`; !hasCall(ws, c) {
		t.Errorf("no %s in\n%s", c, strings.Join(ws.calls, "\n"))
	}
}
