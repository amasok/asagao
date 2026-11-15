package sync

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asana-tui/internal/store"
)

// #27 (tester) attacks on the events sync, with the fake Asana of events_test.go.

// More pages than one sync reads (maxEventPages): the rest is read by the next sync from
// the saved token, and every task named on any page is fetched exactly once.
func TestEventsMorePagesThanOneSyncReads(t *testing.T) {
	e := setupEvents(t)
	e.run(t, 0) // first: 412, full fetch, token T0
	const pages = maxEventPages + 5
	for k := 0; k < pages; k++ {
		e.fake.setTask(fmt.Sprint("e", k), fmt.Sprint("page ", k), "")
	}
	e.fake.events = func(token string) (int, string) {
		var k int
		if _, err := fmt.Sscanf(token, "T%d", &k); err != nil {
			return http.StatusPreconditionFailed, `{"errors":[{"message":"bad"}],"sync":"TX"}`
		}
		if k >= pages {
			return 200, eventsJSON(nil, token, false)
		}
		return 200, eventsJSON([]string{taskEvent(fmt.Sprint("e", k), "changed")}, fmt.Sprint("T", k+1), k+1 < pages)
	}
	res := e.run(t, 5*time.Minute)
	if res.Fallbacks != 0 {
		t.Fatalf("first events sync fell back: %+v\n%v", res, e.fake.log)
	}
	if got := e.token(t); got != fmt.Sprint("T", maxEventPages) {
		t.Fatalf("token after one sync = %q, want T%d", got, maxEventPages)
	}
	if n := len(e.fake.calls("GET /events")); n != maxEventPages {
		t.Fatalf("%d event pages read in one sync, want %d", n, maxEventPages)
	}
	first := len(e.fake.calls("BATCH "))
	e.run(t, 5*time.Minute)
	if got := e.token(t); got != fmt.Sprint("T", pages) {
		t.Fatalf("token after the second sync = %q, want T%d", got, pages)
	}
	if total := first + len(e.fake.calls("BATCH ")); total != pages {
		t.Fatalf("%d tasks fetched for %d named", total, pages)
	}
	for k := 0; k < pages; k++ {
		if got := e.taskName(t, fmt.Sprint("e", k)); got != fmt.Sprint("page ", k) {
			t.Errorf("e%d = %q: event on page %d lost", k, got, k)
		}
	}
}

// The same task named by many events (on several pages) is fetched once; events that are
// not about tasks, or name no gid, fetch nothing.
func TestEventsDuplicatesAndNoise(t *testing.T) {
	e := setupEvents(t)
	e.run(t, 0)
	e.fake.setTask("t1", "changed", "")
	var page1, page2 []string
	for i := 0; i < 40; i++ {
		page1 = append(page1, taskEvent("t1", "changed"))
		page2 = append(page2, taskEvent("t1", "changed"))
	}
	page2 = append(page2,
		`{"action":"added","resource":{"gid":"st1","resource_type":"story"},"parent":{"gid":"t2","resource_type":"task"}}`,
		`{"action":"added","resource":{"gid":"a1","resource_type":"attachment"}}`,
		`{"action":"changed","resource":{"gid":"","resource_type":"task"}}`,
		`{"action":"changed","resource":{"resource_type":"task"}}`,
		`{"action":"changed","resource":{"gid":"x","resource_type":"unknown_kind"}}`,
		`{"action":"changed"}`,
	)
	e.fake.events = func(token string) (int, string) {
		switch token {
		case "T0":
			return 200, eventsJSON(page1, "T1", true)
		case "T1":
			return 200, eventsJSON(page2, "T2", false)
		}
		return 200, eventsJSON(nil, token, false)
	}
	res := e.run(t, 5*time.Minute)
	if res.Fallbacks != 0 {
		t.Fatalf("fell back: %+v\n%v", res, e.fake.log)
	}
	if got := e.fake.calls("BATCH "); len(got) != 1 || got[0] != "BATCH t1" {
		t.Fatalf("batch = %v, want t1 once", got)
	}
	if got := e.fake.calls("POST /batch"); len(got) != 1 {
		t.Fatalf("batch requests = %v", got)
	}
	if e.taskName(t, "t1") != "changed" || e.token(t) != "T2" {
		t.Fatalf("t1 %q token %q", e.taskName(t, "t1"), e.token(t))
	}
}

