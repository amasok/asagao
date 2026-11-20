package mcpserver

import (
	"context"
	"fmt"
	"github.com/amasok/asagao/internal/wire"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/sync"
)

// The syncer is kept for later calls, so it must not be made with the push's 20 second
// timeout context: that context is done after the first call.
func TestPushSyncerNotMadeWithTimeoutContext(t *testing.T) {
	st := seed(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/users/me" {
			fmt.Fprint(w, `{"data":{"gid":"me","name":"Me"}}`)
			return
		}
		fmt.Fprint(w, `{"data":[]}`)
	}))
	t.Cleanup(srv.Close)
	var hadDeadline []bool
	cs := connect(t, &Server{App: wire.UseCases(st), NewSyncer: func(ctx context.Context) (*sync.Syncer, error) {
		_, ok := ctx.Deadline()
		hadDeadline = append(hadDeadline, ok)
		api := asana.NewClient(srv.Client())
		api.BaseURL = srv.URL
		return &sync.Syncer{API: api, Store: st, Cfg: sync.Config{CompletedDays: 14}}, nil
	}})
	var w WriteOutput
	call(t, cs, "create_task", map[string]any{"name": "a", "project": "p1"}, &w)
	if len(hadDeadline) != 1 {
		t.Fatalf("NewSyncer called %d times", len(hadDeadline))
	}
	if hadDeadline[0] {
		t.Error("the kept syncer was made with the push's timeout context")
	}
}
