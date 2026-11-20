package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"testing"

	"github.com/amasok/asagao/internal/store"
)

// #69: real Asana emits no Events on intra-section reorders. When an external reorder
// left the cache stale (e.g. cache [t3, t2, t1] while Asana became [t1, t3, t2]), pushing
// our own move (t2 before t1) refreshes the section's order directly so the cache matches
// Asana ([t2, t1, t3]) without waiting for a full sync or any Event.
func TestPushMoveRefreshesSectionOrderAfterExternalReorder(t *testing.T) {
	e, of := setupOrder(t)
	ctx := context.Background()
	e.run(t, 0)
	e.wantOrder(t, store.TaskFilter{SectionGID: "s1"}, "t3", "t2", "t1")

	// Simulate an external reorder on Asana ([t1, t3, t2]) with zero Events.
	e.fake.mu.Lock()
	of.section = []string{"t1", "t3", "t2"}
	e.fake.mu.Unlock()
	e.fake.events = func(got string) (int, string) {
		return http.StatusOK, eventsJSON(nil, got, false)
	}
	e.run(t, 5*60e9)
	e.wantOrder(t, store.TaskFilter{SectionGID: "s1"}, "t3", "t2", "t1")

	of.take(e)
	var addCalls []string
	prevHook := e.fake.hook
	e.fake.setHook(func(w http.ResponseWriter, r *http.Request) bool {
		// e.fake.ServeHTTP already holds e.fake.mu here.
		if r.Method == http.MethodPost && r.URL.Path == "/sections/s1/addTask" {
			var body struct{ Data map[string]any }
			_ = json.NewDecoder(r.Body).Decode(&body)
			addCalls = append(addCalls, fmt.Sprint(body.Data))
			task, _ := body.Data["task"].(string)
			before, _ := body.Data["insert_before"].(string)
			of.section = slices.DeleteFunc(of.section, func(g string) bool { return g == task })
			if idx := slices.Index(of.section, before); idx >= 0 {
				of.section = slices.Insert(of.section, idx, task)
			} else {
				of.section = slices.Insert(of.section, 0, task)
			}
			fmt.Fprint(w, `{"data":{}}`)
			return true
		}
		if prevHook != nil {
			return prevHook(w, r)
		}
		return false
	})

	if err := e.st.MoveTaskAtLocal(ctx, "t2", "p1", "s1", store.Position{Before: "t1"}, e.now); err != nil {
		t.Fatal(err)
	}
	res, err := e.s.Push(ctx)
	if err != nil || res.Sent != 1 || res.Failed != 0 {
		t.Fatalf("push = %+v, %v", res, err)
	}
	if len(addCalls) != 1 {
		t.Fatalf("addCalls = %v", addCalls)
	}
	if sec, _, _ := of.take(e); sec != 1 {
		t.Fatalf("SectionTaskOrder calls = %d, want 1", sec)
	}
	e.wantOrder(t, store.TaskFilter{SectionGID: "s1"}, "t2", "t1", "t3")
}

// If SectionTaskOrder fails transiently (500) right after pushMove, Push still succeeds
// and records the section in PendingSectionOrders so the next Run picks up Asana's order.
// A 404 leaves PendingSectionOrders clean.
func TestPushMoveSectionOrderFailureQueuesPendingOrIgnores404(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusNotFound} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			e, of := setupOrder(t)
			ctx := context.Background()
			e.run(t, 0)
			prevHook := e.fake.hook
			e.fake.setHook(func(w http.ResponseWriter, r *http.Request) bool {
				if r.Method == http.MethodPost && r.URL.Path == "/sections/s1/addTask" {
					fmt.Fprint(w, `{"data":{}}`)
					return true
				}
				if prevHook != nil {
					return prevHook(w, r)
				}
				return false
			})
			e.fake.mu.Lock()
			of.status = status
			of.section = []string{"t2", "t1", "t3"}
			e.fake.mu.Unlock()

			if err := e.st.MoveTaskAtLocal(ctx, "t2", "p1", "s1", store.Position{Before: "t1"}, e.now); err != nil {
				t.Fatal(err)
			}
			res, err := e.s.Push(ctx)
			if err != nil || res.Sent != 1 || res.Failed != 0 {
				t.Fatalf("push = %+v, %v", res, err)
			}
			pending, _ := e.st.PendingSectionOrders(ctx, "p1")
			if status == http.StatusInternalServerError {
				if pending != "s1" {
					t.Fatalf("pending = %q, want s1", pending)
				}
				// Next sync with 0 events picks up the pending section.
				e.fake.mu.Lock()
				of.status = 0
				e.fake.mu.Unlock()
				e.fake.events = func(got string) (int, string) { return http.StatusOK, eventsJSON(nil, got, false) }
				if r := e.run(t, 5*60e9); r.OrderPending != 0 {
					t.Fatalf("OrderPending = %d", r.OrderPending)
				}
				e.wantOrder(t, store.TaskFilter{SectionGID: "s1"}, "t2", "t1", "t3")
			} else if pending != "" {
				t.Fatalf("404 dirtied PendingSectionOrders: %q", pending)
			}
		})
	}
}

// Batch KindMoveTask entries also refresh the target section's order right after pushing.
func TestPushBatchMoveRefreshesSectionOrder(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	gids := addBatchTasks(t, ws, st, 2)
	// Override GET /sections/s2/tasks to return an explicit order [b2, b1].
	prev := ws.hook
	var secCalls int
	ws.hook = func(rw http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodGet && r.URL.Path == "/sections/s2/tasks" {
			secCalls++
			fmt.Fprint(rw, `{"data":[{"gid":"b2"},{"gid":"b1"}]}`)
			return true
		}
		if prev != nil {
			return prev(rw, r)
		}
		return false
	}
	for _, g := range gids {
		if err := st.MoveTaskLocal(ctx, g, "p1", "s2", now); err != nil {
			t.Fatal(err)
		}
	}
	res, err := s.Push(ctx)
	if err != nil || res.Sent != 2 || res.Failed != 0 {
		t.Fatalf("push = %+v, %v", res, err)
	}
	if secCalls != 1 {
		t.Fatalf("GET /sections/s2/tasks calls = %d, want 1 (deduplicated)", secCalls)
	}
	ts, err := st.ListTasks(ctx, store.TaskFilter{SectionGID: "s2"})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, x := range ts {
		got = append(got, x.GID)
	}
	if !slices.Equal(got, []string{"b2", "b1"}) {
		t.Fatalf("section order after batch move = %v, want [b2 b1]", got)
	}
}
