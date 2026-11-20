package sync

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/store"
)

// The order of tasks as Asana has it (#51): a full listing of the project, a section's
// listing when the events name its tasks, the subtasks' listing.

type orderFake struct {
	project  []string       // GET /tasks?project=p1, in this order
	changed  []string       // the same with modified_since
	section  []string       // GET /sections/s1/tasks, in this order
	subtasks []string       // GET /tasks/t1/subtasks, in this order
	status   int            // if set, GET /sections/s1/tasks answers it (-1: drops the connection)
	subFail  int            // if set, GET /tasks/t1/subtasks answers it
	since    []string       // completed_since of each GET /sections/s1/tasks
	hits     map[string]int // GET path -> count
}

func setupOrder(t *testing.T) (*eventsEnv, *orderFake) {
	t.Helper()
	e := setupEvents(t)
	of := &orderFake{project: []string{"t3", "t2", "t1"}, section: []string{"t3", "t2", "t1"},
		subtasks: []string{"c2", "c1"}, hits: map[string]int{}}
	in := `"memberships":[{"project":{"gid":"p1"},"section":{"gid":"s1"}}]`
	e.fake.tasks = map[string]string{
		// before #51, t1 (due) came first, then t2, t3 (by name); c1, c2 by creation
		"t1": `{"gid":"t1","name":"a","due_on":"2026-11-01","num_subtasks":2,` + in + `}`,
		"t2": `{"gid":"t2","name":"b",` + in + `}`,
		"t3": `{"gid":"t3","name":"c",` + in + `}`,
		"c1": `{"gid":"c1","name":"x","parent":{"gid":"t1"},"created_at":"2026-11-01T00:00:00Z"}`,
		"c2": `{"gid":"c2","name":"y","parent":{"gid":"t1"},"created_at":"2026-11-02T00:00:00Z"}`,
	}
	list := func(w http.ResponseWriter, gids []string) {
		var all []string
		for _, g := range gids {
			all = append(all, e.fake.tasks[g])
		}
		fmt.Fprintf(w, `{"data":[%s]}`, strings.Join(all, ","))
	}
	e.fake.setHook(func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method != http.MethodGet {
			return false
		}
		of.hits[r.URL.Path]++
		switch {
		case r.URL.Path == "/tasks" && r.URL.Query().Get("project") == "p1":
			if r.URL.Query().Get("modified_since") != "" {
				list(w, of.changed)
			} else {
				list(w, of.project)
			}
		case r.URL.Path == "/sections/s1/tasks":
			of.since = append(of.since, r.URL.Query().Get("completed_since"))
			if of.status < 0 {
				conn, _, _ := w.(http.Hijacker).Hijack()
				conn.Close()
				return true
			}
			if of.status != 0 {
				w.WriteHeader(of.status)
				fmt.Fprint(w, `{"errors":[{"message":"no"}]}`)
				return true
			}
			list(w, of.section)
		case r.URL.Path == "/tasks/t1/subtasks":
			if of.subFail != 0 {
				w.WriteHeader(of.subFail)
				fmt.Fprint(w, `{"errors":[{"message":"no"}]}`)
				return true
			}
			list(w, of.subtasks)
		default:
			return false
		}
		return true
	})
	return e, of
}

// take returns how many times each listing of the order was asked for, and forgets them.
func (of *orderFake) take(e *eventsEnv) (section, subtasks int, since []string) {
	e.fake.mu.Lock()
	defer e.fake.mu.Unlock()
	section, subtasks, since = of.hits["/sections/s1/tasks"], of.hits["/tasks/t1/subtasks"], of.since
	clear(of.hits)
	of.since = nil
	return
}

