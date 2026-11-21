package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/domain/plan"
	"github.com/amasok/asagao/internal/store"
	"github.com/amasok/asagao/internal/ui/format"
)

// #62 attack: an answer that ignored the start date, in the paths the basic tests skip.

// answerClient returns a client whose every write is answered with body.
func answerClient(t *testing.T, body string) (*asana.Client, *[]string) {
	t.Helper()
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		paths = append(paths, r.Method+" "+r.URL.Path)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	c := asana.NewClient(srv.Client())
	c.BaseURL = srv.URL
	c.MaxRetries = 0
	return c, &paths
}

func TestWrittenStartOnKnown(t *testing.T) {
	for _, tc := range []struct {
		name, data string
		known      bool
		start      string
	}{
		{"date", `{"gid":"x","start_on":"2026-12-01"}`, true, "2026-12-01"},
		{"null", `{"gid":"x","start_on":null}`, true, ""},
		{"absent", `{"gid":"x"}`, false, ""},
		{"number", `{"gid":"x","start_on":20261201}`, false, ""},
		{"object", `{"gid":"x","start_on":{"a":1}}`, false, ""},
		{"bool", `{"gid":"x","start_on":false}`, false, ""},
		{"empty string", `{"gid":"x","start_on":""}`, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := answerClient(t, `{"data":`+tc.data+`}`)
			ctx := context.Background()
			for name, call := range map[string]func() (*asana.Task, error){
				"update":  func() (*asana.Task, error) { return c.UpdateTask(ctx, "x", map[string]any{"name": "n"}) },
				"create":  func() (*asana.Task, error) { return c.CreateTask(ctx, map[string]any{"name": "n"}) },
				"subtask": func() (*asana.Task, error) { return c.CreateSubtask(ctx, "p", map[string]any{"name": "n"}) },
			} {
				got, err := call()
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				if got.StartOnKnown != tc.known || got.StartOn != tc.start || got.GID != "x" {
					t.Errorf("%s: got known=%v start=%q gid=%q", name, got.StartOnKnown, got.StartOn, got.GID)
				}
			}
		})
	}
}

// A non-date start_on must not look like an ignored start date.
func TestPushStartOnNonDateAnswerNotIgnored(t *testing.T) {
	f, s, st := setupStart(t, map[string]any{"due_on": "2026-12-10", "start_on": nil})
	ctx := context.Background()
	f.task["start_on"] = nil
	// answer with a number for start_on
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			f.ServeHTTP(w, r)
			return
		}
		io.Copy(io.Discard, r.Body)
		fmt.Fprint(w, `{"data":{"gid":"t1","name":"設計","workspace":{"gid":"w1"},"due_on":"2026-12-10","start_on":12345}}`)
	}))
	defer srv.Close()
	s.API.(*asana.Client).BaseURL = srv.URL
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"start_on": "2026-12-01"}, now); err != nil {
		t.Fatal(err)
	}
	res, err := s.Push(ctx)
	if err != nil || res.Failed != 0 || res.Sent != 1 || len(res.Discarded) != 0 {
		t.Fatalf("push = %+v, %v", res, err)
	}
	if _, ok, _ := st.PlanRefusedAt(ctx, plan.StartOn, "w1"); ok {
		t.Fatal("marked on an answer whose start_on is not a date")
	}
}

