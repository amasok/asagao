package sync

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/domain/plan"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/store"
)

var meRef = store.UserRef{GID: "me", Name: "Me"}

// statusHook answers every time tracking request of method with status (no change to the data).
func statusHook(ts *timeServer, method string, status int) func(http.ResponseWriter, *http.Request) bool {
	return func(rw http.ResponseWriter, r *http.Request) bool {
		if r.Method == method && strings.Contains(r.URL.Path, "time_tracking_entries") {
			ts.mu.Lock()
			ts.calls = append(ts.calls, r.Method+" "+r.URL.Path)
			ts.mu.Unlock()
			rw.WriteHeader(status)
			_, _ = rw.Write([]byte(`{"errors":[{"message":"x"}]}`))
			return true
		}
		return ts.hook(rw, r)
	}
}

// 429 on an add: nothing was processed, so the entry stays queued (not failed) and Push stops.
func TestAttackTimeEntryAdd429KeepsPending(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ts := newTimeServer()
	ws.setHook(statusHook(ts, http.MethodPost, http.StatusTooManyRequests))
	ctx := context.Background()
	if _, err := st.AddTimeEntryLocal(ctx, "t1", 30, "2026-11-12", meRef, now); err != nil {
		t.Fatal(err)
	}
	_, _ = s.Push(ctx)
	if op := opByKind(t, st, store.OpAddTimeEntry); op == nil || op.State == store.OpFailed {
		t.Fatalf("after 429: %+v", op)
	}
	// Recovered: the next push sends it exactly once.
	ws.setHook(ts.hook)
	ts.calls = nil
	if res, err := s.Push(ctx); err != nil || res.Failed != 0 {
		t.Fatalf("Push = %+v, %v", res, err)
	}
	if n := ts.count("POST "); n != 1 {
		t.Errorf("%d POSTs: %v", n, ts.calls)
	}
	assertOutboxEmpty(t, st)
}

// 403 on an add: rejected, failed for the user, and the plan is NOT marked unavailable.
func TestAttackTimeEntryAdd403(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ts := newTimeServer()
	ws.setHook(statusHook(ts, http.MethodPost, http.StatusForbidden))
	ctx := context.Background()
	if _, err := st.AddTimeEntryLocal(ctx, "t1", 30, "2026-11-12", meRef, now); err != nil {
		t.Fatal(err)
	}
	if res, _ := s.Push(ctx); res.Failed != 1 {
		t.Fatalf("Push = %+v", res)
	}
	if _, ok, _ := st.PlanRefusedAt(ctx, plan.TimeTracking, "w1"); ok {
		t.Error("403 marked the plan unavailable")
	}
}

// 402 on an edit and on a delete record the plan too, and the fetch is then refused.
func TestAttackTimeEntry402OnEditAndDelete(t *testing.T) {
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			ws, _, s, st := setupPush(t)
			ts := newTimeServer()
			ctx := context.Background()
			if err := st.ReplaceTimeEntries(ctx, "t1", []asana.TimeEntry{{GID: "e1", DurationMinutes: 5, EnteredOn: "2026-11-01"}}, now); err != nil {
				t.Fatal(err)
			}
			ts.mu.Lock()
			ts.entries["e1"] = map[string]any{"gid": "e1", "task": "t1", "duration_minutes": 5, "entered_on": "2026-11-01"}
			ts.order = append(ts.order, "e1")
			ts.mu.Unlock()
			ws.setHook(statusHook(ts, method, http.StatusPaymentRequired))
			if method == http.MethodPut {
				must(t, st.UpdateTimeEntryLocal(ctx, "e1", 10, "2026-11-01", now))
			} else {
				must(t, st.DeleteTimeEntryLocal(ctx, "e1", now))
			}
			if res, _ := s.Push(ctx); res.Failed != 1 {
				t.Fatalf("Push = %+v", res)
			}
			var pe *plan.UnavailableError
			if err := s.FetchTimeEntries(ctx, "t1"); !errors.As(err, &pe) {
				t.Errorf("402 on %s was not recorded: %v", method, err)
			}
		})
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// 5xx on an edit: the edit is retried later, and a repeated push never makes a second add.
func TestAttackTimeEntryEdit5xxThenOK(t *testing.T) {
	ts, s, st := setupTimePush(t)
	ctx := context.Background()
	local, _ := st.AddTimeEntryLocal(ctx, "t1", 30, "2026-11-12", meRef, now)
	if _, err := s.Push(ctx); err != nil {
		t.Fatal(err)
	}
	real, _, _ := st.LocalResult(ctx, readmodel.ResultTimeEntry, local)
	must(t, st.UpdateTimeEntryLocal(ctx, real, 40, "2026-11-12", now))
	ts.mu.Lock()
	ts.lose[http.MethodPut] = 1
	ts.mu.Unlock()
	_, _ = s.Push(ctx)
	_, _ = s.Push(ctx)
	_, _ = s.Push(ctx)
	if n := ts.count("POST "); n != 1 {
		t.Errorf("%d POSTs", n)
	}
	if e := ts.entries[real]; e["duration_minutes"] != float64(40) {
		t.Errorf("server %+v", e)
	}
}