func (e *eventsEnv) wantOrder(t *testing.T, f store.TaskFilter, want ...string) {
	t.Helper()
	ts, err := e.st.ListTasks(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, x := range ts {
		got = append(got, x.GID)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("ListTasks(%+v) = %v, want %v", f, got, want)
	}
}

func (e *eventsEnv) wantSubtasks(t *testing.T, parent string, want ...string) {
	t.Helper()
	d, err := e.st.GetTask(context.Background(), parent)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, x := range d.Subtasks {
		got = append(got, x.GID)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("subtasks of %s = %v, want %v", parent, got, want)
	}
	e.wantOrder(t, store.TaskFilter{ParentGID: parent}, want...)
}

func TestPullKeepsAsanaOrder(t *testing.T) {
	e, of := setupOrder(t)
	e.run(t, 0) // the first sync lists the project in full
	e.wantOrder(t, store.TaskFilter{ProjectGID: "p1"}, "t3", "t2", "t1")
	e.wantOrder(t, store.TaskFilter{SectionGID: "s1"}, "t3", "t2", "t1")
	e.wantSubtasks(t, "t1", "c2", "c1")
	if sec, _, _ := of.take(e); sec != 0 {
		t.Fatalf("a full listing needs no section's: %d", sec)
	}

	// t1 was put first, and c1 before c2: the events name them (as changed).
	of.section, of.subtasks = []string{"t1", "t3", "t2"}, []string{"c1", "c2"}
	e.fake.events = func(token string) (int, string) {
		if token == "T0" {
			return http.StatusOK, eventsJSON([]string{taskEvent("t1", "changed"), taskEvent("c1", "changed")}, "T1", false)
		}
		return http.StatusOK, eventsJSON(nil, token, false)
	}
	e.run(t, 5*60e9)
	e.wantOrder(t, store.TaskFilter{SectionGID: "s1"}, "t1", "t3", "t2")
	e.wantSubtasks(t, "t1", "c1", "c2")
	// The section is listed as the project is kept: incomplete, and completed within
	// CompletedDays.
	if sec, subs, since := of.take(e); sec != 1 || subs != 1 || since[0] != "2026-10-30T09:05:00Z" {
		t.Fatalf("listings asked: section %d (completed_since %v), subtasks %d", sec, since, subs)
	}

	// Nothing changed: nothing more is asked.
	e.run(t, 5*60e9)
	if sec, subs, _ := of.take(e); sec != 0 || subs != 0 {
		t.Fatalf("asked with nothing changed: section %d, subtasks %d", sec, subs)
	}
}

// A section or parent Asana no longer gives (deleted meanwhile, or not ours to see) keeps
// the order known; the sync goes on.
func TestPullOrderGone(t *testing.T) {
	e, of := setupOrder(t)
	e.run(t, 0)
	token := "T0"
	e.fake.events = func(got string) (int, string) {
		if got == token {
			return http.StatusOK, eventsJSON([]string{taskEvent("t2", "changed"), taskEvent("c1", "changed")}, token+"x", false)
		}
		return http.StatusOK, eventsJSON(nil, got, false)
	}
	of.section, of.subtasks = []string{"t1", "t2", "t3"}, []string{"c1", "c2"}
	for _, status := range []int{http.StatusNotFound, http.StatusForbidden} {
		e.fake.mu.Lock()
		of.status, of.subFail = status, status
		e.fake.mu.Unlock()
		if res := e.run(t, 5*60e9); res.OrderPending != 0 {
			t.Fatalf("%d: pending %d", status, res.OrderPending)
		}
		token += "x"
		if e.token(t) != token {
			t.Fatalf("%d: token %q", status, e.token(t))
		}
		e.wantOrder(t, store.TaskFilter{SectionGID: "s1"}, "t3", "t2", "t1")
		e.wantSubtasks(t, "t1", "c2", "c1")
	}
}

// Any other failure to list the order (a 5xx or 429 that outlasted the retries, a 400)
// does not stop the sync: the order is how tasks are shown, and the next sync lists the
// section or parent again. Offline, the sync fails as with any other fetch.
func TestPullOrderFailsLater(t *testing.T) {
	e, of := setupOrder(t)
	e.run(t, 0)
	e.fake.events = func(got string) (int, string) {
		if got == "T0" {
			return http.StatusOK, eventsJSON([]string{taskEvent("t2", "changed"), taskEvent("c1", "changed")}, "T1", false)
		}
		return http.StatusOK, eventsJSON(nil, got, false)
	}
	e.fake.setTask("t2", "b2", `,"memberships":[{"project":{"gid":"p1"},"section":{"gid":"s1"}}]`)
	e.fake.mu.Lock()
	of.section, of.subtasks = []string{"t1", "t2", "t3"}, []string{"c1", "c2"}
	of.status = -1 // offline
	e.fake.mu.Unlock()
	e.now = e.now.Add(5 * 60e9)
	if _, err := e.s.Run(context.Background(), false); err == nil {
		t.Fatal("offline listing of the order not reported")
	}
	if e.token(t) != "T0" { // the events are read again next time
		t.Fatalf("token saved after going offline: %q", e.token(t))
	}

	e.fake.mu.Lock()
	of.status, of.subFail = http.StatusInternalServerError, http.StatusBadRequest
	e.fake.mu.Unlock()
	if res := e.run(t, 5*60e9); res.OrderPending != 2 {
		t.Fatalf("pending = %d", res.OrderPending)
	}
	if e.token(t) != "T1" || e.taskName(t, "t2") != "b2" {
		t.Fatalf("the rest of the sync was not kept: token %q, t2 %q", e.token(t), e.taskName(t, "t2"))
	}
	e.wantOrder(t, store.TaskFilter{SectionGID: "s1"}, "t3", "t2", "t1")
	e.wantSubtasks(t, "t1", "c2", "c1")
	if v, _ := e.st.PendingSectionOrders(context.Background(), "p1"); v == "" {
		t.Fatal("task_order:p1 not kept for the next sync")
	}
	if v, _ := e.st.PendingSubtaskOrders(context.Background()); v == "" {
		t.Fatal("subtask_order not kept for the next sync")
	}

	// The next sync lists them again, with no events.
	e.fake.mu.Lock()
	of.status, of.subFail = 0, 0
	e.fake.mu.Unlock()
	if res := e.run(t, 5*60e9); res.OrderPending != 0 {
		t.Fatalf("still pending: %d", res.OrderPending)
	}
	e.wantOrder(t, store.TaskFilter{SectionGID: "s1"}, "t1", "t2", "t3")
	e.wantSubtasks(t, "t1", "c1", "c2")
	if v, _ := e.st.PendingSectionOrders(context.Background(), "p1"); v != "" {
		t.Fatalf("task_order:p1 = %q after listing it", v)
	}
	if v, _ := e.st.PendingSubtaskOrders(context.Background()); v != "" {
		t.Fatalf("subtask_order = %q after listing it", v)
	}
	of.take(e)
	e.run(t, 5*60e9)
	if sec, subs, _ := of.take(e); sec != 0 || subs != 0 {
		t.Fatalf("listed again with nothing pending: section %d, subtasks %d", sec, subs)
	}
}

// The completed tasks, fetched after the incomplete ones (#54), take their place among
// them: their sections are listed again.
func TestPullCompletedTakeTheirPlace(t *testing.T) {
	e, of := setupOrder(t)
	e.s.Cfg = Config{CompletedDays: 14, LazyProjects: true, AllProjects: true}
	e.s.DeferCompleted = true
	e.fake.tasks["t4"] = `{"gid":"t4","name":"d","completed":true,"completed_at":"2026-11-12T00:00:00Z",` +
		`"memberships":[{"project":{"gid":"p1"},"section":{"gid":"s1"}}]}`
	of.project, of.changed, of.section = []string{"t3", "t2", "t1"}, []string{"t4"}, []string{"t3", "t4", "t2", "t1"}
	e.run(t, 0)
	e.wantOrder(t, store.TaskFilter{SectionGID: "s1"}, "t3", "t2", "t1")
	if _, err := e.s.FetchCompleted(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.wantOrder(t, store.TaskFilter{SectionGID: "s1"}, "t3", "t4", "t2", "t1")
	if sec, _, since := of.take(e); sec != 1 || since[0] != "2026-10-30T09:00:00Z" {
		t.Fatalf("section listed %d times, completed_since %v", sec, since)
	}
}
