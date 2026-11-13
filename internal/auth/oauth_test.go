package auth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asana-tui/internal/config"
)

func TestAuthCodeURLEncodesScopesWithPercent20(t *testing.T) {
	oc := OAuthConfig(config.AuthConfig{ClientID: "1", Scopes: []string{"tasks:read", "tasks:write"}}, "", LocalRedirectURL(8976))
	u := authCodeURL(oc, "st+ate", nil)
	if strings.Contains(u, "+") {
		t.Fatalf("URL contains '+': %s", u)
	}
	if !strings.Contains(u, "scope=tasks%3Aread%20tasks%3Awrite") {
		t.Fatalf("scope not %%20-separated: %s", u)
	}
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	if got := parsed.Query().Get("state"); got != "st+ate" {
		t.Fatalf("state = %q", got)
	}
}

func callback(t *testing.T, h http.Handler, query string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/callback?"+query, nil))
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("callback %q did not return", query)
	}
	return rec
}

func TestCallbackHandler(t *testing.T) {
	for _, tc := range []struct {
		name, query string
		status      int
		code        string
		errPart     string
	}{
		{"ok", "state=s1&code=abc", http.StatusOK, "abc", ""},
		{"state mismatch", "state=other&code=abc", http.StatusBadRequest, "", "state"},
		{"missing state", "code=abc", http.StatusBadRequest, "", "state"},
		{"error from Asana", "state=s1&error=access_denied&error_description=nope", http.StatusBadRequest, "", "access_denied nope"},
		{"error with wrong state is a state error", "state=x&error=access_denied", http.StatusBadRequest, "", "state"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, results := callbackHandler("s1")
			rec := callback(t, h, tc.query)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.status, rec.Body)
			}
			var r callbackResult
			select {
			case r = <-results:
			default:
				t.Fatal("no result reported")
			}
			if r.code != tc.code {
				t.Fatalf("code = %q, want %q", r.code, tc.code)
			}
			switch {
			case tc.errPart == "" && r.err != nil:
				t.Fatalf("err = %v", r.err)
			case tc.errPart != "" && (r.err == nil || !strings.Contains(r.err.Error(), tc.errPart)):
				t.Fatalf("err = %v, want it to mention %q", r.err, tc.errPart)
			}
		})
	}
}

// A second callback (a reload, or a stray request after the first one) must not block
// and must not replace the first result.
func TestCallbackHandlerSecondCallback(t *testing.T) {
	h, results := callbackHandler("s1")
	if rec := callback(t, h, "state=s1&code=first"); rec.Code != http.StatusOK {
		t.Fatalf("first status = %d", rec.Code)
	}
	if rec := callback(t, h, "state=s1&code=second"); rec.Code != http.StatusConflict {
		t.Fatalf("second status = %d, want %d", rec.Code, http.StatusConflict)
	}
	if r := <-results; r.code != "first" || r.err != nil {
		t.Fatalf("result = %+v, want the first code", r)
	}
	select {
	case r := <-results:
		t.Fatalf("unexpected second result %+v", r)
	default:
	}
	// even after the first result was read, later callbacks are ignored
	if rec := callback(t, h, "state=s1&code=third"); rec.Code != http.StatusConflict {
		t.Fatalf("third status = %d", rec.Code)
	}
	select {
	case r := <-results:
		t.Fatalf("unexpected result %+v", r)
	default:
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// receiveCode serves /callback on the redirect port and returns the code it receives.
func TestReceiveCode(t *testing.T) {
	port := freePort(t)
	oc := OAuthConfig(config.AuthConfig{ClientID: "1"}, "", LocalRedirectURL(port))
	ready := make(chan string, 1)
	opt := LoginOptions{NoBrowser: true, Out: func(format string, args ...any) { ready <- fmt.Sprintf(format, args...) }}
	type res struct {
		code string
		err  error
	}
	done := make(chan res, 1)
	go func() {
		code, err := receiveCode(context.Background(), oc, "st", nil, opt)
		done <- res{code, err}
	}()
	select {
	case msg := <-ready:
		if !strings.Contains(msg, "state=st") || !strings.Contains(msg, url.QueryEscape(LocalRedirectURL(port))) {
			t.Fatalf("printed URL = %q", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("receiveCode did not print the URL")
	}
	resp, err := http.Get(LocalRedirectURL(port) + "?state=st&code=xyz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	select {
	case r := <-done:
		if r.err != nil || r.code != "xyz" {
			t.Fatalf("receiveCode = %q, %v", r.code, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("receiveCode did not return")
	}
}

func TestReceiveCodeCancelled(t *testing.T) {
	oc := OAuthConfig(config.AuthConfig{ClientID: "1"}, "", LocalRedirectURL(freePort(t)))
	ctx, cancel := context.WithCancel(context.Background())
	opt := LoginOptions{NoBrowser: true, Out: func(string, ...any) { cancel() }}
	if _, err := receiveCode(ctx, oc, "st", nil, opt); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestLoginManual(t *testing.T) {
	var tokenReq url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		tokenReq = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"at","refresh_token":"rt","token_type":"bearer","expires_in":3600,"data":{"gid":"9","name":"K","email":"k@example.com"}}`)
	}))
	defer srv.Close()
	oc := OAuthConfig(config.AuthConfig{ClientID: "cid"}, "sec", LocalRedirectURL(1))
	oc.Endpoint.TokenURL = srv.URL

	var printed string
	opt := LoginOptions{
		Manual: true,
		Out:    func(format string, args ...any) { printed = fmt.Sprintf(format, args...) },
		Prompt: func(string) (string, error) { return "  the-code \n", nil },
	}
	tok, err := Login(context.Background(), oc, opt)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(printed, url.QueryEscape(OOBRedirect)) || !strings.Contains(printed, "scope=default") || !strings.Contains(printed, "code_challenge=") {
		t.Fatalf("printed URL = %q", printed)
	}
	if tokenReq.Get("code") != "the-code" || tokenReq.Get("code_verifier") == "" || tokenReq.Get("redirect_uri") != OOBRedirect {
		t.Fatalf("token request = %v", tokenReq)
	}
	if tok.AccessToken != "at" || tok.RefreshToken != "rt" {
		t.Fatalf("token = %+v", tok)
	}
	if gid, name, email := UserFromToken(tok); gid != "9" || name != "K" || email != "k@example.com" {
		t.Fatalf("user = %q %q %q", gid, name, email)
	}
	c := CredentialsFromToken(tok, "sec")
	if c.ClientSecret != "sec" || c.RefreshToken != "rt" || c.AccessToken != "at" || c.UserGID != "9" || c.UserEmail != "k@example.com" {
		t.Fatalf("credentials = %+v", c)
	}
}

func TestLoginManualEmptyCode(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"at","token_type":"bearer"}`)
	}))
	defer srv.Close()
	oc := OAuthConfig(config.AuthConfig{ClientID: "cid"}, "", LocalRedirectURL(1))
	oc.Endpoint.TokenURL = srv.URL
	opt := LoginOptions{
		Manual: true,
		Out:    func(string, ...any) {},
		Prompt: func(string) (string, error) { return "   \n", nil },
	}
	if _, err := Login(context.Background(), oc, opt); err == nil || calls != 0 {
		t.Fatalf("empty code: err %v, token requests %d; want an error and no request", err, calls)
	}
}
