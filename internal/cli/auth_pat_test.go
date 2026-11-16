package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/amasok/asana-tui/internal/auth"
	"github.com/amasok/asana-tui/internal/auth/tokenstore"
)

// `auth migrate` moves a PAT login between stores without losing it or turning it into
// an OAuth login.
func TestAuthMigrateKeepsPAT(t *testing.T) {
	keyring.MockInit()
	file := authTestEnv(t, "")
	ctx := context.Background()
	in := &tokenstore.Credentials{PersonalAccessToken: "pat-1", ClientSecret: "cs", UserGID: "42", UserName: "U", UserEmail: "u@example.com"}
	if err := file.Save(ctx, in); err != nil {
		t.Fatal(err)
	}
	if out, err := runAuthCLI(t, "", "", "auth", "migrate", "--to", "keychain"); err != nil {
		t.Fatalf("migrate: %v\n%s", err, out)
	}
	kc := &tokenstore.Keychain{Service: "asana-tui", User: "default"}
	got, err := kc.Load(ctx)
	if err != nil || *got != *in || got.Method() != tokenstore.MethodPAT {
		t.Fatalf("keychain after migrate = %+v, %v; want %+v", got, err, in)
	}
	if _, err := file.Load(ctx); err != tokenstore.ErrNotFound {
		t.Fatalf("the file should be removed after migrating: %v", err)
	}
}

// A failed check of the token (Asana down, offline) saves nothing and keeps the earlier
// login; a token with spaces inside is refused before anything is sent.
func TestAuthLoginPATFailuresKeepEarlierLogin(t *testing.T) {
	ts := authTestEnv(t, "")
	ctx := context.Background()
	earlier := &tokenstore.Credentials{PersonalAccessToken: "old-pat", UserGID: "42"}
	if err := ts.Save(ctx, earlier); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusForbidden) // not 5xx: GETs are retried with backoff
	}))
	defer down.Close()
	offline := httptest.NewServer(http.NotFoundHandler())
	offline.Close()

	for _, tc := range []struct{ name, url, stdin string }{
		{"rejected (403)", down.URL, "new-pat\n"},
		{"offline", offline.URL, "new-pat\n"},
		{"space inside", down.URL, "new pat\n"},
		{"two lines", down.URL, "new-pat\nsecond\n"},
		{"only spaces", down.URL, "   \n\t\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := calls.Load()
			out, err := runAuthCLI(t, tc.url, tc.stdin, "auth", "login", "--pat")
			if err == nil {
				t.Fatalf("login should fail\n%s", out)
			}
			if strings.Contains(out+err.Error(), "new-pat") {
				t.Fatalf("the token must not be printed: %v\n%s", err, out)
			}
			if c, err := ts.Load(ctx); err != nil || *c != *earlier {
				t.Fatalf("the earlier login should be kept: %+v, %v", c, err)
			}
			if tc.url == down.URL && tc.name != "rejected (403)" && calls.Load() != before {
				t.Fatal("a malformed token should be refused without asking Asana")
			}
		})
	}
}

// ASANA_TOKEN still wins: login and logout say so, and status reports the variable.
func TestAuthPATWithEnvToken(t *testing.T) {
	ts := authTestEnv(t, "")
	srv := fakeAsanaMe(t, map[string]string{"good-pat": "42"})
	t.Setenv(auth.EnvToken, "env-pat")
	out, err := runAuthCLI(t, srv.URL, "good-pat", "auth", "login", "--pat")
	if err != nil || !strings.Contains(out, auth.EnvToken) {
		t.Fatalf("login with ASANA_TOKEN set should warn: %v\n%s", err, out)
	}
	if c, err := ts.Load(context.Background()); err != nil || c.PersonalAccessToken != "good-pat" {
		t.Fatalf("the PAT should still be saved: %+v %v", c, err)
	}
	out, err = runAuthCLI(t, srv.URL, "", "auth", "status")
	if err != nil || !strings.Contains(out, auth.EnvToken) || strings.Contains(out, "env-pat") || strings.Contains(out, "good-pat") {
		t.Fatalf("status should name %s and print no token: %v\n%s", auth.EnvToken, err, out)
	}
	out, err = runAuthCLI(t, srv.URL, "", "auth", "logout")
	if err != nil || !strings.Contains(out, auth.EnvToken) {
		t.Fatalf("logout with ASANA_TOKEN set should warn: %v\n%s", err, out)
	}
}

