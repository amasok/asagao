package cli

import (
	"bytes"
	"context"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/amasok/asagao/internal/auth"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/store"
)

func customRun(baseURL string, args ...string) (string, string, error) {
	cmd := newRoot(&app{baseURL: baseURL})
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return out.String(), errOut.String(), err
}

// A task named by gid and by custom ID in one commit is handled once (#36).
func TestGitScanGIDAndCustomIDOfOneTask(t *testing.T) {
	path := gitCLIEnv(t)
	t.Setenv(auth.EnvToken, "test-pat")
	fake := &customIDAsana{fakeGitAsana: &fakeGitAsana{completed: map[string]bool{}, stories: map[string][]map[string]any{}}}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	repo := gitRepo(t)
	gitCommit(t, repo, "fixes #"+gitA+" and asana#ENG-1")
	out, errOut, err := customRun(srv.URL, "git", "scan", "-C", repo)
	if err != nil {
		t.Fatalf("%v\n%s%s", err, out, errOut)
	}
	if got := strings.Join(fake.took(), " "); got != "PUT /tasks/"+gitA+" POST /tasks/"+gitA+"/stories" {
		t.Fatalf("requests %q\n%s", got, out)
	}
	if ops := cliOps(t, path); len(ops) != 0 {
		t.Fatalf("queued %v", opKinds(ops))
	}
}

// A commit scanned twice is not handled twice, also by custom ID.
func TestGitScanCustomIDTwice(t *testing.T) {
	gitCLIEnv(t)
	t.Setenv(auth.EnvToken, "test-pat")
	fake := &customIDAsana{fakeGitAsana: &fakeGitAsana{completed: map[string]bool{}, stories: map[string][]map[string]any{}}}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	repo := gitRepo(t)
	gitCommit(t, repo, "asana#ENG-1 once")
	if _, _, err := customRun(srv.URL, "git", "scan", "-C", repo); err != nil {
		t.Fatal(err)
	}
	n := len(fake.took())
	if _, _, err := customRun(srv.URL, "git", "scan", "-C", repo); err != nil {
		t.Fatal(err)
	}
	if got := len(fake.took()); got != n {
		t.Fatalf("second scan sent %d more requests: %v", got-n, fake.took())
	}
}

// --offline never asks Asana, uses what was found before, and a custom ID never found is "not found".
func TestGitScanOfflineNeverAsks(t *testing.T) {
	path := gitCLIEnv(t)
	t.Setenv(auth.EnvToken, "test-pat")
	var asked atomic.Int32
	fake := &customIDAsana{fakeGitAsana: &fakeGitAsana{completed: map[string]bool{}, stories: map[string][]map[string]any{}}}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	_ = st.SetSyncState(context.Background(), readmodel.KeyCustomID+"ENG-1", gitA)
	_ = st.Close()
	repo := gitRepo(t)
	gitCommit(t, repo, "asana#ENG-1 and asana#NEW-5")
	out, errOut, err := customRun(srv.URL, "git", "scan", "-C", repo, "--offline")
	if err != nil {
		t.Fatalf("%v\n%s%s", err, out, errOut)
	}
	asked.Store(int32(fake.lookups))
	if asked.Load() != 0 {
		t.Fatalf("--offline looked up %d custom IDs", asked.Load())
	}
	if !strings.Contains(out, "コメント: ENG-1 ソファを買う") || !strings.Contains(out, "見つかりません: NEW-5") {
		t.Fatalf("%s%s", out, errOut)
	}
}

// Not logged in: custom IDs fall back to the remembered ones, with no panic.
func TestGitScanCustomIDWithoutLogin(t *testing.T) {
	gitCLIEnv(t) // no token
	repo := gitRepo(t)
	gitCommit(t, repo, "asana#ENG-1")
	out, errOut, err := runCLISplit(t, "git", "scan", "-C", repo)
	if err != nil {
		t.Fatalf("%v\n%s%s", err, out, errOut)
	}
	if !strings.Contains(out, "見つかりません: ENG-1") {
		t.Fatalf("%s%s", out, errOut)
	}
}
