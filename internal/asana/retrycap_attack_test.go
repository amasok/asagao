package asana

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A POST that gets 5xx is not retried (it may have been applied), cap or no cap.
func TestMaxRetryDelayDoesNotRetryPost5xx(t *testing.T) {
	n := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		n++
		w.WriteHeader(http.StatusBadGateway)
	})
	c.MaxRetries, c.MaxRetryDelay = 3, time.Second
	heard := 0
	c.OnRetry = func(int, time.Duration) { heard++ }
	if _, err := c.do(context.Background(), http.MethodPost, "/tasks", nil, map[string]any{"name": "x"}, nil); err == nil {
		t.Fatal("want error")
	}
	if n != 1 || heard != 0 {
		t.Errorf("calls=%d OnRetry=%d, want 1 and 0", n, heard)
	}
}

// 401/403/404 are final: no retry, no OnRetry.
func TestOnRetryNotCalledForClientErrors(t *testing.T) {
	for _, st := range []int{400, 401, 403, 404, 402} {
		n := 0
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { n++; w.WriteHeader(st) })
		c.MaxRetries, c.MaxRetryDelay = 3, time.Second
		c.OnRetry = func(int, time.Duration) { t.Errorf("status %d: OnRetry called", st) }
		if _, err := c.Me(context.Background()); err == nil || n != 1 {
			t.Errorf("status %d: err=%v calls=%d", st, err, n)
		}
	}
}

// The default client (what sync, TUI and MCP use) is not capped and has no callback.
func TestNewClientHasNoCapOrCallback(t *testing.T) {
	c := NewClient(http.DefaultClient)
	if c.MaxRetryDelay != 0 || c.OnRetry != nil {
		t.Fatalf("cap=%v onretry=%v", c.MaxRetryDelay, c.OnRetry != nil)
	}
	var slept []time.Duration
	n := 0
	c = newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		n++
		if n < 3 {
			w.Header().Set("Retry-After", "45")
			w.WriteHeader(429)
			return
		}
		w.Write([]byte(`{"data":{"gid":"1"}}`))
	})
	c.sleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
	if _, err := c.Me(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(slept) != 2 || slept[0] != 45*time.Second {
		t.Errorf("slept %v", slept)
	}
}

// A Retry-After that is zero, negative or too large for a Duration must not make the
// announced or actual wait negative (the user would read "-5s").
func TestRetryAfterOddValuesGiveSaneWait(t *testing.T) {
	for _, ra := range []string{"0", "-5", "9223372036", "99999999999999", "abc", "1.5", " 3", "Wed, 21 Oct 2026 07:28:00 GMT"} {
		n := 0
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			n++
			if n == 1 {
				w.Header().Set("Retry-After", ra)
				w.WriteHeader(503)
				return
			}
			w.Write([]byte(`{"data":{"gid":"1"}}`))
		})
		c.MaxRetries, c.MaxRetryDelay = 1, 2*time.Second
		var announced, slept time.Duration
		c.OnRetry = func(_ int, d time.Duration) { announced = d }
		c.sleep = func(_ context.Context, d time.Duration) error { slept = d; return nil }
		if _, err := c.Me(context.Background()); err != nil {
			t.Errorf("Retry-After %q: %v", ra, err)
			continue
		}
		if announced < 0 || announced > 2*time.Second || slept != announced {
			t.Errorf("Retry-After %q: announced %v, slept %v", ra, announced, slept)
		}
	}
}

// Cancelling during the capped wait returns promptly with the context error and does not
// send another request.
func TestCancelDuringCappedWait(t *testing.T) {
	n := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		n++
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(503)
	})
	c.sleep = nil // real timer
	c.MaxRetries, c.MaxRetryDelay = 1, 5*time.Second
	ctx, cancel := context.WithCancel(context.Background())
	c.OnRetry = func(int, time.Duration) { cancel() }
	start := time.Now()
	_, err := c.Me(ctx)
	if err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > time.Second || n != 1 {
		t.Errorf("took %v, %d calls", time.Since(start), n)
	}
}