// An edit whose entry was deleted in Asana (404) is dropped; it is not a failure the user
// has to resolve forever, and no entry comes back.
func TestAttackTimeEntryEditGone(t *testing.T) {
	ts, s, st := setupTimePush(t)
	ctx := context.Background()
	must(t, st.ReplaceTimeEntries(ctx, "t1", []asana.TimeEntry{{GID: "ghost", DurationMinutes: 5, EnteredOn: "2026-11-01"}}, now))
	must(t, st.UpdateTimeEntryLocal(ctx, "ghost", 9, "2026-11-01", now))
	_, _ = s.Push(ctx)
	if n := ts.count("PUT "); n != 1 {
		t.Errorf("PUT calls %v", ts.calls)
	}
	if op := opByKind(t, st, store.OpUpdateTimeEntry); op != nil && op.State != store.OpFailed {
		t.Errorf("op %+v", op)
	}
}

// A time entry cannot be added to a task made offline whose create failed: no request goes out.
func TestAttackTimeEntryAddOnLocalTask(t *testing.T) {
	ts, s, st := setupTimePush(t)
	ctx := context.Background()
	gid, err := st.CreateTaskLocal(ctx, store.NewTask{ProjectGID: "p1", Name: "new"}, now)
	must(t, err)
	_, err = st.AddTimeEntryLocal(ctx, gid, 15, "2026-11-12", meRef, now)
	must(t, err)
	// the fetch for a local task asks nothing
	if err := s.FetchTimeEntries(ctx, gid); err != nil {
		t.Fatal(err)
	}
	if n := ts.count("GET "); n != 0 {
		t.Errorf("asked Asana about a local task: %v", ts.calls)
	}
}

func TestAttackFetchOffline(t *testing.T) {
	_, s, _ := setupTimePush(t)
	s.API = nil
	if err := s.FetchTimeEntries(context.Background(), "t1"); !errors.Is(err, ErrOffline) {
		t.Errorf("got %v", err)
	}
}

// Terminal control characters from Asana's names must not reach the view.
func TestAttackFetchTimeEntriesTermsafe(t *testing.T) {
	ts, s, st := setupTimePush(t)
	ctx := context.Background()
	ts.mu.Lock()
	g := ts.add("t1", 10, "2026-11-10", "u2", time.Now().Add(-time.Hour))
	ts.entries[g]["created_by"] = map[string]any{"gid": "u2", "name": "ev\x1b]0;pwn\x07il\x1b[2J"}
	ts.entries[g]["attributable_to"] = map[string]any{"gid": "p", "name": "pr\x1b[31mj"}
	ts.entries[g]["categories"] = []any{map[string]any{"gid": "c", "name": "ca\x1b[2Jt"}}
	ts.mu.Unlock()
	must(t, s.FetchTimeEntries(ctx, "t1"))
	es := entriesOf(t, st, "t1")
	if len(es.Entries) != 1 {
		t.Fatalf("%+v", es)
	}
	e := es.Entries[0]
	all := e.Project + e.CreatedBy.Name + strings.Join(e.Categories, "")
	if strings.ContainsAny(all, "\x1b\x07") {
		t.Errorf("control characters survived: %q", all)
	}
}

