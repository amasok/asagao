package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	stdsync "sync"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/domain/plan"
	"github.com/amasok/asagao/internal/store"
	"github.com/amasok/asagao/internal/ui/format"
)

// #57: a task's start date is sent with a due date (Asana requires one of due_on / due_at
// in a request that sets or clears start_on), and a 402 for it is recorded per workspace.

// startServer is a fake Asana with one task t1 in workspace w1, which keeps start_on,
// due_on and due_at as Asana does, and refuses start dates like Asana: without a due date
// in the request (400), or with a 402 when plan402 is set.
type startServer struct {
	t       *testing.T
	mu      stdsync.Mutex
	task    map[string]any // t1's fields
	created map[string]any // the body of a POST /tasks
	puts    []map[string]any
	plan402 bool
	// ignoreStart: answer 200 but keep the task's start date (#62); omitStart: answer
	// without start_on.
	ignoreStart, omitStart bool
}

func (f *startServer) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, _ := io.ReadAll(r.Body)
	var body struct{ Data map[string]any }
	_ = json.Unmarshal(b, &body)
	answer := func(task map[string]any) {
		if f.omitStart {
			task = maps.Clone(task)
			delete(task, "start_on")
		}
		out, _ := json.Marshal(map[string]any{"data": task})
		rw.Write(out)
	}
	refuse := func(status int, msg string) {
		rw.WriteHeader(status)
		fmt.Fprintf(rw, `{"errors":[{"message":%q}]}`, msg)
	}
	checkStart := func(data map[string]any) bool {
		if _, ok := data["start_on"]; !ok {
			return true
		}
		if f.plan402 {
			refuse(http.StatusPaymentRequired, "start dates are a premium feature")
			return false
		}
		_, due := data["due_on"]
		_, at := data["due_at"]
		if !due && !at {
			refuse(http.StatusBadRequest, "due_on or due_at must be present when setting or unsetting start_on")
			return false
		}
		return true
	}
	switch {
	case r.URL.Path == "/users/me":
		fmt.Fprint(rw, `{"data":{"gid":"me","name":"Me"}}`)
	case r.URL.Path == "/tasks/t1" && r.Method == http.MethodGet:
		answer(f.task)
	case r.URL.Path == "/tasks/t1" && r.Method == http.MethodPut:
		f.puts = append(f.puts, body.Data)
		if !checkStart(body.Data) {
			return
		}
		for k, v := range body.Data {
			if k == "start_on" && f.ignoreStart {
				continue
			}
			f.task[k] = v
		}
		if _, ok := body.Data["due_on"]; ok {
			f.task["due_at"] = nil
		}
		answer(f.task)
	case r.URL.Path == "/tasks" && r.Method == http.MethodPost:
		f.created = body.Data
		if !checkStart(body.Data) {
			return
		}
		task := map[string]any{"gid": "new1", "name": body.Data["name"], "workspace": map[string]any{"gid": "w1"},
			"created_at": time.Now().UTC().Format(time.RFC3339), "created_by": map[string]any{"gid": "me"}}
		for _, k := range []string{"due_on", "start_on"} {
			task[k] = body.Data[k]
		}
		if f.ignoreStart {
			task["start_on"] = nil
		}
		answer(task)
	default:
		refuse(http.StatusNotFound, "not found: "+r.Method+" "+r.URL.Path)
	}
}

