package sync

import (
	"context"
	"net/http"
	"testing"

	"github.com/amasok/asagao/internal/store"
)

// A failing listing of the order (5xx, 429) does not fail the sync: it is left pending and
// taken at the next one; the order shown stays as it was meanwhile.
func TestAttackSectionOrderTransientFailure(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusBadGateway, http.StatusTooManyRequests} {
		e, of := setupOrder(t)
		e.run(t, 0)
		e.fake.events = func(got string) (int, string) {
			if got == "T0" {
				return http.StatusOK, eventsJSON([]string{taskEvent("t1", "changed")}, "T1", false)
			}
			return http.StatusOK, eventsJSON(nil, got, false)
		}
		of.section = []string{"t1", "t3", "t2"}
		of.status = status
		if res := e.run(t, 5*60e9); res.OrderPending == 0 {
			t.Fatalf("%d: nothing pending after a failed listing", status)
		}
		e.wantOrder(t, store.TaskFilter{SectionGID: "s1"}, "t3", "t2", "t1")
		of.status = 0
		if res := e.run(t, 5*60e9); res.OrderPending != 0 {
			t.Fatalf("%d: still pending: %d", status, res.OrderPending)
		}
		e.wantOrder(t, store.TaskFilter{SectionGID: "s1"}, "t1", "t3", "t2")
	}
}

// Offline: the sync fails as before and the order is untouched.
func TestAttackOrderOffline(t *testing.T) {
	e, of := setupOrder(t)
	e.run(t, 0)
	e.fake.events = func(got string) (int, string) {
		return http.StatusOK, eventsJSON([]string{taskEvent("t1", "changed")}, got+"x", false)
	}
	of.section = []string{"t1", "t3", "t2"}
	e.srv.Close()
	e.now = e.now.Add(5 * 60e9)
	if _, err := e.s.Run(context.Background(), false); err == nil {
		t.Fatal("offline sync reported no error")
	}
	e.wantOrder(t, store.TaskFilter{SectionGID: "s1"}, "t3", "t2", "t1")
}
