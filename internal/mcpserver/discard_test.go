package mcpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/sync"
)

// A write to a task deleted in Asana is dropped, and the tool says so.
func TestWriteToDeletedTaskReportsDiscard(t *testing.T) {
	st := seed(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/users/me" {
			fmt.Fprint(w, `{"data":{"gid":"me","name":"Me"}}`)
			return
		}
		w.WriteHeader(http.StatusNotFound) // t2 no longer exists
		fmt.Fprint(w, `{"errors":[{"message":"task not found"}]}`)
	}))
	t.Cleanup(srv.Close)
	cs := connect(t, &Server{Store: st, NewSyncer: func(context.Context) (*sync.Syncer, error) {
		api := asana.NewClient(srv.Client())
		api.BaseURL = srv.URL
		return &sync.Syncer{API: api, Store: st, Cfg: sync.Config{CompletedDays: 14}}, nil
	}})
	var w WriteOutput
	call(t, cs, "update_task", map[string]any{"gid": "t2", "completed": true}, &w)
	if w.Sent || len(w.Discarded) != 1 || w.Discarded[0].Summary != "完了にする" || w.Note == "" {
		t.Fatalf("output = %+v", w)
	}
}
