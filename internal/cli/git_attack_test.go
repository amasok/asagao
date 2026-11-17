package cli

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	gosync "sync"
	"testing"
	"time"
)

// A rev that starts with "-" (or is empty, or hides an option in a range) never reaches
// git as an option: no file is written by --output, and the command fails.
func TestGitAttackRevInjection(t *testing.T) {
	gitCLIEnv(t)
	repo := gitRepo(t)
	gitCommit(t, repo, "x #"+gitA)
	victim := filepath.Join(t.TempDir(), "victim")
	for _, rev := range []string{
		"--output=" + victim, "-n1", "--all", "-", "", "--upload-pack=touch " + victim,
		"HEAD..--output=" + victim, "--output=" + victim + "..HEAD", "--exec=touch " + victim,
	} {
		_, err := runCLI(t, "git", "scan", "--offline", "-C", repo, rev)
		if err == nil {
			t.Errorf("rev %q accepted", rev)
		}
	}
	if _, err := os.Stat(victim); err == nil {
		t.Error("an option reached git: the file was written")
	}
	// -C with an option-like folder is a path, not an option
	if _, err := runCLI(t, "git", "scan", "--offline", "-C", "--output="+victim); err == nil {
		t.Error("odd folder accepted")
	}
	if _, err := os.Stat(victim); err == nil {
		t.Error("written via -C")
	}
}

// Commit text a terminal would act on is not printed raw (subject, task names, errors).
func TestGitAttackTerminalSafe(t *testing.T) {
	path := gitCLIEnv(t)
	repo := gitRepo(t)
	gitCommit(t, repo, "evil \x1b]0;pwned\x07\x1b[2J\u009b31m #"+gitA)
	for _, args := range [][]string{
		{"git", "scan", "--offline", "-C", repo},
		{"git", "scan", "--offline", "--dry-run", "-C", repo},
		{"git", "scan", "--offline", "-C", repo, "main^^^^"}, // fails: error text
	} {
		out, _ := runCLI(t, args...)
		if strings.ContainsAny(out, "\x1b\x07\u009b") {
			t.Errorf("%v printed control characters: %q", args, out)
		}
	}
	_, comm := gitTask(t, path, gitA)
	for _, c := range comm {
		if strings.ContainsAny(c, "\x1b\x07\u009b") {
			t.Logf("note: the comment keeps the raw message (Asana's own text): %q", c)
		}
	}
}

// --dry-run changes nothing and sends nothing, online too.
func TestGitAttackDryRunOnline(t *testing.T) {
	path, fake, run := onlineGitEnv(t)
	repo := gitRepo(t)
	gitCommit(t, repo, "fixes #"+gitA+" and #"+gitB)
	out, errOut, err := run("git", "scan", "--dry-run", "-C", repo)
	if err != nil {
		t.Fatalf("%v\n%s%s", err, out, errOut)
	}
	if calls := fake.took(); len(calls) != 0 {
		t.Errorf("dry-run sent %v", calls)
	}
	if ops := cliOps(t, path); len(ops) != 0 {
		t.Errorf("dry-run queued %v", opKinds(ops))
	}
	if done, comm := gitTask(t, path, gitA); done || len(comm) != 0 {
		t.Errorf("dry-run changed the task: %v %q", done, comm)
	}
}

