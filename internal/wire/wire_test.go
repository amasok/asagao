package wire

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/auth"
	"github.com/amasok/asagao/internal/config"
	"github.com/amasok/asagao/internal/store"
	"github.com/amasok/asagao/internal/sync"
)

type tokenFunc func() (*oauth2.Token, error)

func (f tokenFunc) Token() (*oauth2.Token, error) { return f() }

// The client built for a login sends through the auth.Source, so a request without a
// token (logged out meanwhile, the login replaced, the refresh failed) is known as not
// sent (d2), and it does not reach Asana.
func TestLoginClientMarksRequestsWithoutToken(t *testing.T) {
	var reached atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Store(true)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	for _, cause := range []error{auth.ErrNotLoggedIn, auth.ErrLoginReplaced, &oauth2.RetrieveError{ErrorCode: "server_error"}} {
		src := &auth.Source{TokenSource: tokenFunc(func() (*oauth2.Token, error) { return nil, cause })}
		c := loginClient(context.Background(), src, srv.URL)
		c.MaxRetries = 0
		_, err := c.Me(context.Background())
		var notSent interface{ RequestNotSent() bool }
		if !errors.As(err, &notSent) || !notSent.RequestNotSent() {
			t.Errorf("%v: err = %v, want one that says the request was not sent", cause, err)
		}
	}
	if reached.Load() {
		t.Error("a request without a token reached the server")
	}
}

func TestLoginClientSendsTheToken(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"data":{"gid":"me","name":"Me"}}`))
	}))
	defer srv.Close()
	src := &auth.Source{TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "tok", TokenType: "Bearer"})}
	me, err := loginClient(context.Background(), src, srv.URL).Me(context.Background())
	if err != nil || me.GID != "me" || got != "Bearer tok" {
		t.Fatalf("Me = %+v, %v; Authorization %q", me, err, got)
	}
}

func TestAPIClientWithoutLogin(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv(auth.EnvToken, "")
	cfg := config.Default()
	cfg.Auth.TokenStore = config.TokenStoreFile
	api, err := APIClient(context.Background(), cfg, "http://127.0.0.1:1")
	if !errors.Is(err, auth.ErrNotLoggedIn) || api != nil {
		t.Fatalf("APIClient = %v, %v; want a nil client and ErrNotLoggedIn", api, err)
	}
}

func TestSyncer(t *testing.T) {
	cfg := config.Default()
	cfg.Sync = config.SyncConfig{CompletedDays: 30, AllProjects: true}
	s := Syncer(nil, nil, cfg)
	if s.API != nil || s.RichText == nil || s.Cfg != (sync.Config{CompletedDays: 30, AllProjects: true}) {
		t.Fatalf("Syncer = %+v", s)
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg.Auth.TokenStore = config.TokenStoreFile // never the developer's keychain
	t.Setenv(auth.EnvToken, "pat")
	api, err := APIClient(context.Background(), cfg, "http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	if s := Syncer(api, nil, cfg); s.API != api {
		t.Fatalf("Syncer.API = %v", s.API)
	}
}

// A busy Asana is retried once, and onRetry is told; the token is sent as a bearer.
func TestCheckPAT(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer pat-1" || !strings.HasSuffix(r.URL.Path, "/users/me") {
			t.Errorf("request %s %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"gid":"u1","name":"山田","email":"y@example.com"}}`))
	}))
	defer srv.Close()
	var retried []int
	me, err := CheckPAT(context.Background(), "pat-1", srv.URL, time.Millisecond, func(status int, _ time.Duration) { retried = append(retried, status) })
	if err != nil || me.GID != "u1" || me.Email != "y@example.com" {
		t.Fatalf("CheckPAT = %+v, %v", me, err)
	}
	if calls.Load() != 2 || len(retried) != 1 || retried[0] != http.StatusServiceUnavailable {
		t.Fatalf("calls %d, retried %v", calls.Load(), retried)
	}
}

// The store OpenStore gives records a dropped change with the display's description of it
// (the store does not import ui/format itself, Issue #82).
func TestOpenStoreDescribesDroppedChanges(t *testing.T) {
	ctx := context.Background()
	st, err := OpenStore(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	if err := st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "牛乳を買う"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddCommentLocal(ctx, "t1", "了解", store.UserRef{GID: "me"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Reset(ctx, true, now); err != nil {
		t.Fatal(err)
	}
	ds, err := st.PendingDiscards(ctx)
	if err != nil || len(ds) != 1 || ds[0].Summary != "コメント「了解」" || !ds[0].IsReset() {
		t.Fatalf("discards = %+v, %v", ds, err)
	}
}
