package mcpserver

import (
	"context"
	"fmt"
	"github.com/amasok/asagao/internal/wire"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/sync"
)

// A create Asana answered with a server error, and that Asana does not have yet, stays
// queued (it is checked in Asana again on the next push, so as not to create it twice):
// the push itself returns no error, but the change has not reached Asana, so the tool must
// not say it was sent (#75: the CLI already looked at Remaining, the MCP server did not).
func TestWriteLeftQueuedIsNotSent(t *testing.T) {
	st := seed(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/users/me":
			fmt.Fprint(w, `{"data":{"gid":"me","name":"Me"}}`)
		case r.Method == http.MethodGet: // looking for what the create may have made: nothing
			fmt.Fprint(w, `{"data":[]}`)
		default:
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"errors":[{"message":"server error"}]}`)
		}
	}))
	t.Cleanup(srv.Close)
	cs := connect(t, &Server{App: wire.UseCases(st), NewSyncer: func(context.Context) (*sync.Syncer, error) {
		api := asana.NewClient(srv.Client())
		api.BaseURL = srv.URL
		return &sync.Syncer{API: api, Store: st, Cfg: sync.Config{CompletedDays: 14}}, nil
	}})
	var w WriteOutput
	call(t, cs, "create_task", map[string]any{"name": "届いていない", "project": "p1"}, &w)
	if w.Sent || !strings.Contains(w.Note, "still queued") {
		t.Fatalf("output = %+v; want not sent, with a note that the change is still queued", w)
	}
}