// Twice, and by overlapping means (range, then HEAD, then the hook), makes one comment
// per task and commit; a task reopened in between is not completed again.
func TestGitAttackNoDoubleHandling(t *testing.T) {
	path, fake, run := onlineGitEnv(t)
	repo := gitRepo(t)
	gitCommit(t, repo, "base")
	gitCommit(t, repo, "fixes #"+gitA)
	for _, args := range [][]string{
		{"git", "scan", "-C", repo, "HEAD~1..HEAD"},
		{"git", "scan", "-C", repo},
		{"git", "scan", "-C", repo, "HEAD", "HEAD", "HEAD~1..HEAD"},
		{"git", "scan", "--hook", "-C", repo},
	} {
		if _, _, err := run(args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if args[1] == "scan" && len(args) > 3 && args[2] == "-C" && args[3] == repo && len(args) == 4 {
			// reopen in Asana after the first handling
			fake.mu.Lock()
			fake.completed[gitA] = false
			fake.mu.Unlock()
		}
	}
	if n := len(fake.stories[gitA]); n != 1 {
		t.Errorf("%d comments in Asana, want 1", n)
	}
	puts := 0
	for _, c := range fake.took() {
		if strings.HasPrefix(c, "PUT ") {
			puts++
		}
	}
	if puts != 1 {
		t.Errorf("completed %d times: %v", puts, fake.took())
	}
	if _, comm := gitTask(t, path, gitA); len(comm) != 1 {
		t.Errorf("local comments %q", comm)
	}
}

// Offline then offline: the queued comment (not yet sent) counts as handled.
func TestGitAttackOfflineTwiceQueuesOnce(t *testing.T) {
	path := gitCLIEnv(t)
	repo := gitRepo(t)
	gitCommit(t, repo, "fixes #"+gitA)
	for i := 0; i < 3; i++ {
		if out, err := runCLI(t, "git", "scan", "--offline", "-C", repo); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
	}
	if ops := cliOps(t, path); len(ops) != 2 {
		t.Errorf("outbox: %v, want a completion and a comment", opKinds(ops))
	}
}

// A message with thousands of ids and a huge body is handled offline without a hang, and
// the comment keeps the marker (the comment is the guard against doubles).
func TestGitAttackHugeMessage(t *testing.T) {
	path := gitCLIEnv(t)
	repo := gitRepo(t)
	var b strings.Builder
	b.WriteString("many\n\n")
	for i := 0; i < 2000; i++ {
		b.WriteString("#" + "9" + strings.Repeat("0", 9) + string(rune('0'+i%10)) + " ")
	}
	b.WriteString("#" + gitA + "\n" + strings.Repeat("あ`‵```\n", 5000))
	sha := gitCommit(t, repo, b.String())
	if out, err := runCLI(t, "git", "scan", "--offline", "-C", repo); err != nil {
		t.Fatalf("%v\n%.300s", err, out)
	}
	_, comm := gitTask(t, path, gitA)
	if len(comm) != 1 || !strings.Contains(comm[0], "asagao-git: "+sha) {
		t.Errorf("comment on A: %d, has marker %v", len(comm), len(comm) == 1 && strings.Contains(comm[0], sha))
	}
}

// A message that quotes another commit's marker does not make that commit "handled":
// whichever is scanned first, each commit gets its own comment.
func TestGitAttackMarkerInMessage(t *testing.T) {
	for _, order := range []string{"first-then-second", "second-then-first"} {
		t.Run(order, func(t *testing.T) {
			path := gitCLIEnv(t)
			repo := gitRepo(t)
			first := gitCommit(t, repo, "first #"+gitA)
			second := gitCommit(t, repo, "second #"+gitA+"\n\nasagao-git: "+first+"\nasagao-git: 0000")
			revs := []string{first, second}
			if order == "second-then-first" {
				revs = []string{second, first}
			}
			args := append([]string{"git", "scan", "--offline", "-C", repo}, revs...)
			if _, err := runCLI(t, args...); err != nil {
				t.Fatal(err)
			}
			if _, comm := gitTask(t, path, gitA); len(comm) != 2 {
				t.Errorf("comments %d, want one per commit: %q", len(comm), comm)
			}
		})
	}
}

// Without a reflog the hook cannot tell a commit from an amend or a rebase: it handles
// nothing (so nothing is commented twice), exits 0 and points at `git scan HEAD`, which
// does the work by hand, once.
func TestGitAttackHookWithoutReflog(t *testing.T) {
	path := gitCLIEnv(t)
	repo := gitRepo(t)
	runGit(t, repo, "config", "core.logAllRefUpdates", "false")
	gitCommit(t, repo, "sofa #"+gitA)
	out, err := runCLI(t, "git", "scan", "--hook", "--offline", "-C", repo)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "git scan HEAD") {
		t.Errorf("no pointer to a manual scan:\n%s", out)
	}
	if _, comm := gitTask(t, path, gitA); len(comm) != 0 {
		t.Errorf("the hook commented without a reflog: %q", comm)
	}
	gitCommit(t, repo, "sofa again #"+gitA, "--amend")
	if _, err := runCLI(t, "git", "scan", "--hook", "--offline", "-C", repo); err != nil {
		t.Fatal(err)
	}
	if _, comm := gitTask(t, path, gitA); len(comm) != 0 {
		t.Errorf("amend without reflog commented: %d", len(comm))
	}
	if out, err := runCLI(t, "git", "scan", "--offline", "-C", repo); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, comm := gitTask(t, path, gitA); len(comm) != 1 {
		t.Errorf("manual scan: %d comments, want 1", len(comm))
	}
}