// A task removed from the project (still in Asana, now in no project of ours) no longer
// shows in the project's list, and a deleted one is gone from the store.
func TestEventsRemovedAndDeletedTasks(t *testing.T) {
	e := setupEvents(t)
	e.run(t, 0)
	ctx := context.Background()
	if tasks, err := e.st.ListTasks(ctx, store.TaskFilter{ProjectGID: "p1"}); err != nil || len(tasks) != 2 {
		t.Fatalf("setup: %d tasks in p1, %v", len(tasks), err)
	}
	e.fake.mu.Lock()
	e.fake.tasks["t1"] = `{"gid":"t1","name":"One","memberships":[]}`
	delete(e.fake.tasks, "t2")
	e.fake.mu.Unlock()
	e.fake.events = func(token string) (int, string) {
		if token == "T0" {
			return 200, eventsJSON([]string{taskEvent("t1", "removed"), taskEvent("t2", "deleted")}, "T1", false)
		}
		return 200, eventsJSON(nil, token, false)
	}
	e.run(t, 5*time.Minute)
	tasks, err := e.st.ListTasks(ctx, store.TaskFilter{ProjectGID: "p1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tk := range tasks {
		t.Errorf("%s (%s) still listed in p1", tk.GID, tk.Name)
	}
	if _, err := e.st.GetTask(ctx, "t2"); err == nil {
		t.Error("deleted t2 still in the store")
	}
	if e.taskName(t, "t1") != "One" {
		t.Error("t1 (removed from the project, not deleted) should stay in the store until pruned")
	}
}

// A 412 on the second page (the token expired while reading): the first page's changes
// are applied, the project is fetched with modified_since (not in full: the project was
// known), and the new token from the 412 is saved.
func TestEvents412OnSecondPage(t *testing.T) {
	e := setupEvents(t)
	e.run(t, 0)
	e.fake.setTask("t1", "from page 1", "")
	e.fake.events = func(token string) (int, string) {
		switch token {
		case "T0":
			return 200, eventsJSON([]string{taskEvent("t1", "changed")}, "T1", true)
		case "T1":
			return http.StatusPreconditionFailed, `{"errors":[{"message":"Sync token invalid or too old"}],"sync":"NEW"}`
		}
		return 200, eventsJSON(nil, token, false)
	}
	res := e.run(t, 5*time.Minute)
	if res.Fallbacks != 1 {
		t.Fatalf("fallbacks = %d\n%v", res.Fallbacks, e.fake.log)
	}
	got := e.fake.calls("GET /tasks project=p1")
	if len(got) != 1 || !strings.Contains(got[0], "modified_since=") {
		t.Fatalf("fallback fetch = %v, want one with modified_since", got)
	}
	if e.taskName(t, "t1") != "from page 1" || e.token(t) != "NEW" {
		t.Fatalf("t1 %q token %q", e.taskName(t, "t1"), e.token(t))
	}
}

// When the hourly poll fetches the project anyway, tasks the events name that the poll
// already returned are not fetched a second time.
func TestEventsPollDoesNotFetchTwice(t *testing.T) {
	e := setupEvents(t)
	e.run(t, 0)
	e.fake.events = func(token string) (int, string) {
		return 200, eventsJSON([]string{taskEvent("t1", "changed"), taskEvent("t2", "changed")}, "T1", false)
	}
	res := e.run(t, time.Hour)
	if !res.Poll {
		t.Fatalf("expected a poll: %+v", res)
	}
	if got := e.fake.calls("BATCH "); len(got) != 0 {
		t.Fatalf("tasks fetched again after the poll returned them: %v", got)
	}
}

// A task added to the project has subtasks; fetching them fails (5xx after the project's
// token was saved), so the sync fails. The next sync, before any poll, must still fetch
// those subtasks: "if anything failed, the next sync reads the same events".
func TestEventsSubtaskFailureIsRetriedNextSync(t *testing.T) {
	e := setupEvents(t)
	failSubtasks := false
	wrap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failSubtasks && r.URL.Path == "/tasks/t3/subtasks" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		e.fake.ServeHTTP(w, r)
	}))
	defer wrap.Close()
	e.s.API.BaseURL = wrap.URL
	e.run(t, 0)
	e.fake.setTask("t3", "new with subtasks", `,"num_subtasks":1`)
	e.fake.events = func(token string) (int, string) {
		if token == "T0" {
			return 200, eventsJSON([]string{taskEvent("t3", "added")}, "T1", false)
		}
		return 200, eventsJSON(nil, token, false)
	}
	failSubtasks = true
	e.now = e.now.Add(5 * time.Minute)
	if _, err := e.s.Run(context.Background(), false); err == nil {
		t.Fatal("setup: the sync should fail on the subtasks")
	}
	failSubtasks = false
	res := e.run(t, 5*time.Minute)
	if res.Poll || res.Full {
		t.Fatalf("setup: expected a plain events sync: %+v", res)
	}
	if got := e.fake.calls("GET /tasks/t3/subtasks"); len(got) == 0 {
		t.Fatalf("t3's subtasks never fetched after the failed sync (token %q)\n%v", e.token(t), e.fake.log)
	}
}
