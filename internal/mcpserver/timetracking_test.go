package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	stdsync "sync"
	"testing"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/domain/plan"
	"github.com/amasok/asagao/internal/store"
	"github.com/amasok/asagao/internal/sync"
)

// timeAsana answers t1's time tracking endpoints like Asana (unpaid: 402).
type timeAsana struct {
	mu     stdsync.Mutex
	unpaid bool
	calls  []string
}

func (f *timeAsana) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, _ := io.ReadAll(r.Body)
	f.calls = append(f.calls, r.Method+" "+r.URL.Path+" "+strings.TrimSpace(string(raw)))
	reply := func(v any) { _ = json.NewEncoder(w).Encode(map[string]any{"data": v}) }
	if f.unpaid && r.URL.Path != "/users/me" {
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"errors":[{"message":"Payment Required"}]}`))
		return
	}
	if r.URL.Path == "/users/me" {
		reply(map[string]string{"gid": "me", "name": "Me"})
		return
	}
	switch r.Method + " " + r.URL.Path {
	case "GET /tasks/t1/time_tracking_entries":
		reply([]any{map[string]any{"gid": "te1", "duration_minutes": 30, "entered_on": "2026-11-12",
			"created_by": map[string]string{"gid": "me", "name": "Me"}}})
	case "POST /tasks/t1/time_tracking_entries":
		var body struct{ Data map[string]any }
		_ = json.Unmarshal(raw, &body)
		w.WriteHeader(http.StatusCreated)
		reply(map[string]any{"gid": "te2", "duration_minutes": body.Data["duration_minutes"], "entered_on": body.Data["entered_on"],
			"created_by": map[string]string{"gid": "me", "name": "Me"}, "created_at": "2026-11-13T01:00:00Z"})
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":[{"message":"not in the fake"}]}`))
	}
}

func timeServer(t *testing.T, st *store.Store, fake *timeAsana) *Server {
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	return &Server{Store: st, NewSyncer: func(context.Context) (*sync.Syncer, error) {
		api := asana.NewClient(srv.Client())
		api.BaseURL = srv.URL
		api.MaxRetries = 0
		return &sync.Syncer{API: api, Store: st, Cfg: sync.Config{CompletedDays: 14}}, nil
	}}
}

// list_time_entries reads the entries from Asana; add_time_entry sends the minutes and day
// and returns Asana's gid; update / delete queue their change offline.
func TestTimeEntryTools(t *testing.T) {
	st := seed(t)
	fake := &timeAsana{}
	cs := connect(t, timeServer(t, st, fake))
	var list TimeEntriesOutput
	if res := call(t, cs, "list_time_entries", map[string]any{"task": "t1"}, &list); res.IsError {
		t.Fatal(toolError(res))
	}
	if len(list.Entries) != 1 || list.Entries[0].GID != "te1" || list.Total != "30m" || list.Note != "" {
		t.Fatalf("list = %+v", list)
	}
	var w WriteOutput
	if res := call(t, cs, "add_time_entry", map[string]any{"task": "t1", "duration": "1h15m", "date": "yesterday"}, &w); res.IsError {
		t.Fatal(toolError(res))
	}
	if !w.Sent || w.TimeEntryGID != "te2" {
		t.Errorf("add = %+v", w)
	}
	if !slices.Contains(fake.calls, `POST /tasks/t1/time_tracking_entries {"data":{"duration_minutes":75,"entered_on":"2026-11-12"}}`) {
		t.Errorf("calls %q", fake.calls)
	}
	if res := call(t, cs, "add_time_entry", map[string]any{"task": "t1", "duration": "abc"}, &w); !res.IsError {
		t.Error("a bad duration: no error")
	}
	if res := call(t, cs, "update_time_entry", map[string]any{"gid": "te1"}, &w); !res.IsError {
		t.Error("update without a change: no error")
	}
}

// After a 402 the tools are not offered; a 402 on the list says why.
func TestTimeEntryToolsPaymentRequired(t *testing.T) {
	st := seed(t)
	fake := &timeAsana{unpaid: true}
	cs := connect(t, timeServer(t, st, fake))
	var list TimeEntriesOutput
	res := call(t, cs, "list_time_entries", map[string]any{"task": "t1"}, &list)
	if !res.IsError || !strings.Contains(toolError(res), "タイムトラッキング") {
		t.Fatalf("list after 402: %+v", res)
	}
	ctx := context.Background()
	if _, ok, _ := st.PlanRefusedAt(ctx, plan.TimeTracking, "w1"); !ok {
		t.Fatal("402 not recorded")
	}
	// The syncer recorded it by the real clock; the server below runs at testNow.
	if err := st.RecordPlanRefusal(ctx, plan.TimeTracking, "w1", testNow); err != nil {
		t.Fatal(err)
	}
	cs = connect(t, &Server{Store: st})
	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if strings.Contains(tool.Name, "time_entr") {
			t.Errorf("%s offered after a 402", tool.Name)
		}
	}
}