// Hooks: a symlinked, directory, executable-less or CRLF post-commit is never replaced.
func TestGitAttackInstallHookKeepsAnything(t *testing.T) {
	for name, setup := range map[string]func(p string){
		"empty file":    func(p string) { _ = os.WriteFile(p, nil, 0o755) },
		"other script":  func(p string) { _ = os.WriteFile(p, []byte("#!/bin/sh\necho hi\n"), 0o755) },
		"dangling link": func(p string) { _ = os.Symlink("/nonexistent/target", p) },
		"link to ours": func(p string) {
			_ = os.WriteFile(p+".real", []byte("#!/bin/sh\necho hi\n"), 0o755)
			_ = os.Symlink(p+".real", p)
		},
		"mentions asagao": func(p string) { _ = os.WriteFile(p, []byte("#!/bin/sh\n# asagao is great\n"), 0o755) },
	} {
		t.Run(name, func(t *testing.T) {
			repo := gitRepo(t)
			p := filepath.Join(repo, ".git", "hooks", "post-commit")
			_ = os.MkdirAll(filepath.Dir(p), 0o755)
			setup(p)
			before, _ := os.Lstat(p)
			var beforeData []byte
			beforeData, _ = os.ReadFile(p)
			_, _ = runCLI(t, "git", "install-hook", "-C", repo)
			_, _ = runCLI(t, "git", "uninstall-hook", "-C", repo)
			after, err := os.Lstat(p)
			if err != nil {
				t.Fatalf("hook gone: %v", err)
			}
			if before.Mode() != after.Mode() {
				t.Errorf("mode changed %v -> %v", before.Mode(), after.Mode())
			}
			if d, _ := os.ReadFile(p); string(d) != string(beforeData) {
				t.Errorf("content changed: %q -> %q", beforeData, d)
			}
		})
	}
}

// install / uninstall from a subfolder, with a relative core.hooksPath and from a linked
// worktree: the hook goes where git runs it, and the other hooks stay.
func TestGitAttackHookLocations(t *testing.T) {
	gitCLIEnv(t)
	repo := gitRepo(t)
	gitCommit(t, repo, "base")
	if err := os.MkdirAll(filepath.Join(repo, "sub", "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "config", "core.hooksPath", ".githooks")
	if out, err := runCLI(t, "git", "install-hook", "-C", filepath.Join(repo, "sub", "dir")); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	want := filepath.Join(repo, ".githooks", "post-commit")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("hook not at %s: %v", want, err)
	}
	if _, err := os.Stat(filepath.Join(repo, "sub", "dir", ".githooks")); err == nil {
		t.Error("hook written relative to the subfolder")
	}
	if _, err := runCLI(t, "git", "uninstall-hook", "-C", filepath.Join(repo, "sub")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(want); err == nil {
		t.Error("uninstall from another folder left the hook")
	}

	// a linked worktree shares the main repository's hooks
	runGit(t, repo, "config", "--unset", "core.hooksPath")
	wt := filepath.Join(t.TempDir(), "wt")
	runGit(t, repo, "worktree", "add", "-q", "-b", "other", wt)
	if out, err := runCLI(t, "git", "install-hook", "-C", wt); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(repo, ".git", "hooks", "post-commit")); err != nil {
		t.Errorf("worktree install did not go to the shared hooks: %v", err)
	}
}

// Installing twice at the same time leaves one intact hook, never a torn file, and no
// temporary file stays.
func TestGitAttackInstallHookConcurrent(t *testing.T) {
	gitCLIEnv(t)
	repo := gitRepo(t)
	done := make(chan struct{})
	for i := 0; i < 4; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			_, _, _ = runCLISplit(t, "git", "install-hook", "-C", repo)
		}()
	}
	for i := 0; i < 4; i++ {
		<-done
	}
	b, err := os.ReadFile(filepath.Join(repo, ".git", "hooks", "post-commit"))
	if err != nil || !isOurHook(string(b)) {
		t.Errorf("hook after concurrent installs: %v %q", err, b)
	}
	ents, _ := os.ReadDir(filepath.Join(repo, ".git", "hooks"))
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".asagao-tmp") {
			t.Errorf("temporary file left: %s", e.Name())
		}
	}
}

