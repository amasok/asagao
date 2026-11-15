package asana

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// The offset between Asana's clock and ours is measured from the Date header (#21).
func TestClockOffsetFromDateHeader(t *testing.T) {
	for _, tc := range []struct {
		name   string
		shift  time.Duration // Asana's clock minus ours
		noDate bool
		want   time.Duration
	}{
		{name: "asana ahead", shift: 10 * time.Minute, want: 10 * time.Minute},
		{name: "asana behind (pc clock fast)", shift: -3 * time.Hour, want: -3 * time.Hour},
		{name: "noise is ignored", shift: 2 * time.Second, want: 0},
		{name: "no date header", noDate: true, want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if tc.noDate {
					w.Header()["Date"] = nil // net/http leaves the header out
				} else {
					w.Header().Set("Date", time.Now().Add(tc.shift).UTC().Format(http.TimeFormat))
				}
				fmt.Fprint(w, `{"data":{"gid":"u1"}}`)
			})
			if c.ClockOffset() != 0 {
				t.Fatal("offset before any response must be 0")
			}
			if _, err := c.Me(context.Background()); err != nil {
				t.Fatal(err)
			}
			if d := c.ClockOffset() - tc.want; d < -2*time.Second || d > 2*time.Second {
				t.Fatalf("offset = %v, want about %v", c.ClockOffset(), tc.want)
			}
		})
	}
}

// An error response (e.g. the 5xx after which a create is verified) also tells the offset.
func TestClockOffsetFromErrorResponse(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Date", time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat))
		w.WriteHeader(http.StatusInternalServerError)
	})
	c.MaxRetries = 0
	if _, err := c.CreateTask(context.Background(), map[string]any{"name": "x"}); err == nil {
		t.Fatal("want error")
	}
	if d := c.ClockOffset() + time.Hour; d < -2*time.Second || d > 2*time.Second {
		t.Fatalf("offset = %v, want about -1h", c.ClockOffset())
	}
}
