package asana

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
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

// TestLimiterErrorSendsNothing: when Wait fails (e.g. ctx canceled while waiting), the
// request is not sent and the error is returned, wrapping Wait's.
func TestLimiterErrorSendsNothing(t *testing.T) {
	log := &eventLog{}
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		log.add("request")
		fmt.Fprint(w, `{"data":[],"next_page":null}`)
	})
	c.Limiter = logLimiter{log: log, err: context.Canceled}
	_, err := c.Workspaces(context.Background())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if got := log.String(); got != "wait" {
		t.Fatalf("events = %s, want only the wait", got)
	}
	if c.Requests() != 0 {
		t.Fatalf("Requests() = %d, want 0", c.Requests())
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
