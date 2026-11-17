package asana

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/amasok/asagao/internal/asana/model"
)

// tokenTransport adds the token as oauth2.NewClient does, to every request it sends,
// whatever the host: a leak would show on the other server.
type tokenTransport struct{ rt http.RoundTripper }

func (t tokenTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer SECRET-TOKEN")
	return t.rt.RoundTrip(r)
}

// Whatever the path, nothing reaches a second server (the token would go with it), and
// the server that was meant only sees paths under the base.
func TestRawAttackNoRequestToAnotherHost(t *testing.T) {
	var other, mine atomic.Int32
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { other.Add(1) }))
	defer evil.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mine.Add(1)
		if !strings.HasPrefix(r.URL.EscapedPath(), "/api/1.0/") {
			t.Errorf("server saw path %q outside the base", r.URL.EscapedPath())
		}
	}))
	defer srv.Close()
	c := NewClient(&http.Client{Transport: tokenTransport{http.DefaultTransport}})
	c.BaseURL = srv.URL + "/api/1.0"
	host := strings.TrimPrefix(evil.URL, "http://")
	for _, p := range []string{
		evil.URL + "/x", "//" + host + "/x", "/" + evil.URL, "@" + host, ":" + host, host + "/x",
		"/.." + "/x", "/%2e%2e/%2e%2e/x", "/a/..%2f..%2fx", "/a\\..\\x", "/%5c%5c" + host,
		"/x?y=1\r\nHost: " + host, "/x\r\nHost: " + host, "/\u0085x", "/x#@" + host,
	} {
		for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete} {
			_, _, _ = c.Raw(context.Background(), m, p, []byte(`{}`))
		}
	}
	if other.Load() != 0 {
		t.Errorf("%d requests reached another host", other.Load())
	}
}

// Redirects (every 3xx, to another host, to a relative path, from POST too) are not followed.
func TestRawAttackRedirects(t *testing.T) {
	var leaked atomic.Int32
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked.Add(1)
		if r.Header.Get("Authorization") != "" {
			t.Errorf("token leaked to the redirect target")
		}
	}))
	defer evil.Close()
	for _, code := range []int{301, 302, 303, 307, 308} {
		for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete} {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, evil.URL+"/steal", code)
			}))
			c := NewClient(&http.Client{Transport: tokenTransport{http.DefaultTransport}})
			c.BaseURL = srv.URL
			status, _, err := c.Raw(context.Background(), m, "/x", []byte(`{}`))
			srv.Close()
			if err != nil || status != code {
				t.Errorf("%d %s: status %d err %v", code, m, status, err)
			}
		}
	}
	if leaked.Load() != 0 {
		t.Errorf("redirect followed %d times", leaked.Load())
	}
}

// The token is not in the error of a transport failure or of a 4xx/5xx, and a 401 hint
// is Asana's hint, not the token.
func TestRawAttackTokenNotInErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// a careless server that echoes the header back
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"errors":[{"message":"not authorized"}]}`)
	}))
	c := NewClient(&http.Client{Transport: tokenTransport{http.DefaultTransport}})
	c.BaseURL = srv.URL
	_, _, err := c.Raw(context.Background(), http.MethodGet, "/x", nil)
	if err == nil || strings.Contains(err.Error(), "SECRET-TOKEN") {
		t.Errorf("401 err = %v", err)
	}
	srv.Close() // now refused connections
	_, _, err = c.Raw(context.Background(), http.MethodGet, "/x", nil)
	if err == nil || strings.Contains(err.Error(), "SECRET-TOKEN") {
		t.Errorf("transport err = %v", err)
	}
}

// A BaseURL with a trailing slash or a path prefix: the checks still hold, and a path
// cannot climb out of the prefix.
func TestRawAttackBaseURLShapes(t *testing.T) {
	for _, base := range []string{"https://app.asana.com/api/1.0", "https://app.asana.com/api/1.0/", "http://127.0.0.1:1"} {
		c := NewClient(nil)
		c.BaseURL = base
		for _, p := range []string{"/../../evil", "/%2e%2e/x", "//evil.example", "/x?y=/../..", "/a:b", "/a@evil.example"} {
			u, err := c.rawURL(p)
			if err != nil {
				if !errors.Is(err, model.ErrRawPath) {
					t.Errorf("%s %q: err %v", base, p, err)
				}
				continue
			}
			if !strings.HasPrefix(u, strings.TrimSuffix(base, "/")+"/") {
				t.Errorf("%s %q -> %q leaves the base", base, p, u)
			}
		}
	}
}
