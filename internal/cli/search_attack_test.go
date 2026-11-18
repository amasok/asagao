package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/amasok/asagao/internal/auth"
)

// attackEnv serves fakeFindAsana, except that the search API and typeahead answer failStatus
// with a message holding terminal control characters (when failStatus != 0). It counts the
// requests that reach the search API and typeahead.
func attackEnv(t *testing.T, failStatus int) (func(args ...string) (string, string, error), *atomic.Int32) {
	t.Helper()
	writeCLIEnv(t)
	t.Setenv(auth.EnvToken, "test-pat")
	inner := &fakeFindAsana{fakePushAsana: &fakePushAsana{assignee: "me", section: "s1", sname: "家具"}}
	var asked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/tasks/search") || strings.HasSuffix(r.URL.Path, "/typeahead") {
			asked.Add(1)
			if failStatus != 0 {
				w.WriteHeader(failStatus)
				_, _ = w.Write([]byte(`{"errors":[{"message":"bad \u001b[2J\u001b]0;pwned\u0007 thing"}]}`))
				return
			}
		}
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return func(args ...string) (string, string, error) {
		cmd := newRoot(&app{baseURL: srv.URL})
		var out, errOut bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&errOut)
		cmd.SetArgs(args)
		err := cmd.ExecuteContext(context.Background())
		return out.String(), errOut.String(), err
	}, &asked
}

func hasControl(s string) bool {
	for _, r := range s {
		if r == 0x1b || r == 0x07 || (r < 0x20 && r != '\n' && r != '\t') {
			return true
		}
	}
	return false
}

// An Asana error message with control characters never reaches the terminal, and --json
// stays valid, on a failed remote search (#36).
func TestSearchRemoteErrorIsTermsafe(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusBadRequest, http.StatusUnauthorized} {
		run, _ := attackEnv(t, status)
		out, errOut, err := run("search", "ソファ", "--remote")
		if err != nil || !strings.Contains(out, "ソファを買う") || !strings.Contains(errOut, "Asana では探せませんでした") {
			t.Fatalf("%d: out %q err %q %v", status, out, errOut, err)
		}
		if hasControl(errOut) || hasControl(out) {
			t.Fatalf("%d: control characters: %q %q", status, errOut, out)
		}
		out, _, err = run("--json", "search", "ソファ", "--remote")
		var tasks []map[string]any
		if err != nil || json.Unmarshal([]byte(out), &tasks) != nil || len(tasks) == 0 {
			t.Fatalf("%d: json %q %v", status, out, err)
		}
	}
}

// --dry-run sends nothing, not even a lookup: names are read from the cache only.
func TestDryRunAsksAsanaNothing(t *testing.T) {
	run, asked := attackEnv(t, 0)
	_, _, _ = run("task", "assign", "t1", "hanako", "--dry-run")
	_, _, _ = run("task", "create", "--quick", "請求書を送る #経理 @hanako", "--dry-run")
	if n := asked.Load(); n != 0 {
		t.Fatalf("--dry-run asked Asana %d times", n)
	}
}

// A 429 / 5xx on a name lookup does not break the write: assign still fails clearly
// (the user is unknown), not with a hang or a panic; and the message is clean.
func TestAssignWhenLookupFails(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusBadRequest} {
		run, asked := attackEnv(t, status)
		_, errOut, err := run("task", "assign", "t1", "hanako")
		if err == nil {
			t.Fatalf("%d: assigned an unknown user", status)
		}
		if hasControl(err.Error()) || hasControl(errOut) {
			t.Fatalf("%d: control characters: %q %q", status, err, errOut)
		}
		if asked.Load() != 1 {
			t.Fatalf("%d: asked %d times", status, asked.Load())
		}
	}
}

// create --quick with two unknown names asks once each, and one failing lookup does not stop the other.
func TestQuickAddLookupOncePerToken(t *testing.T) {
	run, asked := attackEnv(t, 0)
	if _, _, err := run("task", "create", "--quick", "請求書を送る #経理 @hanako"); err != nil {
		t.Fatal(err)
	}
	if n := asked.Load(); n != 2 {
		t.Fatalf("asked %d times, want 2", n)
	}
}