// A subtask create with an ignored start date: done, discarded, gid is the real one.
func TestPushStartOnIgnoredSubtask(t *testing.T) {
	f, s, st := setupStart(t, map[string]any{"due_on": "2026-12-10", "start_on": nil})
	st.SetDescribeChange(format.DescribeChange)
	ctx := context.Background()
	s.now = func() time.Time { return now }
	var subBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/tasks/t1/subtasks" && r.Method == http.MethodPost {
			var b struct{ Data map[string]any }
			json.NewDecoder(r.Body).Decode(&b)
			subBody = b.Data
			fmt.Fprint(w, `{"data":{"gid":"sub1","name":"子","workspace":{"gid":"w1"},"due_on":"2026-12-10","start_on":null,"created_at":"2026-11-13T00:00:00Z"}}`)
			return
		}
		f.ServeHTTP(w, r)
	}))
	defer srv.Close()
	s.API.(*asana.Client).BaseURL = srv.URL
	gid, err := st.CreateTaskLocal(ctx, store.NewTask{Name: "子", ParentGID: "t1", DueOn: "2026-12-10", StartOn: "2026-12-01"}, now)
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.Push(ctx)
	if err != nil || res.Failed != 0 || res.Sent != 1 || len(res.Discarded) != 1 {
		t.Fatalf("push = %+v, %v", res, err)
	}
	if subBody["start_on"] != "2026-12-01" {
		t.Fatalf("sub body %v", subBody)
	}
	if d := res.Discarded[0]; d.TaskGID != "sub1" || !strings.Contains(d.Reason, "タスクの開始日") {
		t.Fatalf("discard = %+v", d)
	}
	if real, _ := st.RealGID(ctx, gid); real != "sub1" {
		t.Fatalf("real = %q", real)
	}
	if _, ok, _ := st.PlanRefusedAt(ctx, plan.StartOn, "w1"); !ok {
		t.Fatal("not marked")
	}
	if ops, _ := st.ListOps(ctx); len(ops) != 0 {
		t.Fatalf("ops %+v", ops)
	}
}

// A create with a section becomes a move; the ignored start date counts once
// (Sent 1, Discarded 1) over the create and the follow-up move together.
func TestPushStartOnIgnoredCreateWithSection(t *testing.T) {
	f, s, st := setupStart(t, nil)
	st.SetDescribeChange(format.DescribeChange)
	ctx := context.Background()
	s.now = func() time.Time { return now }
	f.ignoreStart = true
	var moves int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/addTask") {
			io.Copy(io.Discard, r.Body)
			moves++
			fmt.Fprint(w, `{"data":{}}`)
			return
		}
		f.ServeHTTP(w, r)
	}))
	defer srv.Close()
	s.API.(*asana.Client).BaseURL = srv.URL
	if err := st.ReplaceSections(ctx, "p1", []asana.Section{{GID: "s1", Name: "S"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateTaskLocal(ctx, store.NewTask{Name: "見積もり", ProjectGID: "p1", SectionGID: "s1", DueOn: "2026-12-10", StartOn: "2026-12-01"}, now); err != nil {
		t.Fatal(err)
	}
	res, err := s.Push(ctx)
	if err != nil || res.Failed != 0 || res.Sent != 1 || len(res.Discarded) != 1 {
		t.Fatalf("push = %+v, %v", res, err)
	}
	if moves != 1 {
		t.Fatalf("moves = %d", moves)
	}
	if ops, _ := st.ListOps(ctx); len(ops) != 0 {
		t.Fatalf("ops %+v", ops)
	}
	if ds, _ := st.PendingDiscards(ctx); len(ds) != 1 {
		t.Fatalf("discards %+v", ds)
	}
}

// A later start date queued behind the ignored one (the first update was folded or
// sent already): after the first push marks the workspace, a second push of a
// new start date queued earlier than the mark must not resend forever.
func TestPushStartOnIgnoredTwiceDoesNotLoop(t *testing.T) {
	f, s, st := setupStart(t, map[string]any{"due_on": "2026-12-10", "start_on": nil})
	ctx := context.Background()
	s.now = func() time.Time { return now }
	f.ignoreStart = true
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"start_on": "2026-12-01"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Push(ctx); err != nil {
		t.Fatal(err)
	}
	// expire the mark, queue another, ignored again
	if err := st.RecordPlanRefusal(ctx, plan.StartOn, "w1", now.Add(-plan.RecheckAfter-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"start_on": "2026-12-02"}, now); err != nil {
		t.Fatal(err)
	}
	res, err := s.Push(ctx)
	if err != nil || res.Failed != 0 || res.Sent != 1 || len(res.Discarded) != 1 {
		t.Fatalf("push 2 = %+v, %v", res, err)
	}
	if len(f.puts) != 2 {
		t.Fatalf("puts = %v", f.puts)
	}
	if ds, _ := st.PendingDiscards(ctx); len(ds) != 2 {
		t.Fatalf("discards = %+v", ds)
	}
	if res, err = s.Push(ctx); err != nil || res.Sent != 0 || len(f.puts) != 2 {
		t.Fatalf("push 3 = %+v, %v, puts %v", res, err, f.puts)
	}
}

// Clearing the start date is not judged, even when Asana answers with one still set.
func TestPushStartOnClearNotJudged(t *testing.T) {
	f, s, st := setupStart(t, map[string]any{"due_on": "2026-12-10", "start_on": "2026-12-01"})
	ctx := context.Background()
	s.now = func() time.Time { return now }
	f.ignoreStart = true // the answer keeps 2026-12-01
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"start_on": ""}, now); err != nil {
		t.Fatal(err)
	}
	res, err := s.Push(ctx)
	if err != nil || res.Failed != 0 || len(res.Discarded) != 0 {
		t.Fatalf("push = %+v, %v", res, err)
	}
	if _, ok, _ := st.PlanRefusedAt(ctx, plan.StartOn, "w1"); ok {
		t.Fatal("marked on a clear")
	}
}