func setupStart(t *testing.T, task map[string]any) (*startServer, *Syncer, *store.Store) {
	t.Helper()
	f := &startServer{t: t, task: map[string]any{"gid": "t1", "name": "設計", "workspace": map[string]any{"gid": "w1"},
		"modified_at": "2026-11-13T00:00:00Z"}}
	for k, v := range task {
		f.task[k] = v
	}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	api := asana.NewClient(srv.Client())
	api.BaseURL = srv.URL
	api.MaxRetries = 0
	if err := st.SetMeGID(ctx, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertProjects(ctx, "w1", []asana.Project{{GID: "p1", Name: "P"}}, "me"); err != nil {
		t.Fatal(err)
	}
	// the cache as a pull of f.task would leave it
	b, _ := json.Marshal(f.task)
	var mt asana.Task
	if err := json.Unmarshal(b, &mt); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertTasks(ctx, []asana.Task{mt}); err != nil {
		t.Fatal(err)
	}
	return f, &Syncer{API: api, Store: st, Cfg: Config{CompletedDays: 14}}, st
}

func TestPushStartOnSendsDueDate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		task    map[string]any
		changes []map[string]any
		wantPut map[string]any // the fields of the PUT that must be there, as JSON values
	}{
		{"set, due date unchanged", map[string]any{"due_on": "2026-12-10"},
			[]map[string]any{{"start_on": "2026-12-01"}}, map[string]any{"start_on": "2026-12-01", "due_on": "2026-12-10"}},
		{"cleared, due date unchanged", map[string]any{"due_on": "2026-12-10", "start_on": "2026-12-01"},
			[]map[string]any{{"start_on": ""}}, map[string]any{"start_on": nil, "due_on": "2026-12-10"}},
		{"set with a new due date", map[string]any{},
			[]map[string]any{{"start_on": "2026-12-01", "due_on": "2026-12-05"}}, map[string]any{"start_on": "2026-12-01", "due_on": "2026-12-05"}},
		{"both cleared", map[string]any{"due_on": "2026-12-10", "start_on": "2026-12-01"},
			[]map[string]any{{"start_on": "", "due_on": ""}}, map[string]any{"start_on": nil, "due_on": nil}},
		{"due at a time", map[string]any{"due_on": "2026-12-10", "due_at": "2026-12-10T03:00:00Z"},
			[]map[string]any{{"start_on": "2026-12-01"}}, map[string]any{"start_on": "2026-12-01", "due_at": "2026-12-10T03:00:00Z"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, s, st := setupStart(t, tc.task)
			ctx := context.Background()
			for _, c := range tc.changes {
				if err := st.UpdateTaskLocal(ctx, "t1", c, now); err != nil {
					t.Fatal(err)
				}
			}
			res, err := s.Push(ctx)
			if err != nil || res.Sent != 1 || res.Failed != 0 {
				t.Fatalf("push = %+v, %v (puts %v)", res, err, f.puts)
			}
			if len(f.puts) != 1 {
				t.Fatalf("puts = %v", f.puts)
			}
			put := f.puts[0]
			for k, v := range tc.wantPut {
				got, ok := put[k]
				if !ok || fmt.Sprint(got) != fmt.Sprint(v) {
					t.Errorf("PUT %s = %v (present %v), want %v; body %v", k, got, ok, v, put)
				}
			}
			if _, both := put["due_at"]; both {
				if _, on := put["due_on"]; on {
					t.Errorf("PUT has both due_on and due_at: %v", put)
				}
			}
		})
	}
}

