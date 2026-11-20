package cli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/auth"
	"github.com/amasok/asagao/internal/store"
)

// Commits that name a task by its custom ID, "asana#ENG-1" (Issue #36).

// customIDAsana answers GET /workspaces/w1/tasks/custom_id/ENG-1 with gitA, and passes the
// rest to fakeGitAsana.
type customIDAsana struct {
	*fakeGitAsana
	lookups int
}

func (f *customIDAsana) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if id, ok := strings.CutPrefix(r.URL.Path, "/workspaces/w1/tasks/custom_id/"); ok {
		f.lookups++
		if id != "ENG-1" {
			http.Error(w, `{"errors":[{"message":"Not Found"}]}`, http.StatusNotFound)
			return
		}
		r.URL.Path = "/tasks/" + gitA
	}
	f.fakeGitAsana.ServeHTTP(w, r)
}

func TestGitScanByCustomID(t *testing.T) {
	path := gitCLIEnv(t)
	t.Setenv(auth.EnvToken, "test-pat")
	fake := &customIDAsana{fakeGitAsana: &fakeGitAsana{completed: map[string]bool{}, stories: map[string][]map[string]any{}}}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	run := func(baseURL string, args ...string) (string, string, error) {
		cmd := newTestRoot(baseURL)
		var out, errOut bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&errOut)
		cmd.SetArgs(args)
		err := cmd.ExecuteContext(context.Background())
		return out.String(), errOut.String(), err
	}
	repo := gitRepo(t)
	gitCommit(t, repo, "fixes asana#ENG-1 and see asana#ENG-404")
	out, errOut, err := run(srv.URL, "git", "scan", "-C", repo)
	if err != nil {
		t.Fatalf("%v\n%s%s", err, out, errOut)
	}
	if !strings.Contains(out, "完了にしてコメント: ENG-1 ソファを買う") || !strings.Contains(out, "見つかりません: ENG-404") {
		t.Fatalf("output:\n%s%s", out, errOut)
	}
	if calls := fake.took(); strings.Join(calls, " ") != "PUT /tasks/"+gitA+" POST /tasks/"+gitA+"/stories" {
		t.Fatalf("requests %q", calls)
	}
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	gid, _ := st.CustomIDTaskGID(context.Background(), "ENG-1")
	_ = st.Close()
	if gid != gitA {
		t.Fatalf("gid not remembered: %q", gid)
	}

	// offline later: the gid found before is used (the commit was handled: nothing to do)
	gitCommit(t, repo, "asana#ENG-1 again")
	out, errOut, err = run(unreachableAsana(t), "git", "scan", "-C", repo, "HEAD~1..HEAD")
	if err != nil {
		t.Fatalf("%v\n%s%s", err, out, errOut)
	}
	if !strings.Contains(out, "コメント: ENG-1 ソファを買う") || !strings.Contains(errOut, "タスクの ID") {
		t.Fatalf("offline output:\n%s%s", out, errOut)
	}
	if ops := cliOps(t, path); len(ops) != 1 {
		t.Fatalf("queued %v", opKinds(ops))
	}
}

// Asana too slow for the hook: the gids found before are still used for every ID (the
// time limit is Asana's, not the cache's).
func TestGitScanHookCustomIDSlowAsana(t *testing.T) {
	path, fake, run := onlineGitEnv(t)
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	for id, gid := range map[string]string{"ENG-1": gitA, "ENG-2": gitB} {
		if err := st.SetCustomIDTaskGID(context.Background(), id, gid); err != nil {
			t.Fatal(err)
		}
	}
	_ = st.Close()
	old := hookPushTimeout
	hookPushTimeout = 300 * time.Millisecond
	t.Cleanup(func() { hookPushTimeout = old })
	fake.mu.Lock()
	fake.delay = 10 * time.Second
	fake.mu.Unlock()
	repo := gitRepo(t)
	gitCommit(t, repo, "fixes asana#ENG-1, see asana#ENG-2")
	start := time.Now()
	out, errOut, err := run("git", "scan", "--hook", "-C", repo, "HEAD")
	if err != nil {
		t.Fatalf("%v\n%s%s", err, out, errOut)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("the hook took %v", d)
	}
	if !strings.Contains(out, "完了にしてコメント: ENG-1 ソファを買う") || !strings.Contains(out, "コメント: ENG-2 カーテンを買う") {
		t.Fatalf("output:\n%s%s", out, errOut)
	}
	if !strings.Contains(errOut, "タスクの ID") {
		t.Errorf("no warning:\n%s", errOut)
	}
}