// A wrong date in the answer (not null) is ignored too.
func TestPushStartOnAnotherDateKept(t *testing.T) {
	f, s, st := setupStart(t, map[string]any{"due_on": "2026-12-10", "start_on": "2026-11-30"})
	ctx := context.Background()
	s.now = func() time.Time { return now }
	f.ignoreStart = true
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"start_on": "2026-12-01"}, now); err != nil {
		t.Fatal(err)
	}
	res, err := s.Push(ctx)
	if err != nil || len(res.Discarded) != 1 || res.Sent != 1 {
		t.Fatalf("push = %+v, %v", res, err)
	}
	if got, _ := st.GetTask(ctx, "t1"); got.StartOn != "2026-11-30" {
		t.Fatalf("cache start = %q", got.StartOn)
	}
}

// Ignored start date together with a due date change: the due date is applied.
func TestPushStartOnIgnoredKeepsOtherFields(t *testing.T) {
	f, s, st := setupStart(t, map[string]any{"due_on": "2026-12-10", "start_on": nil})
	ctx := context.Background()
	s.now = func() time.Time { return now }
	f.ignoreStart = true
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"start_on": "2026-12-01", "due_on": "2026-12-12", "name": "新名"}, now); err != nil {
		t.Fatal(err)
	}
	res, err := s.Push(ctx)
	if err != nil || len(res.Discarded) != 1 {
		t.Fatalf("push = %+v, %v", res, err)
	}
	got, _ := st.GetTask(ctx, "t1")
	if got.DueOn != "2026-12-12" || got.Name != "新名" || got.StartOn != "" {
		t.Fatalf("t1 = %+v", got)
	}
}

// Taking the start-date update out of the batch keeps the order of one task's entries:
// a move queued after it is sent after its PUT, and the others around it still go.
func TestPushStartOnNotBatchedKeepsOrder(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	gids := addBatchTasks(t, ws, st, 3)
	for _, g := range gids {
		c := map[string]any{"completed": true}
		if g == "b2" {
			c = map[string]any{"start_on": "2026-12-01", "due_on": "2026-12-10"}
		}
		if err := st.UpdateTaskLocal(ctx, g, c, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.MoveTaskLocal(ctx, "b2", "p1", "s2", now); err != nil {
		t.Fatal(err)
	}
	res, err := s.Push(ctx)
	if err != nil || res.Failed != 0 || res.Remaining != 0 {
		t.Fatalf("push = %+v, %v", res, err)
	}
	ws.mu.Lock()
	calls := append([]string(nil), ws.calls...)
	ws.mu.Unlock()
	put, move := -1, -1
	for i, c := range calls {
		if strings.HasPrefix(c, "PUT /tasks/b2") && put < 0 {
			put = i
		}
		if strings.HasPrefix(c, "POST /sections/s2/addTask") || strings.Contains(c, "/sections/s2/addTask") {
			move = i
		}
	}
	if put < 0 || move < 0 || put > move {
		t.Fatalf("PUT b2 at %d, move at %d: %v", put, move, calls)
	}
	for _, g := range []string{"b1", "b3"} {
		if got, _ := st.GetTask(ctx, g); got == nil || got.Pending {
			t.Fatalf("%s = %+v", g, got)
		}
	}
}
