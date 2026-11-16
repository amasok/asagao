package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/amasok/asana-tui/internal/auth/tokenstore"
)

func fastLoginRetry(t *testing.T) {
	old := loginRetryDelay
	loginRetryDelay = 10 * time.Millisecond
	t.Cleanup(func() { loginRetryDelay = old })
}

// A 5xx body that echoes the token and carries terminal escapes must show up neither in the
// error nor on stderr/stdout.
func TestAuthLoginPAT5xxBodyDoesNotLeak(t *testing.T) {
	ts := authTestEnv(t, "")
	fastLoginRetry(t)
	_ = ts
	const tok = "1/secret-token-abc"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "\x1b[31mred")
		w.WriteHeader(502)
		w.Write([]byte(`{"errors":[{"message":"bad token ` + tok + ` \u001b]0;pwn\u0007 \u001b[2J"}]}`))
	}))
	defer srv.Close()
	out, err := runAuthCLI(t, srv.URL, tok+"\n", "auth", "login", "--pat")
	if err == nil {
		t.Fatal("want error")
	}
	for _, s := range []string{out, err.Error()} {
		if strings.Contains(s, tok) || strings.ContainsAny(s, "\x1b\x07") {
			t.Errorf("leak or control char in %q", s)
		}
	}
}

// 401 and 403 are final: one request, no "retrying" announcement, earlier login kept.
func TestAuthLoginPATAuthErrorsDoNotRetry(t *testing.T) {
	ts := authTestEnv(t, "")
	fastLoginRetry(t)
	ctx := context.Background()
	earlier := &tokenstore.Credentials{PersonalAccessToken: "old-pat", UserGID: "42"}
	if err := ts.Save(ctx, earlier); err != nil {
		t.Fatal(err)
	}
	for _, st := range []int{401, 403, 404, 400} {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(st) }))
		out, err := runAuthCLI(t, srv.URL, "new-pat\n", "auth", "login", "--pat")
		srv.Close()
		if err == nil || calls.Load() != 1 || strings.Contains(out, "もう一度確かめます") || strings.Contains(err.Error(), "一時的") {
			t.Errorf("status %d: err=%v calls=%d out=%q", st, err, calls.Load(), out)
		}
		if c, err := ts.Load(ctx); err != nil || *c != *earlier {
			t.Fatalf("status %d: earlier login lost: %+v %v", st, c, err)
		}
	}
}

// Offline is not retried and is not reported as Asana's "temporary" problem.
func TestAuthLoginPATOfflineFailsAtOnce(t *testing.T) {
	authTestEnv(t, "")
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	start := time.Now()
	out, err := runAuthCLI(t, srv.URL, "new-pat\n", "auth", "login", "--pat")
	if err == nil || strings.Contains(out, "もう一度確かめます") || time.Since(start) > 3*time.Second {
		t.Errorf("err=%v out=%q took %v", err, out, time.Since(start))
	}
}

// Even a huge or odd Retry-After never makes login wait more than the cap, and the
// announcement never shows a negative time.
func TestAuthLoginPATOddRetryAfter(t *testing.T) {
	authTestEnv(t, "")
	fastLoginRetry(t)
	for _, ra := range []string{"-5", "0", "99999999999999", "abc"} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", ra)
			w.WriteHeader(429)
		}))
		start := time.Now()
		out, err := runAuthCLI(t, srv.URL, "new-pat\n", "auth", "login", "--pat")
		srv.Close()
		if err == nil || time.Since(start) > 3*time.Second {
			t.Errorf("Retry-After %q: err=%v took %v out=%q", ra, err, time.Since(start), out)
		}
		if strings.Contains(out, "-9") || strings.Contains(out, "-5s") {
			t.Errorf("Retry-After %q: negative wait announced: %q", ra, out)
		}
	}
}
