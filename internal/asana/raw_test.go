package asana

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/amasok/asagao/internal/asana/model"
)

// Raw sends the method, path, query and body as they are, and returns Asana's answer.
func TestRawSendsAsIs(t *testing.T) {
	var got string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = r.Method + " " + r.URL.RequestURI() + " " + r.Header.Get("Content-Type") + " " + string(b)
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"data":{"gid":"9"}}`)
	})
	status, body, err := c.Raw(context.Background(), http.MethodPost, "/tasks?opt_fields=name", []byte(`{"data":{"name":"x"}}`))
	if err != nil || status != http.StatusCreated || string(body) != `{"data":{"gid":"9"}}` {
		t.Fatalf("Raw = %d %q %v", status, body, err)
	}
	if want := `POST /tasks?opt_fields=name application/json {"data":{"name":"x"}}`; got != want {
		t.Errorf("request = %q, want %q", got, want)
	}
}

// A path that could leave the API is refused before anything is sent.
func TestRawRefusesPathsOutsideTheAPI(t *testing.T) {
	var hits atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { hits.Add(1) })
	c.BaseURL += "/api/1.0"
	for _, p := range []string{
		"", "tasks", "//evil.example/x", "/../x", "/tasks/../../x", "/%2e%2e/x", "/%2E%2E/x",
		"/tasks/%2F..%2Fx", "/a\\b", "/a%5c..", "/a#b", "/a b", "/a\x1b[31m", "/a%00",
		"@evil.example/x", "/.", "/./x", "/a%zz",
	} {
		if _, _, err := c.Raw(context.Background(), http.MethodGet, p, nil); !errors.Is(err, model.ErrRawPath) {
			t.Errorf("Raw(%q) err = %v, want ErrRawPath", p, err)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("%d requests sent for refused paths", n)
	}
}

// The URL Raw builds stays under BaseURL's path.
func TestRawURLStaysUnderBaseURL(t *testing.T) {
	c := NewClient(nil)
	u, err := c.rawURL("/tasks/1?opt_fields=name,notes")
	if err != nil || u != "https://app.asana.com/api/1.0/tasks/1?opt_fields=name,notes" {
		t.Fatalf("rawURL = %q, %v", u, err)
	}
	c.BaseURL = "https://app.asana.com/api/1.0/"
	if u, err := c.rawURL("/users/me"); err != nil || u != "https://app.asana.com/api/1.0/users/me" {
		t.Errorf("with a trailing slash: %q, %v", u, err)
	}
}

// A redirect is not followed: the token would go with it to wherever it points.
func TestRawDoesNotFollowRedirects(t *testing.T) {
	var leaked atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1) }))
	defer other.Close()
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/steal", http.StatusFound)
	})
	status, _, err := c.Raw(context.Background(), http.MethodGet, "/users/me", nil)
	if err != nil || status != http.StatusFound {
		t.Fatalf("Raw = %d, %v; want 302", status, err)
	}
	if leaked.Load() != 0 {
		t.Error("the redirect was followed")
	}
	// The client's own redirect policy is left as it was.
	if c.HTTP.CheckRedirect != nil {
		t.Error("Raw changed the shared http.Client")
	}
}

// 429 is retried for every method, 5xx for all but POST (which may have been applied).
func TestRawRetries(t *testing.T) {
	for _, tc := range []struct {
		method string
		status int
		want   int32
	}{
		{http.MethodPost, http.StatusTooManyRequests, 2},
		{http.MethodGet, http.StatusBadGateway, 2},
		{http.MethodPut, http.StatusServiceUnavailable, 2},
		{http.MethodDelete, http.StatusInternalServerError, 2},
		{http.MethodPost, http.StatusInternalServerError, 1},
	} {
		var n atomic.Int32
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if n.Add(1) == 1 {
				w.WriteHeader(tc.status)
				return
			}
			fmt.Fprint(w, `{"data":{}}`)
		})
		_, _, _ = c.Raw(context.Background(), tc.method, "/x", nil)
		if got := n.Load(); got != tc.want {
			t.Errorf("%s after %d: %d requests, want %d", tc.method, tc.status, got, tc.want)
		}
	}
}

// A >= 400 answer is an error (with the login hint for 401), and its body is returned.
func TestRawErrorKeepsBody(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"errors":[{"message":"Not Authorized"}]}`)
	})
	c.AuthHint = "asagao auth login で入り直してください"
	status, body, err := c.Raw(context.Background(), http.MethodGet, "/users/me", nil)
	if status != http.StatusUnauthorized || !strings.Contains(string(body), "Not Authorized") {
		t.Errorf("Raw = %d %q", status, body)
	}
	if !IsUnauthorized(err) || !strings.Contains(err.Error(), c.AuthHint) {
		t.Errorf("err = %v", err)
	}
}
