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
	status   int            // if set, GET /sections/s1/tasks answers it
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
			if of.status != 0 {
				w.WriteHeader(of.status)
				fmt.Fprint(w, `{"errors":[{"message":"no"}]}`)
				return true
			}
			list(w, of.section)
		case r.URL.Path == "/tasks/t1/subtasks":
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

// A section Asana no longer gives (deleted meanwhile, or not ours to see) keeps the order
// known; the sync goes on. Another failure fails the sync, like any other fetch.
func TestPullSectionOrderUnavailable(t *testing.T) {
	e, of := setupOrder(t)
	e.run(t, 0)
	token := "T0"
	e.fake.events = func(got string) (int, string) {
		if got == token {
			return http.StatusOK, eventsJSON([]string{taskEvent("t2", "changed")}, token+"x", false)
		}
		return http.StatusOK, eventsJSON(nil, got, false)
	}
	of.section = []string{"t1", "t2", "t3"}
	for _, status := range []int{http.StatusNotFound, http.StatusForbidden} {
		of.status = status
		e.run(t, 5*60e9)
		e.wantOrder(t, store.TaskFilter{SectionGID: "s1"}, "t3", "t2", "t1")
		token += "x"
	}

	of.status = http.StatusBadRequest
	e.now = e.now.Add(5 * 60e9)
	if _, err := e.s.Run(context.Background(), false); err == nil {
		t.Fatal("a failed listing of the order was not reported")
	}
	if e.token(t) != token { // the events are read again next time
		t.Fatalf("token saved after a failure: %q", e.token(t))
	}
	of.status = 0
	e.run(t, 5*60e9)
	e.wantOrder(t, store.TaskFilter{SectionGID: "s1"}, "t1", "t2", "t3")
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
