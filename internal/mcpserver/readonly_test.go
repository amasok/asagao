package mcpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/config"
	"github.com/amasok/asagao/internal/sync"
)

// A read-only server's sync only pulls: changes the user queued (in the TUI or CLI) stay
// in the outbox, so an agent cannot send them to Asana.
func TestReadOnlySyncDoesNotSendOutbox(t *testing.T) {
	st := seed(t)
	ctx := context.Background()
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"completed": true}, testNow); err != nil {
		t.Fatal(err)
	}
	var gets int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("read-only sync sent %s %s", r.Method, r.URL)
			http.Error(w, "no", http.StatusBadRequest)
			return
		}
		gets++
		switch r.URL.Path {
		case "/users/me":
			fmt.Fprint(w, `{"data":{"gid":"me","name":"Me"}}`)
		default:
			fmt.Fprint(w, `{"data":[]}`)
		}
	}))
	t.Cleanup(srv.Close)
	cs := connect(t, &Server{Store: st, ReadOnly: true, NewSyncer: func(context.Context) (*sync.Syncer, error) {
		api := asana.NewClient(srv.Client())
		api.BaseURL = srv.URL
		return &sync.Syncer{API: api, Store: st, Cfg: config.SyncConfig{CompletedDays: 14}}, nil
	}})

	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if tool.Name == "sync" && !strings.Contains(tool.Description, "read-only") {
			t.Errorf("sync description should say it does not send: %q", tool.Description)
		}
	}

	var out SyncOutput
	res := call(t, cs, "sync", map[string]any{"full": true}, &out)
	if res.IsError {
		t.Fatalf("sync: %s", toolError(res))
	}
	if gets == 0 {
		t.Error("sync should still pull from Asana")
	}
	if out.Result.Push.Sent != 0 || out.Status.Pending != 1 || out.Result.Push.Remaining != 1 {
		t.Errorf("queued change should stay unsent: result=%+v status=%+v", out.Result.Push, out.Status)
	}
	if d, _ := st.GetTask(ctx, "t1"); !d.Completed {
		t.Error("the queued local change must survive the pull")
	}
}

// Push itself refuses on a pull-only syncer (defence in depth for other callers).
func TestPullOnlySyncerRefusesPush(t *testing.T) {
	s := &sync.Syncer{Store: seed(t), PullOnly: true}
	if _, err := s.Push(context.Background()); err != sync.ErrPullOnly {
		t.Fatalf("Push on a pull-only syncer = %v, want ErrPullOnly", err)
	}
}