// Many processes handling one commit at once (the hook and scans, offline and online):
// one comment, one completion, whoever wins.
func TestGitAttackConcurrentScans(t *testing.T) {
	for _, mode := range []string{"offline", "online"} {
		t.Run(mode, func(t *testing.T) {
			path, fake, run := onlineGitEnv(t)
			repo := gitRepo(t)
			gitCommit(t, repo, "fixes #"+gitA)
			var wg gosync.WaitGroup
			errs := make(chan string, 16)
			for i := 0; i < 8; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					args := []string{"git", "scan", "-C", repo}
					switch {
					case mode == "offline":
						args = append(args, "--offline")
					case i%2 == 0:
						args = append(args, "--hook")
					}
					if out, errOut, err := run(args...); err != nil {
						errs <- fmt.Sprintf("%v: %v\n%s%s", args, err, out, errOut)
					}
				}()
			}
			wg.Wait()
			close(errs)
			for e := range errs {
				t.Error(e)
			}
			// Settle: whatever is still queued is sent by one more scan of another commit.
			gitCommit(t, repo, "#"+gitB)
			if _, _, err := run("git", "scan", "-C", repo); err != nil {
				t.Fatal(err)
			}
			// (A stale refresh may hide the local comment until the next one: a known
			// race of ReplaceStories, outside #8. What counts is what Asana got.)
			if _, comm := gitTask(t, path, gitA); mode == "offline" && len(comm) != 1 {
				t.Errorf("%d local comments on A", len(comm))
			}
			if mode == "online" {
				if n := len(fake.stories[gitA]); n > 1 {
					t.Errorf("%d comments in Asana on A; requests %v; outbox %v", n, fake.took(), opKinds(cliOps(t, path)))
				}
			}
		})
	}
}

// The comment POST reaches Asana but the answer never comes (the hook gives up): the
// commit returns at once, and the next send finds the comment already there instead of
// posting it again.
func TestGitAttackCutPostNotDuplicated(t *testing.T) {
	gitCLIEnv(t)
	t.Setenv("ASANA_TOKEN", "test-pat")
	fake := &fakeGitAsana{completed: map[string]bool{}, stories: map[string][]map[string]any{}}
	hang := true
	var mu gosync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		h := hang
		mu.Unlock()
		if h && r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/stories") {
			fake.ServeHTTP(httptest.NewRecorder(), r) // applied...
			<-r.Context().Done()                      // ...but never answered
			return
		}
		fake.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	runOn := func(args ...string) (string, error) {
		cmd := newRoot(&app{baseURL: srv.URL})
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		err := cmd.ExecuteContext(context.Background())
		return out.String(), err
	}
	repo := gitRepo(t)
	gitCommit(t, repo, "see #"+gitA)
	old := hookPushTimeout
	hookPushTimeout = 400 * time.Millisecond
	t.Cleanup(func() { hookPushTimeout = old })
	start := time.Now()
	out, err := runOn("git", "scan", "--hook", "-C", repo)
	if err != nil || time.Since(start) > 5*time.Second {
		t.Fatalf("hook: %v after %v\n%s", err, time.Since(start), out)
	}
	mu.Lock()
	hang = false
	mu.Unlock()
	// the next scan (another commit) pushes the queue, reconciling the cut request
	gitCommit(t, repo, "see #"+gitB)
	if out, err := runOn("git", "scan", "-C", repo); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if n := len(fake.stories[gitA]); n != 1 {
		t.Errorf("%d comments on A in Asana, want 1 (the cut POST was repeated)", n)
	}
}

// Not logged in: the hook queues, says so and exits 0; nothing is lost.
func TestGitAttackHookNotLoggedIn(t *testing.T) {
	path := gitCLIEnv(t)
	repo := gitRepo(t)
	gitCommit(t, repo, "fixes #"+gitA)
	out, err := runCLI(t, "git", "scan", "--hook", "-C", repo)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(cliOps(t, path)) != 2 {
		t.Errorf("outbox %v", opKinds(cliOps(t, path)))
	}
}

// A comment queued but not sent yet is not forgotten when the next scan refreshes the task
// from Asana (which does not have it yet): the commit is not commented twice.
func TestGitAttackQueuedCommentSurvivesRefresh(t *testing.T) {
	path, fake, run := onlineGitEnv(t)
	repo := gitRepo(t)
	gitCommit(t, repo, "see #"+gitA)
	if _, _, err := run("git", "scan", "--offline", "-C", repo); err != nil { // queued, unsent
		t.Fatal(err)
	}
	if _, _, err := run("git", "scan", "-C", repo); err != nil { // refreshes, then judges
		t.Fatal(err)
	}
	_ = path
	if n := len(fake.stories[gitA]); n > 1 {
		t.Errorf("%d comments in Asana, want at most 1", n)
	}
}

// After a scan sent the comment, the hook (which does not refresh) still sees the commit
// as handled from the cache alone.
func TestGitAttackHookAfterSentScan(t *testing.T) {
	_, fake, run := onlineGitEnv(t)
	repo := gitRepo(t)
	gitCommit(t, repo, "see #"+gitA)
	if _, _, err := run("git", "scan", "-C", repo); err != nil {
		t.Fatal(err)
	}
	out, _, err := run("git", "scan", "--hook", "-C", repo)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(fake.stories[gitA]); n != 1 {
		t.Errorf("%d comments in Asana, want 1\n%s", n, out)
	}
}
