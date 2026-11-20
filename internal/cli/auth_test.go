package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/auth"
	"github.com/amasok/asagao/internal/auth/tokenstore"
	"github.com/amasok/asagao/internal/config"
)

// fakeAsanaMe answers /users/me for the tokens in users (token -> gid) and 401 otherwise.
func fakeAsanaMe(t *testing.T, users map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/users/me" {
			http.NotFound(w, r)
			return
		}
		gid, ok := users[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"errors":[{"message":"Not Authorized"}]}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"gid": gid, "name": "User " + gid, "email": gid + "@example.com"}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// authTestEnv uses private directories and the file token store (never the real keychain).
func authTestEnv(t *testing.T, configTOML string) tokenstore.Store {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv(auth.EnvToken, "")
	t.Setenv("ASANA_CLIENT_ID", "")
	if err := os.MkdirAll(config.ConfigDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.FilePath(), []byte("[auth]\ntoken_store = \"file\"\n"+configTOML), 0o600); err != nil {
		t.Fatal(err)
	}
	return &tokenstore.File{Path: filepath.Join(config.ConfigDir(), "token.json")}
}

func runAuthCLI(t *testing.T, baseURL, stdin string, args ...string) (string, error) {
	t.Helper()
	cmd := newTestRoot(baseURL)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return out.String(), err
}

func TestAuthLoginPAT(t *testing.T) {
	ts := authTestEnv(t, "")
	srv := fakeAsanaMe(t, map[string]string{"good-pat": "42"})
	ctx := context.Background()

	if _, err := runAuthCLI(t, srv.URL, "bad-pat\n", "auth", "login", "--pat"); err == nil || !strings.Contains(err.Error(), auth.PATConsoleURL) {
		t.Fatalf("a rejected PAT should fail with where to make one: %v", err)
	}
	if _, err := ts.Load(ctx); err != tokenstore.ErrNotFound {
		t.Fatalf("a rejected PAT must not be saved: %v", err)
	}
	if _, err := runAuthCLI(t, srv.URL, "\n", "auth", "login", "--pat"); err == nil {
		t.Fatal("an empty PAT should fail")
	}

	// No client_id configured: plain `auth login` takes a PAT, without asking.
	out, err := runAuthCLI(t, srv.URL, "  good-pat\n", "auth", "login")
	if err != nil {
		t.Fatalf("login: %v\n%s", err, out)
	}
	if !strings.Contains(out, "User 42") || !strings.Contains(out, "Personal Access Token") || strings.Contains(out, "good-pat") {
		t.Fatalf("login output: %s", out)
	}
	c, err := ts.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if c.Method() != tokenstore.MethodPAT || c.PersonalAccessToken != "good-pat" || c.UserGID != "42" || c.RefreshToken != "" {
		t.Fatalf("saved %+v", c)
	}

	out, err = runAuthCLI(t, srv.URL, "", "auth", "status")
	if err != nil || !strings.Contains(out, "User 42") || !strings.Contains(out, "方式: Personal Access Token") {
		t.Fatalf("status: %v\n%s", err, out)
	}

	// The API client uses the stored PAT without a client_id.
	a := &app{wiring: testWiring(), baseURL: srv.URL}
	if a.cfg, err = config.Load(); err != nil {
		t.Fatal(err)
	}
	api, err := a.apiClient(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if me, err := api.Me(ctx); err != nil || me.GID != "42" {
		t.Fatalf("Me with stored PAT = %+v, %v", me, err)
	}

	if out, err = runAuthCLI(t, srv.URL, "", "auth", "logout"); err != nil {
		t.Fatalf("logout: %v\n%s", err, out)
	}
	if _, err := ts.Load(ctx); err != tokenstore.ErrNotFound {
		t.Fatalf("logout should delete the PAT: %v", err)
	}
	if out, _ = runAuthCLI(t, srv.URL, "", "auth", "status"); !strings.Contains(out, "auth login --pat") {
		t.Fatalf("status when logged out should explain the PAT login: %s", out)
	}
}

// A PAT replacing an OAuth login keeps the client secret (for switching back), and a
// rejected stored PAT tells the user how to fix it.
func TestAuthLoginPATAfterOAuth(t *testing.T) {
	ts := authTestEnv(t, "client_id = \"cid\"\n")
	srv := fakeAsanaMe(t, map[string]string{"good-pat": "42"})
	ctx := context.Background()
	if err := ts.Save(ctx, &tokenstore.Credentials{ClientSecret: "cs", RefreshToken: "rt", UserGID: "42"}); err != nil {
		t.Fatal(err)
	}
	if out, err := runAuthCLI(t, srv.URL, "good-pat", "auth", "login", "--pat"); err != nil {
		t.Fatalf("login: %v\n%s", err, out)
	}
	c, err := ts.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if c.Method() != tokenstore.MethodPAT || c.RefreshToken != "" || c.ClientSecret != "cs" {
		t.Fatalf("saved %+v", c)
	}

	c.PersonalAccessToken = "revoked-pat"
	if err := ts.Save(ctx, c); err != nil {
		t.Fatal(err)
	}
	a := &app{wiring: testWiring(), baseURL: srv.URL}
	if a.cfg, err = config.Load(); err != nil {
		t.Fatal(err)
	}
	api, err := a.apiClient(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = api.Me(ctx)
	if help := auth.LoginHelp(err); !strings.Contains(help, "auth login --pat") || !strings.Contains(err.Error(), "auth login --pat") {
		t.Fatalf("401 with a stored PAT: err = %v, help = %q", err, help)
	}
}

// Logging in as another account than the local data's says that syncing stays stopped
// (sync.ErrAccountChanged) and how to get out of it.
func TestAuthLoginWarnsOtherAccount(t *testing.T) {
	authTestEnv(t, "")
	srv := fakeAsanaMe(t, map[string]string{"pat-me": "me", "pat-other": "42"})
	if err := os.MkdirAll(config.DataDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	st := createStoreAt(t, filepath.Join(config.DataDir(), "store.db")) // synced as "me"
	_ = st.Close()

	out, err := runAuthCLI(t, srv.URL, "pat-other", "auth", "login", "--pat")
	if err != nil || !strings.Contains(out, "アカウントと違う") || !strings.Contains(out, "asagao reset") {
		t.Fatalf("login as another account: %v\n%s", err, out)
	}
	out, err = runAuthCLI(t, srv.URL, "pat-me", "auth", "login", "--pat")
	if err != nil || strings.Contains(out, "アカウントと違う") {
		t.Fatalf("login as the same account should not warn: %v\n%s", err, out)
	}
}