func TestPushStartOn402(t *testing.T) {
	f, s, st := setupStart(t, map[string]any{"due_on": "2026-12-10"})
	st.SetDescribeChange(format.DescribeChange) // as wire.OpenStore does
	ctx := context.Background()
	s.now = func() time.Time { return now }
	f.plan402 = true
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"start_on": "2026-12-01"}, now); err != nil {
		t.Fatal(err)
	}
	res, err := s.Push(ctx)
	if err != nil || res.Failed != 0 || res.Sent != 0 || len(res.Discarded) != 1 {
		t.Fatalf("push = %+v, %v", res, err)
	}
	if d := res.Discarded[0]; !strings.Contains(d.Reason, "タスクの開始日") || !strings.Contains(d.Summary, "開始日を 2026-12-01") {
		t.Fatalf("discard = %+v", d)
	}
	if at, ok, _ := st.PlanRefusedAt(ctx, plan.StartOn, "w1"); !ok || !plan.OffAt(at, now) {
		t.Fatalf("402 not recorded for w1: (%v, %v)", at, ok)
	}
	// Only the start date was changed: the entry is gone, and so is the local start date.
	ops, _ := st.ListOps(ctx)
	if len(ops) != 0 {
		t.Fatalf("ops = %+v", ops)
	}
	if d, _ := st.GetTask(ctx, "t1"); d.StartOn != "" || d.Pending {
		t.Fatalf("t1 after the 402: start %q pending %v", d.StartOn, d.Pending)
	}
	if ds, _ := st.PendingDiscards(ctx); len(ds) != 1 {
		t.Fatalf("pending discards = %+v", ds)
	}
	// While it is recorded, another start date is refused before it is queued.
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"start_on": "2026-12-03"}, now); err == nil {
		t.Fatal("a start date was queued under a recorded 402")
	}

	// The plan was upgraded and the 402 expired: a start date that is sent forgets it.
	f.plan402 = false
	later := now.Add(plan.RecheckAfter + time.Minute)
	s.now = func() time.Time { return later }
	if err := st.RecordPlanRefusal(ctx, plan.StartOn, "w1", now.Add(-plan.RecheckAfter)); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"start_on": "2026-12-02"}, later); err != nil {
		t.Fatal(err)
	}
	// (a 402 recorded again meanwhile, e.g. by another process: the answer clears it)
	if err := st.RecordPlanRefusal(ctx, plan.StartOn, "w1", later.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if res, err := s.Push(ctx); err != nil || res.Failed != 0 {
		t.Fatalf("push = %+v, %v", res, err)
	}
	if _, ok, _ := st.PlanRefusedAt(ctx, plan.StartOn, "w1"); ok {
		t.Fatal("402 not forgotten after a start date was accepted")
	}
}

func TestPushCreateWithStartOn(t *testing.T) {
	f, s, st := setupStart(t, nil)
	ctx := context.Background()
	gid, err := st.CreateTaskLocal(ctx, store.NewTask{Name: "新規", ProjectGID: "p1", DueOn: "2026-12-10", StartOn: "2026-12-01"}, now)
	if err != nil {
		t.Fatal(err)
	}
	// started and then cleared before it was sent: nothing to unset
	if err := st.UpdateTaskLocal(ctx, gid, map[string]any{"start_on": ""}, now); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateTaskLocal(ctx, gid, map[string]any{"due_on": ""}, now); err != nil {
		t.Fatal(err)
	}
	res, err := s.Push(ctx)
	if err != nil || res.Failed != 0 {
		t.Fatalf("push = %+v, %v (created %v)", res, err, f.created)
	}
	if _, ok := f.created["start_on"]; ok {
		t.Fatalf("a cleared start date was sent with the create: %v", f.created)
	}

	gid, err = st.CreateTaskLocal(ctx, store.NewTask{Name: "新規2", ProjectGID: "p1", DueOn: "2026-12-10", StartOn: "2026-12-01"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if res, err := s.Push(ctx); err != nil || res.Failed != 0 {
		t.Fatalf("push = %+v, %v", res, err)
	}
	if f.created["start_on"] != "2026-12-01" || f.created["due_on"] != "2026-12-10" {
		t.Fatalf("created %v", f.created)
	}
	_ = gid
}

// A 402 for the start date does not hold back the rest of the change (#57 review): the
// start date is dropped and reported, the rest is sent.
func TestPushStartOn402KeepsTheRest(t *testing.T) {
	f, s, st := setupStart(t, map[string]any{"due_on": "2026-12-10", "start_on": "2026-11-30"})
	ctx := context.Background()
	s.now = func() time.Time { return now }
	f.plan402 = true
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "詳細設計", "start_on": "2026-12-01"}, now); err != nil {
		t.Fatal(err)
	}
	res, err := s.Push(ctx)
	if err != nil || res.Failed != 0 || res.Sent != 1 || len(res.Discarded) != 1 {
		t.Fatalf("push = %+v, %v (puts %v)", res, err, f.puts)
	}
	if f.task["name"] != "詳細設計" || f.task["start_on"] != "2026-11-30" {
		t.Fatalf("server task = %v", f.task)
	}
	if d, _ := st.GetTask(ctx, "t1"); d.Name != "詳細設計" || d.StartOn != "2026-11-30" || d.Pending {
		t.Fatalf("t1 = name %q start %q pending %v", d.Name, d.StartOn, d.Pending)
	}
	if ops, _ := st.ListOps(ctx); len(ops) != 0 {
		t.Fatalf("ops = %+v", ops)
	}
}