// An entry just created by a push is kept by a fetch whose list was read before it; a list
// read after it replaces it.
func TestAttackFetchKeepsEntryCreatedDuringFetch(t *testing.T) {
	_, s, st := setupTimePush(t)
	ctx := context.Background()
	_, err := st.AddTimeEntryLocal(ctx, "t1", 30, "2026-11-12", meRef, now)
	must(t, err)
	if _, err := s.Push(ctx); err != nil {
		t.Fatal(err)
	}
	must(t, st.ReplaceTimeEntries(ctx, "t1", nil, time.Now().Add(-time.Hour)))
	if es := entriesOf(t, st, "t1"); len(es.Entries) != 1 {
		t.Errorf("entry created after the read was lost: %+v", es.Entries)
	}
	must(t, st.ReplaceTimeEntries(ctx, "t1", nil, time.Now().Add(time.Hour)))
	if es := entriesOf(t, st, "t1"); len(es.Entries) != 0 {
		t.Errorf("stale entry kept: %+v", es.Entries)
	}
}

// Two pushes at once (flock excludes the second, or they take turns): one add, one POST.
func TestAttackConcurrentPushOneAdd(t *testing.T) {
	ts, s, st := setupTimePush(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		_, err := st.AddTimeEntryLocal(ctx, "t1", 10+i, "2026-11-12", meRef, now)
		must(t, err)
	}
	done := make(chan struct{}, 4)
	for i := 0; i < 4; i++ {
		go func() { _, _ = s.Push(ctx); done <- struct{}{} }()
	}
	for i := 0; i < 4; i++ {
		<-done
	}
	if n := ts.count("POST "); n != 3 {
		t.Errorf("%d POSTs for 3 entries: %v", n, ts.calls)
	}
}

// An edit queued for an entry whose add was dropped (rejected) sends no request to Asana.
func TestAttackTimeEntryEditOfDroppedAdd(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ts := newTimeServer()
	ws.setHook(statusHook(ts, http.MethodPost, http.StatusForbidden))
	ctx := context.Background()
	local, err := st.AddTimeEntryLocal(ctx, "t1", 30, "2026-11-12", meRef, now)
	must(t, err)
	op := opByKind(t, st, store.OpAddTimeEntry)
	_, err = st.BeginSend(ctx, op.ID, now)
	must(t, err)
	must(t, st.UpdateTimeEntryLocal(ctx, local, 40, "2026-11-12", now))
	must(t, st.MarkOpVerify(ctx, op.ID, ""))
	ws.setHook(statusHook(ts, http.MethodPost, http.StatusForbidden))
	ts.calls = nil
	_, _ = s.Push(ctx)
	if n := ts.count("PUT "); n != 0 {
		t.Errorf("PUT sent for an entry Asana never had: %v", ts.calls)
	}
}

// An entry recorded on a task made offline is sent after the task's create, to the task's real gid.
func TestAttackTimeEntryOnOfflineTaskAfterCreate(t *testing.T) {
	ts, s, st := setupTimePush(t)
	ctx := context.Background()
	gid, err := st.CreateTaskLocal(ctx, store.NewTask{ProjectGID: "p1", Name: "new"}, now)
	must(t, err)
	_, err = st.AddTimeEntryLocal(ctx, gid, 15, "2026-11-12", meRef, now)
	must(t, err)
	for i := 0; i < 3; i++ {
		if _, err := s.Push(ctx); err != nil {
			t.Fatal(err)
		}
	}
	real, _, _ := st.LocalResult(ctx, readmodel.ResultTask, gid)
	if real == "" {
		t.Fatal("task not created")
	}
	if n := ts.count("POST /tasks/" + real + "/time_tracking_entries"); n != 1 {
		t.Errorf("calls %v", ts.calls)
	}
	if n := ts.count("POST /tasks/local"); n != 0 {
		t.Errorf("sent to the local gid: %v", ts.calls)
	}
	assertOutboxEmpty(t, st)
}

