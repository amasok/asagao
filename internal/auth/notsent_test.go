package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/oauth2"
)

type tokenFunc func() (*oauth2.Token, error)

func (f tokenFunc) Token() (*oauth2.Token, error) { return f() }

func TestSourceTokenWrapsNoToken(t *testing.T) {
	retrieve := &oauth2.RetrieveError{Response: &http.Response{StatusCode: 500}, ErrorCode: "server_error"}
	other := errors.New("キーチェーンを読めません")
	for _, tc := range []struct {
		name    string
		err     error
		notSent bool
	}{
		{"logged out", ErrNotLoggedIn, true},
		{"logged out, wrapped", fmt.Errorf("load: %w", ErrNotLoggedIn), true},
		{"login replaced", ErrLoginReplaced, true},
		{"refresh failed", retrieve, true},
		{"refresh failed, wrapped", fmt.Errorf("oauth2: %w", retrieve), true},
		{"another error", other, false},
		{"the store changed under us", errLoginChanged, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := &Source{TokenSource: tokenFunc(func() (*oauth2.Token, error) { return nil, tc.err })}
			tok, err := src.Token()
			if tok != nil || err == nil {
				t.Fatalf("Token = %v, %v", tok, err)
			}
			var te *TokenError
			if errors.As(err, &te) != tc.notSent {
				t.Fatalf("err = %#v, a *TokenError: %v, want %v", err, !tc.notSent, tc.notSent)
			}
			if !tc.notSent {
				if err != tc.err {
					t.Fatalf("other errors are returned as they are: %v", err)
				}
				return
			}
			if !te.RequestNotSent() {
				t.Fatal("RequestNotSent = false")
			}
			// Shown and matched as the error it holds.
			if err.Error() != tc.err.Error() || !errors.Is(err, tc.err) {
				t.Fatalf("err = %q, want it to be %q", err, tc.err)
			}
			if LoginHelp(err) != LoginHelp(tc.err) {
				t.Fatalf("LoginHelp = %q, want %q", LoginHelp(err), LoginHelp(tc.err))
			}
			var re *oauth2.RetrieveError
			if errors.As(tc.err, &re) && (!errors.As(err, &re) || re != retrieve) {
				t.Fatalf("the RetrieveError is not found in %v", err)
			}
		})
	}
	want := &oauth2.Token{AccessToken: "a"}
	src := &Source{TokenSource: tokenFunc(func() (*oauth2.Token, error) { return want, nil })}
	if tok, err := src.Token(); tok != want || err != nil {
		t.Fatalf("Token = %v, %v", tok, err)
	}
}

// Through an HTTP client the error comes back inside a *url.Error, and is still known by
// its RequestNotSent method (as the sync engine looks for it); nothing reaches the server.
func TestSourceTokenThroughHTTPClient(t *testing.T) {
	reached := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	defer srv.Close()
	src := &Source{TokenSource: tokenFunc(func() (*oauth2.Token, error) { return nil, ErrNotLoggedIn })}
	_, err := oauth2.NewClient(context.Background(), src).Get(srv.URL)
	var notSent interface{ RequestNotSent() bool }
	if !errors.As(err, &notSent) || !notSent.RequestNotSent() || !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("err = %v", err)
	}
	if reached {
		t.Fatal("the request reached the server")
	}
}
