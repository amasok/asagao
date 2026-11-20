package mcpserver

import (
	"context"
	"github.com/amasok/asagao/internal/wire"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/store"
	"github.com/amasok/asagao/internal/sync"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func hostileServer(t *testing.T, st *store.Store, status int) *Server {
	inner := (&findAsana{}).serve(t)
	t.Cleanup(inner.Close)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/tasks/search") || strings.HasSuffix(r.URL.Path, "/typeahead") {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"errors":[{"message":"bad \u001b[2J\u001b]0;pwned\u0007 thing"}]}`))
			return
		}
		http.Redirect(w, r, inner.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(srv.Close)
	return &Server{App: wire.UseCases(st), NewSyncer: func(context.Context) (*sync.Syncer, error) {
		api := asana.NewClient(srv.Client())
		api.BaseURL, api.MaxRetries = srv.URL, 0
		return &sync.Syncer{API: api, Store: st}, nil
	}}
}

// The note of a failed remote search never carries control characters from Asana's message (#36).
func TestSearchTasksRemoteNoteIsTermsafe(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusBadRequest, http.StatusInternalServerError} {
		st := seed(t)
		cs := connect(t, hostileServer(t, st, status))
		var out SearchOutput
		call(t, cs, "search_tasks", map[string]any{"query": "ソファ", "remote": true}, &out)
		if out.Count != 1 || !strings.Contains(out.Note, "could not be searched") {
			t.Fatalf("%d: %+v", status, out)
		}
		for _, r := range out.Note {
			if r == 0x1b || r == 7 {
				t.Fatalf("%d: control character in note %q", status, out.Note)
			}
		}
	}
}

// A blank query is refused with or without remote, and Asana is not asked.
func TestSearchTasksRemoteBlankQuery(t *testing.T) {
	st := seed(t)
	f := &findAsana{}
	cs := connect(t, findServer(t, st, f, true))
	for _, q := range []string{"", "  ", "\t"} {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "search_tasks", Arguments: map[string]any{"query": q, "remote": true}})
		if err == nil && !res.IsError {
			t.Errorf("blank query %q accepted", q)
		}
	}
	if f.searches != 0 || len(f.typeaheads) != 0 {
		t.Fatalf("asked Asana: %d %v", f.searches, f.typeaheads)
	}
}
