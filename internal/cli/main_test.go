package cli

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/amasok/asana-tui/internal/auth"
	"github.com/amasok/asana-tui/internal/auth/tokenstore"
)

// The tests of this package run commands as a user would, so before any of them:
//
//   - the keychain is go-keyring's in-memory mock: the default token store ("keychain")
//     must never read or write the developer's real login (an OAuth token found there
//     would also be refreshed against the real Asana);
//   - the token, client id and client secret from the environment are cleared, so one
//     exported in the developer's shell is not used;
//   - requests may only go to this machine (httptest servers). One that goes elsewhere is
//     blocked, and the run fails.
//
// Tests that need a token set it themselves (t.Setenv, or a file token store).

// keyringMocked is set once TestMain has put the mock in place.
var keyringMocked bool

// realNetworkAttempts counts requests to a host that is not this machine.
var realNetworkAttempts atomic.Int32

type loopbackOnly struct{ next http.RoundTripper }

func (l loopbackOnly) RoundTrip(r *http.Request) (*http.Response, error) {
	host, _, err := net.SplitHostPort(r.URL.Host)
	if err != nil {
		host = r.URL.Host
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		realNetworkAttempts.Add(1)
		fmt.Fprintf(os.Stderr, "TEST REACHED THE NETWORK: %s %s\n", r.Method, r.URL)
		return nil, fmt.Errorf("blocked by test guard: %s", r.URL.Host)
	}
	return l.next.RoundTrip(r)
}

func TestMain(m *testing.M) {
	keyring.MockInit()
	keyringMocked = true
	for _, k := range []string{auth.EnvToken, "ASANA_CLIENT_ID", EnvClientSecret} {
		os.Unsetenv(k)
	}
	http.DefaultTransport = loopbackOnly{http.DefaultTransport}
	code := m.Run()
	if n := realNetworkAttempts.Load(); n > 0 && code == 0 {
		fmt.Fprintf(os.Stderr, "FAIL: %d request(s) left for a real host\n", n)
		code = 1
	}
	os.Exit(code)
}

// A command using the default token store (no token_store in the config) gets the token
// from the mock keychain, run on its own as well as with the others: nothing reads the
// real one.
func TestDefaultTokenStoreIsTheMock(t *testing.T) {
	if !keyringMocked {
		t.Fatal("TestMain did not mock the keychain; refusing to touch it")
	}
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	kc := &tokenstore.Keychain{Service: "asana-tui", User: "default"}
	ctx := context.Background()
	if _, err := kc.Load(ctx); err != tokenstore.ErrNotFound {
		t.Fatalf("the mock keychain should start empty here: %v", err)
	}
	if err := kc.Save(ctx, &tokenstore.Credentials{PersonalAccessToken: "mock-pat", UserGID: "42", UserName: "Mock"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = kc.Delete(ctx) })
	if v, err := keyring.Get("asana-tui", "default"); err != nil || !strings.Contains(v, "mock-pat") {
		t.Fatalf("the saved token should be in the mock: %q, %v", v, err)
	}
	out, err := runCLI(t, "auth", "status")
	if err != nil || !strings.Contains(out, "Mock") {
		t.Fatalf("auth status should see the mock's login: %v\n%s", err, out)
	}
	for _, k := range []string{auth.EnvToken, "ASANA_CLIENT_ID", EnvClientSecret} {
		if v := os.Getenv(k); v != "" {
			t.Errorf("%s is set in the tests", k)
		}
	}
}
