package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	stdsync "sync"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/store"
)

// timeServer answers the time tracking endpoints like Asana (developers.asana.com):
// a task's list gives the compact entries (no created_at), one entry and the answers to a
// create or an update give it with created_at, a delete answers {"data":{}}.
type timeServer struct {
	mu      stdsync.Mutex
	shift   time.Duration
	entries map[string]map[string]any // gid -> entry
	order   []string
	next    int
	// paid false answers every request with 402.
	unpaid bool
	// lose["POST"] = n: the next n such requests are applied, then answered 500.
	lose  map[string]int
	calls []string
}

func newTimeServer() *timeServer {
	return &timeServer{entries: map[string]map[string]any{}, lose: map[string]int{}}
}

func (ts *timeServer) add(task string, minutes int, on, by string, created time.Time) string {
	ts.next++
	gid := fmt.Sprintf("te%d", ts.next)
	ts.entries[gid] = map[string]any{"gid": gid, "resource_type": "time_tracking_entry", "duration_minutes": minutes,
		"entered_on": on, "created_by": map[string]any{"gid": by, "resource_type": "user", "name": by},
		"assignee":   map[string]any{"gid": by, "resource_type": "user", "name": by},
		"created_at": created.UTC().Format(time.RFC3339), "task": task}
	ts.order = append(ts.order, gid)
	return gid
}

func (ts *timeServer) compact(e map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range e {
		if k != "created_at" && k != "task" {
			out[k] = v
		}
	}
	return out
}