func twKey(ws string) string { return "plan_unavailable:" + plan.TimeTracking + ":" + ws }

// A recent 402 hides the list without asking Asana; at 24 hours it is asked again, and an
// answer forgets the 402. A value that is garbage or from the future does not hide it.
func TestAttackPlanRecheckAfter24h(t *testing.T) {
	ts, s, st := setupTimePush(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	cur := at
	s.now = func() time.Time { return cur }
	must(t, st.RecordPlanRefusal(ctx, plan.TimeTracking, "w1", at))
	var pe *plan.UnavailableError
	for _, d := range []time.Duration{0, time.Hour, 24*time.Hour - time.Second} {
		cur = at.Add(d)
		if err := s.FetchTimeEntries(ctx, "t1"); !errors.As(err, &pe) {
			t.Fatalf("at +%v: %v", d, err)
		}
	}
	if n := ts.count("GET "); n != 0 {
		t.Fatalf("asked Asana while hidden: %v", ts.calls)
	}
	cur = at.Add(24 * time.Hour)
	must(t, s.FetchTimeEntries(ctx, "t1"))
	if n := ts.count("GET "); n != 1 {
		t.Fatalf("not asked again at 24h: %v", ts.calls)
	}
	if _, ok, _ := st.PlanRefusedAt(ctx, plan.TimeTracking, "w1"); ok {
		t.Error("402 not forgotten after an answer")
	}
	for _, v := range []string{"garbage", cur.Add(time.Hour).Format(time.RFC3339)} {
		must(t, st.SetSyncState(ctx, twKey("w1"), v))
		before := ts.count("GET ")
		must(t, s.FetchTimeEntries(ctx, "t1"))
		if ts.count("GET ") != before+1 {
			t.Errorf("value %q hid the list", v)
		}
	}
}

// Still 402 after the recheck: it is hidden again from that moment.
func TestAttackPlanRecheckStill402(t *testing.T) {
	ts, s, st := setupTimePush(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	cur := at.Add(25 * time.Hour)
	s.now = func() time.Time { return cur }
	must(t, st.RecordPlanRefusal(ctx, plan.TimeTracking, "w1", at))
	ts.unpaid = true
	var pe *plan.UnavailableError
	if err := s.FetchTimeEntries(ctx, "t1"); !errors.As(err, &pe) {
		t.Fatal(err)
	}
	cur = cur.Add(time.Hour)
	if err := s.FetchTimeEntries(ctx, "t1"); !errors.As(err, &pe) || ts.count("GET ") != 1 {
		t.Errorf("err %v, calls %v", err, ts.calls)
	}
}

// A 402 in one workspace does not hide time tracking of a task in another.
func TestAttackPlanPerWorkspace(t *testing.T) {
	ts, s, st := setupTimePush(t)
	ctx := context.Background()
	must(t, st.UpsertTasks(ctx, []asana.Task{{GID: "t2", Name: "B", Workspace: &asana.Ref{GID: "w2"}}}))
	must(t, st.RecordPlanRefusal(ctx, plan.TimeTracking, "w1", time.Now().UTC()))
	var pe *plan.UnavailableError
	if err := s.FetchTimeEntries(ctx, "t1"); !errors.As(err, &pe) {
		t.Fatalf("t1: %v", err)
	}
	if err := s.FetchTimeEntries(ctx, "t2"); err != nil {
		t.Fatalf("t2 (another workspace): %v", err)
	}
	if n := ts.count("GET /tasks/t2/"); n != 1 {
		t.Errorf("calls %v", ts.calls)
	}
	// and a 402 on t2 records w2 only
	ts.unpaid = true
	must(t, st.ClearPlanRefusal(ctx, plan.TimeTracking, "w1"))
	_ = s.FetchTimeEntries(ctx, "t2")
	if _, ok, _ := st.PlanRefusedAt(ctx, plan.TimeTracking, "w2"); !ok {
		t.Error("w2 not recorded")
	}
	if _, ok, _ := st.PlanRefusedAt(ctx, plan.TimeTracking, "w1"); ok {
		t.Error("w1 recorded by a 402 of w2")
	}
}
