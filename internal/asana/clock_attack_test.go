package asana

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// #21 (tester): once the offset is known, a later response without a usable Date header
// (missing, garbage, empty) must not reset it to 0: the PC's wrong clock would come back.
func TestClockOffsetKeptOnBadDateHeader(t *testing.T) {
	for _, bad := range []string{"", "garbage", "Mon, 32 Foo 2026 99:99:99 GMT", "2026-11-16T00:00:00Z"} {
		t.Run(fmt.Sprintf("%q", bad), func(t *testing.T) {
			first := true
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if first {
					w.Header().Set("Date", time.Now().Add(-3*time.Hour).UTC().Format(http.TimeFormat))
				} else if bad == "" {
					w.Header()["Date"] = nil
				} else {
					w.Header()["Date"] = []string{bad}
				}
				first = false
				fmt.Fprint(w, `{"data":{"gid":"u1"}}`)
			})
			for i := 0; i < 2; i++ {
				if _, err := c.Me(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			if d := c.ClockOffset() + 3*time.Hour; d < -2*time.Second || d > 2*time.Second {
				t.Fatalf("offset = %v after a bad Date header, want about -3h", c.ClockOffset())
			}
		})
	}
}

// The latest response wins: a PC clock corrected (or Asana's Date moving) is followed,
// in both directions, and the noise band brings it back to exactly 0.
func TestClockOffsetFollowsLatestResponse(t *testing.T) {
	shifts := []time.Duration{2 * time.Hour, -45 * time.Minute, 3 * time.Second}
	want := []time.Duration{2 * time.Hour, -45 * time.Minute, 0}
	i := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Date", time.Now().Add(shifts[i]).UTC().Format(http.TimeFormat))
		fmt.Fprint(w, `{"data":{"gid":"u1"}}`)
	})
	for ; i < len(shifts); i++ {
		if _, err := c.Me(context.Background()); err != nil {
			t.Fatal(err)
		}
		if d := c.ClockOffset() - want[i]; d < -2*time.Second || d > 2*time.Second {
			t.Fatalf("step %d: offset = %v, want about %v", i, c.ClockOffset(), want[i])
		}
		if want[i] == 0 && c.ClockOffset() != 0 {
			t.Fatalf("step %d: offset within noise must be exactly 0, got %v", i, c.ClockOffset())
		}
	}
}
