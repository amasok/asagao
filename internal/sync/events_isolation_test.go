package sync

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	stdsync "sync"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/store"
)

// #27 (tester): a second project p2 next to the fake's p1. p1's events fail in various
// ways; only p1 falls back to a project fetch, p2 keeps using its events, the sync
// succeeds, and only p2's token advances.
func TestEventsFailureFallsBackOnlyThatProject(t *testing.T) {
	for _, tc := range []struct {
		name   string
		events func(token string) (int, string) // p1's events after the first sync
		batch  bool                             // p1's events are fine, but its batch fails
	}{
		{name: "429", events: func(string) (int, string) { return http.StatusTooManyRequests, `{"errors":[{"message":"slow down"}]}` }},
		{name: "503", events: func(string) (int, string) { return http.StatusServiceUnavailable, `{"errors":[{"message":"down"}]}` }},
		{name: "dropped", events: func(string) (int, string) { return 0, "" }},
		{name: "expired", events: func(string) (int, string) {
			return http.StatusPreconditionFailed, `{"errors":[{"message":"Sync token invalid or too old"}],"sync":"P1NEW"}`
		}},
		{name: "batch 500", batch: true, events: func(token string) (int, string) {
			return 200, eventsJSON([]string{taskEvent("t1", "changed")}, "T1", false)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := setupEvents(t)
			var mu stdsync.Mutex
			var p2Fetches, p2Events int
			p2Token := ""
			failBatch := false
			wrap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				q := r.URL.Query()
				switch {
				case r.URL.Path == "/projects":
					fmt.Fprint(w, `{"data":[{"gid":"p1","name":"P","members":[{"gid":"u1"}]},{"gid":"p2","name":"Q","members":[{"gid":"u1"}]}]}`)
				case r.URL.Path == "/events" && q.Get("resource") == "p2":
					p2Events++
					if q.Get("sync") == "" {
						w.WriteHeader(http.StatusPreconditionFailed)
						fmt.Fprint(w, `{"errors":[{"message":"no token"}],"sync":"Q0"}`)
						return
					}
					p2Token = q.Get("sync")
					fmt.Fprint(w, eventsJSON(nil, "Q1", false))
				case r.URL.Path == "/projects/p2/sections" || r.URL.Path == "/projects/p2/custom_field_settings":
					fmt.Fprint(w, `{"data":[]}`)
				case r.URL.Path == "/tasks" && q.Get("project") == "p2":
					p2Fetches++
					fmt.Fprint(w, `{"data":[{"gid":"q1","name":"Q one","memberships":[{"project":{"gid":"p2"}}]}]}`)
				case r.URL.Path == "/batch" && failBatch:
					w.WriteHeader(http.StatusInternalServerError)
				default:
					mu.Unlock()
					e.fake.ServeHTTP(w, r)
					mu.Lock()
				}
			}))
			defer wrap.Close()
			e.s.API.(*asana.Client).BaseURL = wrap.URL
			e.run(t, 0) // both projects: 412, full fetch
			mu.Lock()
			first := p2Fetches
			mu.Unlock()
			if first != 1 {
				t.Fatalf("setup: p2 fetched %d times", first)
			}
			ctx := context.Background()
			if v, _ := e.st.SyncState(ctx, store.KeyEvents+"p2"); v != "Q0" {
				t.Fatalf("setup: p2 token %q", v)
			}
			e.fake.mu.Lock()
			e.fake.events = tc.events
			e.fake.mu.Unlock()
			e.fake.setTask("t1", "changed in Asana", "")
			mu.Lock()
			failBatch = tc.batch
			p2Fetches = 0
			mu.Unlock()
			res := e.run(t, 5*time.Minute)
			mu.Lock()
			defer mu.Unlock()
			if res.Poll || res.Full {
				t.Fatalf("setup: expected an events sync: %+v", res)
			}
			if res.Fallbacks != 1 {
				t.Errorf("fallbacks = %d, want 1 (p1 only)", res.Fallbacks)
			}
			if got := e.fake.calls("GET /tasks project=p1"); len(got) != 1 {
				t.Errorf("p1 fetched %d times, want 1 fallback: %v", len(got), e.fake.log)
			}
			if p2Fetches != 0 {
				t.Errorf("p2 fetched %d times: its events worked, it must not fall back", p2Fetches)
			}
			if p2Token != "Q0" {
				t.Errorf("p2 events read with token %q", p2Token)
			}
			if v, _ := e.st.SyncState(ctx, store.KeyEvents+"p2"); v != "Q1" {
				t.Errorf("p2 token = %q, want Q1", v)
			}
			p1, _ := e.st.SyncState(ctx, store.KeyEvents+"p1")
			switch tc.name {
			case "expired":
				if p1 != "P1NEW" {
					t.Errorf("p1 token = %q, want the new one from the 412", p1)
				}
			case "batch 500":
				// the batch failed but the fallback fetch covered the change; either the old
				// or the new token is fine, as long as the change is there (checked below)
			default:
				if p1 != "T0" {
					t.Errorf("p1 token = %q, want T0 kept", p1)
				}
			}
			if got := e.taskName(t, "t1"); got != "changed in Asana" {
				t.Errorf("t1 = %q: p1's change not fetched by the fallback", got)
			}
		})
	}
}

// Unsent local edits win over Asana's values on every way a task is fetched: the
// fallback fetch after an events failure, and the hourly poll.
func TestEventsUnsentEditWinsOnFallbackAndPoll(t *testing.T) {
	for _, tc := range []struct {
		name  string
		after time.Duration
		ev    func(string) (int, string)
	}{
		{"fallback 503", 5 * time.Minute, func(string) (int, string) { return http.StatusServiceUnavailable, `{}` }},
		{"fallback expired", 5 * time.Minute, func(string) (int, string) {
			return http.StatusPreconditionFailed, `{"errors":[{"message":"too old"}],"sync":"N"}`
		}},
		{"poll", time.Hour, func(string) (int, string) { return 200, eventsJSON(nil, "T0", false) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := setupEvents(t)
			e.run(t, 0)
			ctx := context.Background()
			if err := e.st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "local edit"}, e.now); err != nil {
				t.Fatal(err)
			}
			e.s.PullOnly = true // keep it queued
			e.fake.setTask("t1", "server edit", "")
			e.fake.setTask("t2", "server edit 2", "")
			e.fake.mu.Lock()
			e.fake.events = tc.ev
			e.fake.mu.Unlock()
			e.run(t, tc.after)
			if got := e.fake.calls("GET /tasks project=p1"); len(got) != 1 {
				t.Fatalf("setup: expected one project fetch: %v", e.fake.log)
			}
			if got := e.taskName(t, "t1"); got != "local edit" {
				t.Fatalf("t1 = %q: the unsent edit must win", got)
			}
			if got := e.taskName(t, "t2"); got != "server edit 2" {
				t.Fatalf("t2 = %q: other tasks still take Asana's values", got)
			}
		})
	}
}
