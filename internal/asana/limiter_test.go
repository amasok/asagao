package asana

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/amasok/asagao/internal/asana/model"
)

// eventLog is shared by a test server and a limiter, to see the order of waits and requests.
type eventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *eventLog) add(e string) {
	l.mu.Lock()
	l.events = append(l.events, e)
	l.mu.Unlock()
}

func (l *eventLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.events, ",")
}

// logLimiter records each Wait in log and returns err.
type logLimiter struct {
	log *eventLog
	err error
}

func (l logLimiter) Wait(context.Context) error {
	l.log.add("wait")
	return l.err
}

// TestLimiterWaitsBeforeEachRequest: with a Limiter, every HTTP request (a 429's retry
// and each page of a list too) is preceded by one Wait.
func TestLimiterWaitsBeforeEachRequest(t *testing.T) {
	log := &eventLog{}
	limited := false
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		log.add("request")
		switch {
		case r.URL.Query().Get("offset") == "" && !limited:
			limited = true
			w.WriteHeader(http.StatusTooManyRequests)
		case r.URL.Query().Get("offset") == "":
			fmt.Fprint(w, `{"data":[{"gid":"1","name":"a"}],"next_page":{"offset":"next"}}`)
		default:
			fmt.Fprint(w, `{"data":[{"gid":"2","name":"b"}],"next_page":null}`)
		}
	})
	c.MaxRetries = 1
	c.Limiter = logLimiter{log: log}
	ws, err := c.Workspaces(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 2 {
		t.Fatalf("workspaces = %v", ws)
	}
	if got, want := log.String(), "wait,request,wait,request,wait,request"; got != want {
		t.Fatalf("events = %s, want %s", got, want)
	}
	if c.Requests() != 3 {
		t.Fatalf("Requests() = %d, want 3", c.Requests())
	}
}

// TestLimiterErrorSendsNothing: when Wait fails (ctx canceled while waiting, or another
// error such as *rate.Limiter's "would exceed context deadline"), the request is not sent,
// and the error wraps Wait's, is a *url.Error (a network error) and says it was not sent.
func TestLimiterErrorSendsNothing(t *testing.T) {
	for _, werr := range []error{context.Canceled, errors.New("rate: Wait(n=1) would exceed context deadline")} {
		t.Run(werr.Error(), func(t *testing.T) {
			log := &eventLog{}
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				log.add("request")
				fmt.Fprint(w, `{"data":{"gid":"9"},"next_page":null}`)
			})
			c.Limiter = logLimiter{log: log, err: werr}
			calls := map[string]func() error{
				"list": func() error { _, err := c.Workspaces(context.Background()); return err },
				"upload": func() error {
					_, err := c.UploadAttachment(context.Background(), "1", "f.txt", []byte("x"))
					return err
				},
				"raw": func() error {
					_, _, err := c.Raw(context.Background(), http.MethodGet, "/users/me", nil)
					return err
				},
			}
			for name, call := range calls {
				err := call()
				if !errors.Is(err, werr) {
					t.Errorf("%s: err = %v, want it to wrap %v", name, err, werr)
				}
				var urlErr *url.Error
				if !errors.As(err, &urlErr) {
					t.Errorf("%s: err = %T, want a *url.Error", name, err)
				}
				var notSent interface{ RequestNotSent() bool }
				if !errors.As(err, &notSent) || !notSent.RequestNotSent() {
					t.Errorf("%s: err = %v does not say the request was not sent", name, err)
				}
			}
			if got := log.String(); got != "wait,wait,wait" {
				t.Fatalf("events = %s, want only the waits", got)
			}
			if c.Requests() != 0 {
				t.Fatalf("Requests() = %d, want 0", c.Requests())
			}
		})
	}
}

// TestLimiterPacesUploadAndRaw: the requests that do not go through do (an upload's
// multipart POST, `asagao api`) are paced too.
func TestLimiterPacesUploadAndRaw(t *testing.T) {
	log := &eventLog{}
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		log.add("request")
		fmt.Fprint(w, `{"data":{"gid":"9","name":"f.txt"}}`)
	})
	c.Limiter = logLimiter{log: log}
	if _, err := c.UploadAttachment(context.Background(), "1", "f.txt", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Raw(context.Background(), http.MethodGet, "/users/me", nil); err != nil {
		t.Fatal(err)
	}
	if got, want := log.String(), "wait,request,wait,request"; got != want {
		t.Fatalf("events = %s, want %s", got, want)
	}

}

// TestLimiterCountsEachBatchAction: Asana counts each action of a POST /batch against the
// rate limit, so the limiter waits once per action (writes and reads), and again for each
// action when the batch is retried.
func TestLimiterCountsEachBatchAction(t *testing.T) {
	log := &eventLog{}
	cur := log // where the server writes
	failed := false
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		cur.add("request")
		var body struct {
			Data struct {
				Actions []map[string]any `json:"actions"`
			} `json:"data"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Data.Actions[0]["method"] != "get" && !failed {
			failed = true
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		var res []string
		for range body.Data.Actions {
			res = append(res, `{"status_code":200,"body":{"data":{"gid":"1","name":"x"}}}`)
		}
		fmt.Fprintf(w, `{"data":[%s]}`, strings.Join(res, ","))
	})
	c.MaxRetries = 1
	c.Limiter = logLimiter{log: log}
	if _, err := c.Batch(context.Background(), []BatchWrite{
		model.BatchUpdateTask("1", map[string]any{"completed": true}),
		model.BatchAddTag("2", "g"),
		model.BatchAddTag("3", "g"),
	}); err != nil {
		t.Fatal(err)
	}
	if got, want := log.String(), "wait,wait,wait,request,wait,wait,wait,request"; got != want {
		t.Fatalf("Batch of 3 retried once: events = %s, want %s", got, want)
	}

	log2 := &eventLog{}
	cur = log2
	c.Limiter = logLimiter{log: log2}
	if _, _, err := c.TasksByGID(context.Background(), []string{"1", "2"}); err != nil {
		t.Fatal(err)
	}
	if got, want := log2.String(), "wait,wait,request"; got != want {
		t.Fatalf("TasksByGID of 2: events = %s, want %s", got, want)
	}
}