// status names the stored method and never prints the token.
func TestAuthStatusMethod(t *testing.T) {
	ts := authTestEnv(t, "client_id = \"cid\"\n")
	ctx := context.Background()
	status := func() string {
		t.Helper()
		out, err := runAuthCLI(t, "", "", "auth", "status")
		if err != nil {
			t.Fatalf("status: %v\n%s", err, out)
		}
		return out
	}
	if out := status(); !strings.Contains(out, "ログインしていません") {
		t.Fatalf("logged out: %s", out)
	}
	_ = ts.Save(ctx, &tokenstore.Credentials{RefreshToken: "secret-rt", UserName: "U"})
	if out := status(); !strings.Contains(out, "方式: OAuth") || strings.Contains(out, "secret-rt") {
		t.Fatalf("oauth: %s", out)
	}
	_ = ts.Save(ctx, &tokenstore.Credentials{PersonalAccessToken: "secret-pat", UserName: "U"})
	if out := status(); !strings.Contains(out, "方式: Personal Access Token") || strings.Contains(out, "secret-pat") {
		t.Fatalf("pat: %s", out)
	}
}

// When Asana answers the token check with 5xx (or 429), login retries once with a short,
// announced wait and then fails saying it is Asana's side, instead of the sync backoff of
// about 30 s (#24). The earlier login is kept.
func TestAuthLoginPATServerErrorFailsFast(t *testing.T) {
	ts := authTestEnv(t, "")
	ctx := context.Background()
	earlier := &tokenstore.Credentials{PersonalAccessToken: "old-pat", UserGID: "42"}
	if err := ts.Save(ctx, earlier); err != nil {
		t.Fatal(err)
	}
	old := loginRetryDelay
	loginRetryDelay = 10 * time.Millisecond
	t.Cleanup(func() { loginRetryDelay = old })

	for _, tc := range []struct {
		status     int
		retryAfter string
	}{
		{http.StatusServiceUnavailable, ""},
		{http.StatusInternalServerError, ""},
		{http.StatusTooManyRequests, "60"}, // Retry-After is capped too
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if tc.retryAfter != "" {
					w.Header().Set("Retry-After", tc.retryAfter)
				}
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()
			start := time.Now()
			out, err := runAuthCLI(t, srv.URL, "new-pat\n", "auth", "login", "--pat")
			if err == nil {
				t.Fatalf("login should fail\n%s", out)
			}
			if d := time.Since(start); d > 5*time.Second {
				t.Errorf("login took %v", d)
			}
			if n := calls.Load(); n != 2 {
				t.Errorf("asked Asana %d times, want 2 (one retry)", n)
			}
			if !strings.Contains(out, "もう一度確かめます") || !strings.Contains(out, fmt.Sprint(tc.status)) {
				t.Errorf("the wait should be announced:\n%s", out)
			}
			busy := "一時的"
			if tc.status == http.StatusTooManyRequests {
				busy = "混み合っています" // a rate limit, not a failure
			}
			if !strings.Contains(out, busy) {
				t.Errorf("the retry notice should say %q:\n%s", busy, out)
			}
			if msg := err.Error(); !strings.Contains(msg, busy) || !strings.Contains(msg, "保存していません") || !strings.Contains(msg, fmt.Sprint(tc.status)) || strings.Contains(msg, "new-pat") {
				t.Errorf("error = %q", msg)
			}
			if c, err := ts.Load(ctx); err != nil || *c != *earlier {
				t.Fatalf("the earlier login should be kept: %+v, %v", c, err)
			}
		})
	}
}

// A single 5xx is retried and the login goes through.
func TestAuthLoginPATRetriesOnce(t *testing.T) {
	ts := authTestEnv(t, "")
	old := loginRetryDelay
	loginRetryDelay = 10 * time.Millisecond
	t.Cleanup(func() { loginRetryDelay = old })
	var calls atomic.Int32
	me := fakeAsanaMe(t, map[string]string{"good-pat": "42"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		me.Config.Handler.ServeHTTP(w, r)
	}))
	defer srv.Close()
	out, err := runAuthCLI(t, srv.URL, "good-pat\n", "auth", "login", "--pat")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if c, err := ts.Load(context.Background()); err != nil || c.PersonalAccessToken != "good-pat" || c.UserGID != "42" {
		t.Fatalf("saved %+v, %v", c, err)
	}
}

// An Asana that takes the connection but never answers does not keep login waiting: the
// check gives up after loginTimeout, saves nothing and says so.
func TestAuthLoginPATTimesOut(t *testing.T) {
	ts := authTestEnv(t, "")
	old := loginTimeout
	loginTimeout = 200 * time.Millisecond
	t.Cleanup(func() { loginTimeout = old })
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)
	start := time.Now()
	out, err := runAuthCLI(t, srv.URL, "new-pat\n", "auth", "login", "--pat")
	if err == nil || !strings.Contains(err.Error(), "返事がありませんでした") || !strings.Contains(err.Error(), "保存していません") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("took %v", d)
	}
	if _, err := ts.Load(context.Background()); err != tokenstore.ErrNotFound {
		t.Errorf("nothing should be saved: %v", err)
	}
}
