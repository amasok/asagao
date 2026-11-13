package asana

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newTestClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := NewClient(srv.Client())
	c.BaseURL = srv.URL
	c.sleep = func(context.Context, time.Duration) error { return nil }
	return c
}

func TestListPaginates(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("limit") != "100" {
			t.Errorf("limit = %q", r.URL.Query().Get("limit"))
		}
		switch r.URL.Query().Get("offset") {
		case "":
			fmt.Fprint(w, `{"data":[{"gid":"1","name":"a"}],"next_page":{"offset":"next"}}`)
		case "next":
			fmt.Fprint(w, `{"data":[{"gid":"2","name":"b"}],"next_page":null}`)
		default:
			t.Errorf("unexpected offset %q", r.URL.Query().Get("offset"))
		}
	})
	ws, err := c.Workspaces(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 2 || ws[0].GID != "1" || ws[1].GID != "2" {
		t.Fatalf("got %+v", ws)
	}
}

func TestRetriesOn429(t *testing.T) {
	calls := 0
	var waited []time.Duration
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, `{"data":{"gid":"me","name":"Me"}}`)
	})
	c.sleep = func(_ context.Context, d time.Duration) error { waited = append(waited, d); return nil }
	u, err := c.Me(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if u.GID != "me" || calls != 3 {
		t.Fatalf("gid=%s calls=%d", u.GID, calls)
	}
	if len(waited) != 2 || waited[0] != 7*time.Second {
		t.Fatalf("waited %v", waited)
	}
}

func TestErrorResponse(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		fmt.Fprint(w, `{"errors":[{"message":"premium only"}]}`)
	})
	_, err := c.Me(context.Background())
	if !IsPaymentRequired(err) {
		t.Fatalf("err = %v", err)
	}
	if err.Error() != "asana: 402 premium only" {
		t.Fatalf("message = %q", err.Error())
	}
}

func TestTasksQuery(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("assignee") != "me" || q.Get("workspace") != "w1" || q.Get("completed_since") != "2026-11-01T00:00:00Z" {
			t.Errorf("query = %v", q)
		}
		if q.Get("project") != "" {
			t.Errorf("project should be empty")
		}
		fmt.Fprint(w, `{"data":[{"gid":"t1","name":"task","due_on":"2026-11-20","assignee":{"gid":"me"}}]}`)
	})
	tasks, err := c.Tasks(context.Background(), TaskQuery{
		Assignee: "me", Workspace: "w1",
		CompletedSince: time.Date(2026, 11, 1, 9, 0, 0, 0, time.FixedZone("JST", 9*3600)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].DueOn != "2026-11-20" || tasks[0].Assignee.GID != "me" {
		t.Fatalf("got %+v", tasks)
	}
}
