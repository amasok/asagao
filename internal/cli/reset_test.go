package cli

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asana-tui/internal/config"
	"github.com/amasok/asana-tui/internal/sync"
)

// run executes the CLI with a private data and config directory.
func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newRoot(&app{baseURL: unreachableAsana(t)})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return out.String(), err
}

// unreachableAsana is the API URL of commands run by tests that should not reach Asana:
// a closed local server, so a command that finds a token (another test's keychain mock,
// say) fails as offline instead of talking to the real Asana.
func unreachableAsana(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	return srv.URL
}

// runCLISplit is runCLI with the command's output (where --json goes) and its error output
// kept apart.
func runCLISplit(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := newRoot(&app{baseURL: unreachableAsana(t)})
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err = cmd.ExecuteContext(context.Background())
	return out.String(), errOut.String(), err
}

func TestResetCommand(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ctx := context.Background()
	now := time.Date(2026, 11, 14, 9, 0, 0, 0, time.Local)
	if err := os.MkdirAll(config.DataDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	st := createStoreAt(t, filepath.Join(config.DataDir(), "store.db"))
	if _, err := createTask(ctx, st, createOpts{name: "牛乳を買う", notes: "低脂肪"}, now); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()

	out, err := runCLI(t, "reset")
	if err == nil || !strings.Contains(err.Error(), "1 件") || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("reset without --force: %v\n%s", err, out)
	}
	if files, _ := os.ReadDir(backupDir()); len(files) != 0 {
		t.Fatalf("no backup without --force: %v", files)
	}

	out, err = runCLI(t, "reset", "--force")
	if err != nil {
		t.Fatalf("reset --force: %v", err)
	}
	files, _ := os.ReadDir(backupDir())
	if len(files) != 1 || !strings.Contains(out, filepath.Join(backupDir(), files[0].Name())) {
		t.Fatalf("the backup path should be shown: %v\n%s", files, out)
	}
	data, _ := os.ReadFile(filepath.Join(backupDir(), files[0].Name()))
	if !strings.Contains(string(data), "牛乳を買う") || !strings.Contains(string(data), "低脂肪") {
		t.Fatalf("backup should keep the unsent change:\n%s", data)
	}
	if !strings.Contains(out, "outbox discarded") {
		t.Fatalf("should point to the dropped-changes list:\n%s", out)
	}
	out, err = runCLI(t, "outbox", "discarded")
	if err != nil || !strings.Contains(out, "牛乳を買う") || !strings.Contains(out, "（初期化で破棄）") {
		t.Fatalf("outbox discarded after reset: %v\n%s", err, out)
	}
	st = createStoreAt(t, filepath.Join(config.DataDir(), "store.db"))
	if n, _ := st.UnsentCount(ctx); n != 0 {
		t.Fatalf("outbox should be empty: %d", n)
	}
}

func TestAccountChangedMessage(t *testing.T) {
	ctx := context.Background()
	st := createStore(t)
	_, _ = createTask(ctx, st, createOpts{name: "a"}, time.Now())
	_, _ = createTask(ctx, st, createOpts{name: "b"}, time.Now())
	err := accountChanged(ctx, st)
	if !errors.Is(err, sync.ErrAccountChanged) || !strings.Contains(err.Error(), "2 件") || !strings.Contains(err.Error(), "asana-tui reset") {
		t.Fatalf("err = %v", err)
	}
}
