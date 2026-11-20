package mcpserver

import (
	"context"
	"fmt"
	"github.com/amasok/asagao/internal/wire"
	"net/http"
	"net/http/httptest"
	"strings"
	stdsync "sync"
	"testing"

	"golang.org/x/oauth2"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/auth"
	"github.com/amasok/asagao/internal/sync"
)

// Not logged in, then a token Asana rejects (a revoked PAT), then a new login in a
// terminal: writes stay queued with a note telling the user to log in, and once they
// have, the server builds its syncer again and sends with the new token.
func TestLoginProblemsAreExplainedAndNewLoginIsUsed(t *testing.T) {
	st := seed(t)
	ctx := context.Background()
	var mu stdsync.Mutex
	valid, stored := "old-pat", "" // the token Asana accepts / the one asagao has stored
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		ok := r.Header.Get("Authorization") == "Bearer "+valid
		mu.Unlock()
		switch {
		case !ok:
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"errors":[{"message":"Not Authorized"}]}`)
		case r.URL.Path == "/users/me":
			fmt.Fprint(w, `{"data":{"gid":"me","name":"Me"}}`)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/tasks/"):
			fmt.Fprintf(w, `{"data":{"gid":%q,"name":"x"}}`, strings.TrimPrefix(r.URL.Path, "/tasks/"))
		case r.Method == http.MethodPut:
			fmt.Fprintf(w, `{"data":{"gid":%q,"name":"x","completed":true}}`, strings.TrimPrefix(r.URL.Path, "/tasks/"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	builds := 0
	cs := connect(t, &Server{App: wire.UseCases(st), NewSyncer: func(context.Context) (*sync.Syncer, error) {
		mu.Lock()
		tok := stored
		mu.Unlock()
		if tok == "" {
			return nil, auth.ErrNotLoggedIn
		}
		builds++
		api := asana.NewClient(oauth2.NewClient(ctx, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: tok})))
		api.BaseURL = srv.URL
		api.MaxRetries = 0
		api.AuthHint = (&auth.Source{Method: "personal_access_token"}).UnauthorizedHint()
		return &sync.Syncer{API: api, Store: st, Cfg: sync.Config{CompletedDays: 14}}, nil
	}})

	// 1. Not logged in.
	var w WriteOutput
	call(t, cs, "update_task", map[string]any{"gid": "t1", "completed": true}, &w)
	if w.Sent || !strings.Contains(w.Note, "NOT sent") || !strings.Contains(w.Note, "auth login --pat") {
		t.Fatalf("write while not logged in: %+v", w)
	}
	if msg := toolError(call(t, cs, "sync", nil, nil)); !strings.Contains(msg, "auth login --pat") {
		t.Fatalf("sync while not logged in = %q", msg)
	}

	// 2. Logged in, then the PAT is revoked in Asana.
	mu.Lock()
	stored = "old-pat"
	valid = "new-pat"
	mu.Unlock()
	call(t, cs, "update_task", map[string]any{"gid": "t2", "completed": true}, &w)
	if w.Sent || !strings.Contains(w.Note, "NOT sent") || !strings.Contains(w.Note, "auth login --pat") {
		t.Fatalf("write with a revoked token: %+v", w)
	}
	msg := toolError(call(t, cs, "sync", nil, nil))
	if !strings.Contains(msg, "log in again") || !strings.Contains(msg, "auth login --pat") || strings.Contains(msg, "sync stopped") {
		t.Fatalf("sync with a revoked token = %q", msg)
	}
	if n, _ := st.UnsentCount(ctx); n != 2 {
		t.Fatalf("both changes must stay queued: %d", n)
	}
	ops, _ := st.ListOps(ctx)
	for _, op := range ops {
		if op.State != "pending" {
			t.Fatalf("a 401 must not fail a change: %+v", op)
		}
	}

	// 3. The user logs in again with the new PAT: the next write builds a new syncer.
	mu.Lock()
	stored = "new-pat"
	mu.Unlock()
	before := builds
	call(t, cs, "update_task", map[string]any{"gid": "t3", "completed": true}, &w)
	if !w.Sent {
		t.Fatalf("after a new login the write should be sent: %+v (syncer builds %d -> %d)", w, before, builds)
	}
	if builds != before+1 {
		t.Fatalf("the syncer should be built again once (builds %d -> %d)", before, builds)
	}
	if n, _ := st.UnsentCount(ctx); n != 0 {
		t.Fatalf("the queued changes should go out with it: %d unsent", n)
	}
}