func TestPushCreateStartOn402(t *testing.T) {
	f, s, st := setupStart(t, nil)
	ctx := context.Background()
	s.now = func() time.Time { return now }
	f.plan402 = true
	gid, err := st.CreateTaskLocal(ctx, store.NewTask{Name: "見積もり", ProjectGID: "p1", DueOn: "2026-12-10", StartOn: "2026-12-01"}, now)
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.Push(ctx)
	if err != nil || res.Failed != 0 || res.Sent != 1 || len(res.Discarded) != 1 {
		t.Fatalf("push = %+v, %v (created %v)", res, err, f.created)
	}
	if _, ok := f.created["start_on"]; ok || f.created["name"] != "見積もり" || f.created["due_on"] != "2026-12-10" {
		t.Fatalf("created %v", f.created)
	}
	real, err := st.RealGID(ctx, gid)
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := st.GetTask(ctx, real); d == nil || d.StartOn != "" || d.DueOn != "2026-12-10" {
		t.Fatalf("created task = %+v", d)
	}
}

// The due date changed in Asana after the start date was queued so that Asana would
// refuse it (400): it is not sent, but dropped with the reason, and the rest is sent.
func TestPushStartOnDueChangedInAsana(t *testing.T) {
	for _, tc := range []struct {
		name   string
		server map[string]any
		want   string
	}{
		{"due removed", map[string]any{"due_on": nil}, "期日が外された"},
		{"due moved before the start", map[string]any{"due_on": "2026-11-20"}, "期日が 2026-11-20 になり"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, s, st := setupStart(t, map[string]any{"due_on": "2026-12-10"})
			ctx := context.Background()
			if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"start_on": "2026-12-01", "name": "改名"}, now); err != nil {
				t.Fatal(err)
			}
			for k, v := range tc.server {
				f.task[k] = v
			}
			res, err := s.Push(ctx)
			if err != nil || res.Sent != 1 || res.Failed != 0 || len(res.Discarded) != 1 {
				t.Fatalf("push = %+v, %v", res, err)
			}
			if !strings.Contains(res.Discarded[0].Reason, tc.want) {
				t.Errorf("reason = %q", res.Discarded[0].Reason)
			}
			for _, put := range f.puts {
				if _, ok := put["start_on"]; ok {
					t.Errorf("start date sent: %v", put)
				}
			}
			if f.task["name"] != "改名" {
				t.Errorf("the rest was not sent: %v", f.task)
			}
			if d, _ := st.GetTask(ctx, "t1"); d.StartOn != "" {
				t.Errorf("local start date kept: %q", d.StartOn)
			}
		})
	}
}

// Clearing a start date that Asana no longer has (its due date was removed too) sends
// nothing: there is nothing to clear, and Asana wants a due date with it.
func TestPushStartOnClearAlreadyGone(t *testing.T) {
	f, s, st := setupStart(t, map[string]any{"due_on": "2026-12-10", "start_on": "2026-12-01"})
	ctx := context.Background()
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"start_on": ""}, now); err != nil {
		t.Fatal(err)
	}
	f.task["due_on"], f.task["start_on"] = nil, nil
	res, err := s.Push(ctx)
	if err != nil || res.Sent != 1 || res.Failed != 0 || len(f.puts) != 0 {
		t.Fatalf("push = %+v, %v (puts %v)", res, err, f.puts)
	}
	if ops, _ := st.ListOps(ctx); len(ops) != 0 {
		t.Fatalf("ops = %+v", ops)
	}
}