func (ts *timeServer) count(prefix string) int {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	n := 0
	for _, c := range ts.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func (ts *timeServer) hook(rw http.ResponseWriter, r *http.Request) bool {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	isList := len(parts) == 3 && parts[0] == "tasks" && parts[2] == "time_tracking_entries"
	isOne := len(parts) == 2 && parts[0] == "time_tracking_entries"
	if !isList && !isOne {
		return false
	}
	ts.mu.Lock()
	defer ts.mu.Unlock()
	b, _ := io.ReadAll(r.Body)
	ts.calls = append(ts.calls, r.Method+" "+r.URL.Path+" "+strings.TrimSpace(string(b)))
	if ts.unpaid {
		rw.WriteHeader(http.StatusPaymentRequired)
		fmt.Fprint(rw, `{"errors":[{"message":"Time tracking is not available on this plan."}]}`)
		return true
	}
	var body struct{ Data map[string]any }
	_ = json.Unmarshal(b, &body)
	write := func(v any) {
		out, _ := json.Marshal(map[string]any{"data": v})
		if ts.lose[r.Method] > 0 {
			ts.lose[r.Method]--
			rw.WriteHeader(http.StatusInternalServerError)
			return
		}
		rw.Write(out)
	}
	now := time.Now().Add(ts.shift)
	switch {
	case isList && r.Method == http.MethodGet:
		list := []any{}
		for _, g := range ts.order {
			if e, ok := ts.entries[g]; ok && e["task"] == parts[1] {
				list = append(list, ts.compact(e))
			}
		}
		out, _ := json.Marshal(map[string]any{"data": list, "next_page": nil})
		rw.Write(out)
	case isList && r.Method == http.MethodPost:
		minutes, _ := body.Data["duration_minutes"].(float64)
		on, _ := body.Data["entered_on"].(string)
		if on == "" {
			on = now.Format(time.DateOnly)
		}
		g := ts.add(parts[1], int(minutes), on, "me", now)
		rw.WriteHeader(http.StatusCreated)
		write(ts.entries[g])
	case isOne:
		e, ok := ts.entries[parts[1]]
		if !ok {
			rw.WriteHeader(http.StatusNotFound)
			fmt.Fprint(rw, `{"errors":[{"message":"Not Found"}]}`)
			return true
		}
		switch r.Method {
		case http.MethodGet:
			write(e)
		case http.MethodPut:
			for k, v := range body.Data {
				e[k] = v
			}
			write(e)
		case http.MethodDelete:
			delete(ts.entries, parts[1])
			write(map[string]any{})
		}
	default:
		rw.WriteHeader(http.StatusBadRequest)
	}
	return true
}

func setupTimePush(t *testing.T) (*timeServer, *Syncer, *store.Store) {
	t.Helper()
	ws, _, s, st := setupPush(t)
	ts := newTimeServer()
	ws.setHook(ts.hook)
	return ts, s, st
}

func entriesOf(t *testing.T, st *store.Store, task string) readmodel.TimeEntries {
	t.Helper()
	es, err := st.TimeEntries(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	return es
}

// An add goes out with the minutes and the day only, and the entry gets Asana's gid; an
// edit and a delete queued for it meanwhile go to that gid.
func TestPushTimeEntries(t *testing.T) {
	ts, s, st := setupTimePush(t)
	ctx := context.Background()
	local, err := st.AddTimeEntryLocal(ctx, "t1", 90, "2026-11-12", store.UserRef{GID: "me", Name: "Me"}, now)
	if err != nil {
		t.Fatal(err)
	}
	ts.mu.Lock()
	other := ts.add("t1", 15, "2026-11-10", "u2", now.Add(-time.Hour))
	ts.mu.Unlock()
	if err := s.FetchTimeEntries(ctx, "t1"); err != nil {
		t.Fatal(err)
	}
	if es := entriesOf(t, st, "t1"); len(es.Entries) != 2 || es.TotalMinutes != 105 {
		t.Fatalf("before the push %+v", es)
	}
	if err := st.UpdateTimeEntryLocal(ctx, other, 20, "2026-11-10", now); err != nil {
		t.Fatal(err)
	}
	res, err := s.Push(ctx)
	if err != nil || res.Sent != 2 || res.Failed != 0 {
		t.Fatalf("Push = %+v, %v", res, err)
	}
	if got := ts.count("POST /tasks/t1/time_tracking_entries " + `{"data":{"duration_minutes":90,"entered_on":"2026-11-12"}}`); got != 1 {
		t.Errorf("POST calls %v", ts.calls)
	}
	if got := ts.count("PUT /time_tracking_entries/" + other + ` {"data":{"duration_minutes":20,"entered_on":"2026-11-10"}}`); got != 1 {
		t.Errorf("PUT calls %v", ts.calls)
	}
	es := entriesOf(t, st, "t1")
	if len(es.Entries) != 2 || store.IsLocalGID(es.Entries[0].GID) || es.Entries[0].Pending || es.TotalMinutes != 110 {
		t.Fatalf("after the push %+v", es.Entries)
	}
	real := es.Entries[0].GID
	if r, _, _ := st.LocalResult(ctx, store.ResultTimeEntry, local); r != real {
		t.Errorf("result of %s = %q, want %q", local, r, real)
	}
	if err := st.DeleteTimeEntryLocal(ctx, real, now); err != nil {
		t.Fatal(err)
	}
	if res, err := s.Push(ctx); err != nil || res.Sent != 1 {
		t.Fatalf("Push = %+v, %v", res, err)
	}
	if _, ok := ts.entries[real]; ok || ts.count("DELETE /time_tracking_entries/"+real) != 1 {
		t.Errorf("not deleted: %v", ts.calls)
	}
	assertOutboxEmpty(t, st)
}

// An edit and a delete of an entry not created in Asana yet wait for its add, then use
// Asana's gid.
func TestPushTimeEntryEditsWaitForAdd(t *testing.T) {
	ts, s, st := setupTimePush(t)
	ctx := context.Background()
	local, _ := st.AddTimeEntryLocal(ctx, "t1", 30, "2026-11-12", store.UserRef{GID: "me"}, now)
	add := opByKind(t, st, store.OpAddTimeEntry)
	if _, err := st.BeginSend(ctx, add.ID, now); err != nil { // as if another push is sending it
		t.Fatal(err)
	}
	if err := st.UpdateTimeEntryLocal(ctx, local, 45, "2026-11-12", now); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkOpVerify(ctx, add.ID, ""); err != nil { // its answer was lost
		t.Fatal(err)
	}
	res, err := s.Push(ctx)
	if err != nil || res.Failed != 0 {
		t.Fatalf("Push = %+v, %v", res, err)
	}
	if n := ts.count("POST "); n != 1 {
		t.Errorf("%d POSTs: %v", n, ts.calls)
	}
	if n := ts.count("PUT /time_tracking_entries/te1 "); n != 1 {
		t.Errorf("PUT to Asana's gid: %v", ts.calls)
	}
	if e := ts.entries["te1"]; e == nil || e["duration_minutes"] != float64(45) {
		t.Errorf("server entry %+v", e)
	}
	assertOutboxEmpty(t, st)
}

// An add whose answer is lost is not sent again: Asana's entry with the same minutes and day,
// created by me since the add was sent, is taken as its result. An older entry with the same
// values, or someone else's, is not.
func TestPushTimeEntryLostAnswerIsFound(t *testing.T) {
	ts, s, st := setupTimePush(t)
	ctx := context.Background()
	ts.mu.Lock()
	ts.add("t1", 30, "2026-11-12", "me", time.Now().Add(-time.Hour)) // the same values, an hour ago
	ts.add("t1", 30, "2026-11-12", "u2", time.Now())                 // the same values, someone else
	ts.lose[http.MethodPost] = 1
	ts.mu.Unlock()
	if _, err := st.AddTimeEntryLocal(ctx, "t1", 30, "2026-11-12", store.UserRef{GID: "me"}, now); err != nil {
		t.Fatal(err)
	}
	res, err := s.Push(ctx)
	if err != nil || res.Sent != 1 || res.Failed != 0 {
		t.Fatalf("Push = %+v, %v", res, err)
	}
	if n := ts.count("POST "); n != 1 {
		t.Errorf("%d POSTs: %v", n, ts.calls)
	}
	es := entriesOf(t, st, "t1")
	if len(es.Entries) != 1 || es.Entries[0].GID != "te3" {
		t.Errorf("entries %+v", es.Entries)
	}
	assertOutboxEmpty(t, st)
}

// Two entries of mine that match (both created since the add) cannot be told apart: the add
// is failed for the user instead of being sent again.
func TestPushTimeEntryLostAnswerAmbiguous(t *testing.T) {
	ts, s, st := setupTimePush(t)
	ctx := context.Background()
	ts.mu.Lock()
	ts.add("t1", 30, "2026-11-12", "me", time.Now().Add(time.Second))
	ts.lose[http.MethodPost] = 1
	ts.mu.Unlock()
	if _, err := st.AddTimeEntryLocal(ctx, "t1", 30, "2026-11-12", store.UserRef{GID: "me"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Push(ctx); err != nil {
		t.Fatal(err)
	}
	if n := ts.count("POST "); n != 1 {
		t.Errorf("%d POSTs: %v", n, ts.calls)
	}
	if op := opByKind(t, st, store.OpAddTimeEntry); op == nil || op.State != store.OpFailed {
		t.Errorf("add %+v", op)
	}
}

// A 402 fails the entry, records that the plan lacks time tracking, and from then on the
// entries are not asked for.
func TestTimeTrackingPaymentRequired(t *testing.T) {
	ts, s, st := setupTimePush(t)
	ctx := context.Background()
	ts.unpaid = true
	if _, err := st.AddTimeEntryLocal(ctx, "t1", 30, "2026-11-12", store.UserRef{GID: "me"}, now); err != nil {
		t.Fatal(err)
	}
	if res, _ := s.Push(ctx); res.Failed != 1 {
		t.Fatalf("Push = %+v", res)
	}
	if v, _ := st.SyncState(ctx, readmodel.KeyPlanUnavailable+readmodel.PlanTimeTracking); v == "" {
		t.Fatal("402 not recorded")
	}
	before := ts.count("")
	err := s.FetchTimeEntries(ctx, "t1")
	var pe *readmodel.PlanUnavailableError
	if !errors.As(err, &pe) || pe.Feature != readmodel.PlanTimeTracking {
		t.Errorf("FetchTimeEntries = %v", err)
	}
	if ts.count("") != before {
		t.Errorf("asked Asana again: %v", ts.calls)
	}
}

// A 402 on the list is recorded too (a read is where a free workspace finds out first).
func TestFetchTimeEntriesPaymentRequired(t *testing.T) {
	ts, s, st := setupTimePush(t)
	ctx := context.Background()
	ts.unpaid = true
	var pe *readmodel.PlanUnavailableError
	if err := s.FetchTimeEntries(ctx, "t1"); !errors.As(err, &pe) {
		t.Fatalf("FetchTimeEntries = %v", err)
	}
	if err := s.FetchTimeEntries(ctx, "t1"); !errors.As(err, &pe) || ts.count("GET ") != 1 {
		t.Errorf("second fetch = %v, calls %v", err, ts.calls)
	}
	if v, _ := st.SyncState(ctx, readmodel.KeyPlanUnavailable+readmodel.PlanTimeTracking); v == "" {
		t.Error("402 not recorded")
	}
}

// A delete answered 404 is done (the entry is gone already).
func TestPushTimeEntryDeleteGone(t *testing.T) {
	ts, s, st := setupTimePush(t)
	ctx := context.Background()
	if err := st.ReplaceTimeEntries(ctx, "t1", []asana.TimeEntry{{GID: "gone", DurationMinutes: 5, EnteredOn: "2026-11-01"}}, now); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteTimeEntryLocal(ctx, "gone", now); err != nil {
		t.Fatal(err)
	}
	if res, err := s.Push(ctx); err != nil || res.Sent != 1 || res.Failed != 0 {
		t.Fatalf("Push = %+v, %v (%v)", res, err, ts.calls)
	}
	assertOutboxEmpty(t, st)
}
