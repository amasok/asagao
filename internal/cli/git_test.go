package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	gosync "sync"
	"testing"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/auth"
	"github.com/amasok/asagao/internal/config"
	"github.com/amasok/asagao/internal/store"
)

const (
	gitA = "1204567890123456" // ソファを買う
	gitB = "1204567890123457" // カーテンを買う
)

// gitRepo makes a git repository in a temporary folder, with git's settings kept away from
// the user's (no global or system config: a gpg signing setting would break the commits).
func gitRepo(t *testing.T) string {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "T")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "T")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.com")
	dir := filepath.Join(t.TempDir(), "web")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "init", "-q", "-b", "main")
	return dir
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// gitCommit makes a commit with msg (adding a line to a file of its own, so that it can be
// reverted) and returns its object name.
func gitCommit(t *testing.T, dir, msg string, extra ...string) string {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(dir, "log.txt"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(msg + "\n")
	_ = f.Close()
	runGit(t, dir, "add", "log.txt")
	runGit(t, dir, append([]string{"commit", "-q", "-m", msg}, extra...)...)
	return runGit(t, dir, "rev-parse", "HEAD")
}

// gitCLIEnv is a local store (no token: offline) with the tasks gitA and gitB.
func gitCLIEnv(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ASANA_TOKEN", "")
	t.Setenv("ASANA_CLIENT_ID", "")
	for _, d := range []string{config.DataDir(), config.ConfigDir()} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(config.FilePath(), []byte("[auth]\ntoken_store = \"file\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(config.DataDir(), "store.db")
	st := createStoreAt(t, path)
	p1 := []asana.Membership{{Project: &asana.Ref{GID: "p1"}, Section: &asana.Ref{GID: "s1"}}}
	if err := st.UpsertTasks(context.Background(), []asana.Task{
		{GID: gitA, Name: "ソファを買う", Assignee: &asana.Ref{GID: "me"}, Workspace: &asana.Ref{GID: "w1"}, Memberships: p1},
		{GID: gitB, Name: "カーテンを買う", Assignee: &asana.Ref{GID: "me"}, Workspace: &asana.Ref{GID: "w1"}, Memberships: p1},
	}); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	return path
}

func gitTask(t *testing.T, path, gid string) (completed bool, comments []string) {
	t.Helper()
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	d, err := st.GetTask(context.Background(), gid)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range d.Stories {
		if s.Type == "comment" {
			comments = append(comments, s.Text)
		}
	}
	return d.Completed, comments
}

// Offline: a mention comments, "fixes" completes and comments, both queued in the outbox;
// a second scan of the same commit adds nothing.
func TestGitScanOffline(t *testing.T) {
	path := gitCLIEnv(t)
	repo := gitRepo(t)
	sha := gitCommit(t, repo, "Add sofa #"+gitA+"\n\nfixes #"+gitB+"\nSee #30 on GitHub.")
	out, err := runCLI(t, "git", "scan", "-C", repo)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{sha[:12] + " Add sofa", "コメント: ソファを買う (" + gitA + ")", "完了にしてコメント: カーテンを買う (" + gitB + ")", "未送信"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	doneA, commA := gitTask(t, path, gitA)
	doneB, commB := gitTask(t, path, gitB)
	if doneA || !doneB {
		t.Errorf("completed: A %v, B %v", doneA, doneB)
	}
	if len(commA) != 1 || len(commB) != 1 || !strings.Contains(commA[0], "asagao-git: "+sha) || !strings.Contains(commB[0], "完了にしました") || !strings.Contains(commA[0], "(web)") {
		t.Fatalf("comments A %q, B %q", commA, commB)
	}
	if kinds := opKinds(cliOps(t, path)); strings.Join(kinds, " ") != "add_comment:"+gitA+" update_task:"+gitB+" add_comment:"+gitB {
		t.Errorf("outbox %v", kinds)
	}

	// Again (B reopened meanwhile): nothing new.
	if _, err := runCLI(t, "task", "reopen", gitB); err != nil {
		t.Fatal(err)
	}
	out, err = runCLI(t, "git", "scan", "-C", repo, sha)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "処理済み: ソファを買う") || !strings.Contains(out, "処理済み: カーテンを買う") || strings.Contains(out, "未送信") {
		t.Errorf("second scan:\n%s", out)
	}
	doneB, commB = gitTask(t, path, gitB)
	_, commA = gitTask(t, path, gitA)
	if doneB || len(commA) != 1 || len(commB) != 1 {
		t.Errorf("second scan wrote: B completed %v, comments A %d B %d", doneB, len(commA), len(commB))
	}
}

// Ranges: each commit once, oldest first; commits without references are listed as such.
func TestGitScanRange(t *testing.T) {
	path := gitCLIEnv(t)
	repo := gitRepo(t)
	base := gitCommit(t, repo, "init")
	first := gitCommit(t, repo, "first #"+gitA)
	gitCommit(t, repo, "no ref (#8)")
	last := gitCommit(t, repo, "second #"+gitA)
	out, err := runCLI(t, "git", "scan", "-C", repo, base+"..HEAD", "HEAD")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.Count(out, "コメント: ソファを買う") != 2 || !strings.Contains(out, "タスクの参照はありません") {
		t.Errorf("output:\n%s", out)
	}
	if i, j := strings.Index(out, first[:12]), strings.Index(out, last[:12]); i < 0 || j < i {
		t.Errorf("not oldest first:\n%s", out)
	}
	_, comm := gitTask(t, path, gitA)
	if len(comm) != 2 {
		t.Errorf("comments %q", comm)
	}
}

// Nothing to do: no store is touched, and --dry-run writes nothing.
func TestGitScanNoRefsAndDryRun(t *testing.T) {
	path := gitCLIEnv(t)
	repo := gitRepo(t)
	gitCommit(t, repo, "Merge pull request #30")
	out, err := runCLI(t, "git", "scan", "-C", repo)
	if err != nil || !strings.Contains(out, "タスクの参照はありませんでした") {
		t.Fatalf("%v\n%s", err, out)
	}
	gitCommit(t, repo, "fixes #"+gitA)
	out, err = runCLI(t, "git", "scan", "-C", repo, "--dry-run")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "完了にしてコメント: ソファを買う") || !strings.Contains(out, "dry-run") {
		t.Errorf("output:\n%s", out)
	}
	if done, comm := gitTask(t, path, gitA); done || len(comm) != 0 || len(cliOps(t, path)) != 0 {
		t.Errorf("dry run wrote: completed %v, comments %q", done, comm)
	}
}

// A task not in the cache is reported; bad revisions and options are errors.
func TestGitScanErrors(t *testing.T) {
	gitCLIEnv(t)
	repo := gitRepo(t)
	gitCommit(t, repo, "#9999999999 unknown")
	out, err := runCLI(t, "git", "scan", "-C", repo)
	if err != nil || !strings.Contains(out, "見つかりません: 9999999999") {
		t.Errorf("%v\n%s", err, out)
	}
	for _, rev := range []string{"nope", "--output=/tmp/x", "-p"} {
		if out, err := runCLI(t, "git", "scan", "-C", repo, "--", rev); err == nil {
			t.Errorf("%q: no error\n%s", rev, out)
		}
	}
	if _, err := runCLI(t, "git", "scan", "-C", t.TempDir()); err == nil {
		t.Error("not a repository: no error")
	}
}

// --json lists the actions.
func TestGitScanJSON(t *testing.T) {
	gitCLIEnv(t)
	repo := gitRepo(t)
	sha := gitCommit(t, repo, "fixes #"+gitA)
	stdout, _, err := runCLISplit(t, "--json", "git", "scan", "-C", repo, "--offline")
	if err != nil {
		t.Fatal(err)
	}
	var r gitScanResult
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("%q: %v", stdout, err)
	}
	if len(r.Commits) != 1 || r.Commits[0].SHA != sha || len(r.Commits[0].Actions) != 1 {
		t.Fatalf("%+v", r)
	}
	if a := r.Commits[0].Actions[0]; a.TaskGID != gitA || !a.Completed || a.Result != "commented" || a.CommentGID == "" {
		t.Errorf("%+v", a)
	}
}

// The hook handles commits made by `git commit`, not those a rebase or revert makes, and
// an amend only handles what it adds.
func TestGitScanHook(t *testing.T) {
	path := gitCLIEnv(t)
	repo := gitRepo(t)
	gitCommit(t, repo, "init")
	runGit(t, repo, "checkout", "-q", "-b", "topic")
	sha := gitCommit(t, repo, "sofa #"+gitA)
	hook := func() string {
		t.Helper()
		out, err := runCLI(t, "git", "scan", "--hook", "-C", repo, "HEAD")
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		return out
	}
	hook()
	if _, comm := gitTask(t, path, gitA); len(comm) != 1 {
		t.Fatalf("comments %q", comm)
	}
	// amend adding a fix of B: B only
	gitCommit(t, repo, "sofa #"+gitA+"\n\nfixes #"+gitB, "--amend")
	hook()
	if _, comm := gitTask(t, path, gitA); len(comm) != 1 {
		t.Errorf("amend commented on A again: %q", comm)
	}
	if done, comm := gitTask(t, path, gitB); !done || len(comm) != 1 {
		t.Errorf("B after amend: completed %v, comments %q", done, comm)
	}
	// rebase onto a new main commit: new object names, same messages: skipped
	runGit(t, repo, "checkout", "-q", "main")
	if err := os.WriteFile(filepath.Join(repo, "main.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "main.txt")
	runGit(t, repo, "commit", "-q", "-m", "main moves")
	runGit(t, repo, "checkout", "-q", "topic")
	runGit(t, repo, "rebase", "-q", "main")
	if runGit(t, repo, "rev-parse", "HEAD") == sha {
		t.Fatal("rebase did not rewrite")
	}
	if out := hook(); out != "" {
		t.Errorf("hook after a rebase printed:\n%s", out)
	}
	// revert: skipped
	runGit(t, repo, "revert", "--no-edit", "HEAD")
	hook()
	if _, comm := gitTask(t, path, gitB); len(comm) != 1 {
		t.Errorf("revert commented: %q", comm)
	}
	// errors do not fail the hook
	out, err := runCLI(t, "git", "scan", "--hook", "-C", t.TempDir())
	if err != nil || !strings.Contains(out, "asagao:") {
		t.Errorf("hook outside a repository: %v\n%s", err, out)
	}
	// nothing to say: silent
	gitCommit(t, repo, "no refs")
	if out := hook(); out != "" {
		t.Errorf("hook without refs printed:\n%s", out)
	}
}

func TestGitInstallHook(t *testing.T) {
	gitCLIEnv(t)
	repo := gitRepo(t)
	path := filepath.Join(repo, ".git", "hooks", "post-commit")
	out, err := runCLI(t, "git", "install-hook", "-C", repo)
	if err != nil || !strings.Contains(out, "入れました") {
		t.Fatalf("%v\n%s", err, out)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm()&0o111 == 0 {
		t.Errorf("hook not executable: %v", fi.Mode())
	}
	if !isOurHook(string(b)) || !strings.Contains(string(b), "git scan --hook HEAD") {
		t.Errorf("hook:\n%s", b)
	}
	if out, err := exec.Command("sh", "-n", path).CombinedOutput(); err != nil {
		t.Errorf("sh -n: %v\n%s", err, out)
	}
	// again: ours, rewritten
	if out, err := runCLI(t, "git", "install-hook", "-C", repo); err != nil || !strings.Contains(out, "入っています") {
		t.Errorf("again: %v\n%s", err, out)
	}
	// uninstall, twice
	if out, err := runCLI(t, "git", "uninstall-hook", "-C", repo); err != nil || !strings.Contains(out, "外しました") {
		t.Errorf("uninstall: %v\n%s", err, out)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("hook left: %v", err)
	}
	if out, err := runCLI(t, "git", "uninstall-hook", "-C", repo); err != nil || !strings.Contains(out, "入っていません") {
		t.Errorf("uninstall again: %v\n%s", err, out)
	}
}

// Another hook is never overwritten nor removed.
func TestGitInstallHookKeepsOtherHooks(t *testing.T) {
	gitCLIEnv(t)
	repo := gitRepo(t)
	path := filepath.Join(repo, ".git", "hooks", "post-commit")
	other := "#!/bin/sh\necho mine\n"
	if err := os.WriteFile(path, []byte(other), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := runCLI(t, "git", "install-hook", "-C", repo)
	if err == nil || !strings.Contains(err.Error(), "上書きしません") || !strings.Contains(err.Error(), hookLine) {
		t.Errorf("install over another hook: %v\n%s", err, out)
	}
	if _, err := runCLI(t, "git", "uninstall-hook", "-C", repo); err == nil {
		t.Error("uninstall of another hook: no error")
	}
	// ours, edited by the user: not removed either
	exe, _ := os.Executable()
	if err := os.WriteFile(path, []byte(hookScript(exe)+"echo mine\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, "git", "uninstall-hook", "-C", repo); err == nil {
		t.Error("uninstall of an edited hook: no error")
	}
	// a hook that calls asagao already
	if err := os.WriteFile(path, []byte(other+hookLine+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, "git", "install-hook", "-C", repo); err != nil || !strings.Contains(out, "変更はありません") {
		t.Errorf("hook calling asagao: %v\n%s", err, out)
	}
	if b, _ := os.ReadFile(path); string(b) != other+hookLine+"\n" {
		t.Errorf("hook changed:\n%s", b)
	}
	// a dangling symbolic link
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(repo, "nowhere"), path); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, "git", "install-hook", "-C", repo); err == nil {
		t.Error("install over a dangling link: no error")
	}
}

// core.hooksPath is followed, and the hooks folder is made when missing.
func TestGitInstallHookHooksPath(t *testing.T) {
	gitCLIEnv(t)
	repo := gitRepo(t)
	runGit(t, repo, "config", "core.hooksPath", ".githooks")
	if out, err := runCLI(t, "git", "install-hook", "-C", repo); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(repo, ".githooks", "post-commit")); err != nil {
		t.Error(err)
	}
}

func TestShQuote(t *testing.T) {
	for _, s := range []string{"", "/usr/bin/asagao", "/a b/it's", "'", "''x'"} {
		got, ok := shUnquote(shQuote(s))
		if !ok || got != s {
			t.Errorf("%q: %q %v", s, got, ok)
		}
	}
	for _, s := range []string{"", "'", "abc", "'a'b'"} {
		if _, ok := shUnquote(s); ok {
			t.Errorf("%q should not unquote", s)
		}
	}
}

// fakeGitAsana is a fake Asana for the git commands: tasks gitA and gitB, their comments,
// and the requests that change them.
type fakeGitAsana struct {
	mu        gosync.Mutex
	calls     []string
	completed map[string]bool
	stories   map[string][]map[string]any
}

var tagRE = regexp.MustCompile(`<[^>]*>`)

func (f *fakeGitAsana) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var body struct {
		Data map[string]any `json:"data"`
	}
	raw, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(raw, &body)
	if r.Method != http.MethodGet {
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	}
	reply := func(v any) { _ = json.NewEncoder(w).Encode(map[string]any{"data": v}) }
	task := func(gid string) map[string]any {
		names := map[string]string{gitA: "ソファを買う", gitB: "カーテンを買う"}
		return map[string]any{"gid": gid, "name": names[gid], "completed": f.completed[gid], "assignee": map[string]string{"gid": "me"},
			"workspace":   map[string]string{"gid": "w1"},
			"memberships": []any{map[string]any{"project": map[string]string{"gid": "p1"}, "section": map[string]string{"gid": "s1"}}}}
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	known := len(parts) >= 2 && parts[0] == "tasks" && (parts[1] == gitA || parts[1] == gitB)
	switch {
	case r.URL.Path == "/users/me":
		reply(map[string]string{"gid": "me", "name": "Me"})
	case r.URL.Path == "/attachments":
		reply([]any{})
	case known && len(parts) == 2 && r.Method == http.MethodGet:
		reply(task(parts[1]))
	case known && len(parts) == 2 && r.Method == http.MethodPut:
		if v, ok := body.Data["completed"].(bool); ok {
			f.completed[parts[1]] = v
		}
		reply(task(parts[1]))
	case known && len(parts) == 3 && parts[2] == "subtasks":
		reply([]any{})
	case known && len(parts) == 3 && parts[2] == "stories" && r.Method == http.MethodGet:
		reply(f.stories[parts[1]])
	case known && len(parts) == 3 && parts[2] == "stories":
		h, _ := body.Data["html_text"].(string)
		s := map[string]any{"gid": "story" + parts[1] + string(rune('a'+len(f.stories[parts[1]]))), "type": "comment", "resource_subtype": "comment_added",
			"text": html.UnescapeString(tagRE.ReplaceAllString(h, "")), "html_text": h,
			"created_by": map[string]string{"gid": "me", "name": "Me"}, "created_at": "2026-11-18T00:00:00Z"}
		f.stories[parts[1]] = append(f.stories[parts[1]], s)
		reply(s)
	default:
		http.Error(w, `{"errors":[{"message":"not in the fake"}]}`, http.StatusNotFound)
	}
}

func (f *fakeGitAsana) took() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func onlineGitEnv(t *testing.T) (string, *fakeGitAsana, func(args ...string) (string, string, error)) {
	t.Helper()
	path := gitCLIEnv(t)
	t.Setenv(auth.EnvToken, "test-pat")
	fake := &fakeGitAsana{completed: map[string]bool{}, stories: map[string][]map[string]any{}}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	run := func(args ...string) (string, string, error) {
		cmd := newRoot(&app{baseURL: srv.URL})
		var out, errOut bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&errOut)
		cmd.SetArgs(args)
		err := cmd.ExecuteContext(context.Background())
		return out.String(), errOut.String(), err
	}
	return path, fake, run
}

// Online: sent right away. A comment for the commit already in Asana (made from another
// computer, not in this cache) is seen after the refresh, so the commit is not handled
// twice.
func TestGitScanOnline(t *testing.T) {
	path, fake, run := onlineGitEnv(t)
	repo := gitRepo(t)
	sha := gitCommit(t, repo, "fixes #"+gitA)
	out, errOut, err := run("git", "scan", "-C", repo)
	if err != nil {
		t.Fatalf("%v\n%s%s", err, out, errOut)
	}
	if !strings.Contains(out, "Asana に送信しました") {
		t.Errorf("output:\n%s%s", out, errOut)
	}
	if calls := fake.took(); strings.Join(calls, " ") != "PUT /tasks/"+gitA+" POST /tasks/"+gitA+"/stories" {
		t.Errorf("requests %q", calls)
	}
	if len(cliOps(t, path)) != 0 {
		t.Errorf("left in the outbox: %v", opKinds(cliOps(t, path)))
	}
	if !strings.Contains(fake.stories[gitA][0]["text"].(string), "asagao-git: "+sha) {
		t.Errorf("comment sent: %q", fake.stories[gitA][0]["text"])
	}

	// Another computer: a fresh cache without the comment. Reopened in Asana meanwhile.
	fake.mu.Lock()
	fake.completed[gitA] = false
	fake.mu.Unlock()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	gitCLIEnvAt(t, path)
	out, errOut, err = run("git", "scan", "-C", repo)
	if err != nil {
		t.Fatalf("%v\n%s%s", err, out, errOut)
	}
	if !strings.Contains(out, "処理済み") {
		t.Errorf("second computer:\n%s%s", out, errOut)
	}
	if calls := fake.took(); len(calls) != 2 {
		t.Errorf("second computer sent: %q", calls)
	}
}

// gitCLIEnvAt makes the store of gitCLIEnv again at path (the environment is set already).
func gitCLIEnvAt(t *testing.T, path string) {
	t.Helper()
	st := createStoreAt(t, path)
	p1 := []asana.Membership{{Project: &asana.Ref{GID: "p1"}, Section: &asana.Ref{GID: "s1"}}}
	if err := st.UpsertTasks(context.Background(), []asana.Task{
		{GID: gitA, Name: "ソファを買う", Completed: true, Workspace: &asana.Ref{GID: "w1"}, Memberships: p1},
	}); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
}

// The hook script runs the asagao it names with the arguments of a hook scan, from the
// repository, after `git commit` has written HEAD's reflog (which hookCommits reads).
func TestGitHookScriptRuns(t *testing.T) {
	repo := gitRepo(t)
	bin := t.TempDir()
	rec := filepath.Join(bin, "rec")
	stub := filepath.Join(bin, "it's asagao")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\necho \"$@\" >> \""+rec+"\"\ngit reflog -1 --format=%gs HEAD >> \""+rec+"\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeHook(filepath.Join(repo, ".git", "hooks", "post-commit"), hookScript(stub), false); err != nil {
		t.Fatal(err)
	}
	gitCommit(t, repo, "first")
	gitCommit(t, repo, "first again", "--amend")
	b, err := os.ReadFile(rec)
	if err != nil {
		t.Fatal(err)
	}
	want := "git scan --hook HEAD\ncommit (initial): first\ngit scan --hook HEAD\ncommit (amend): first again\n"
	if string(b) != want {
		t.Errorf("hook ran:\n%s\nwant:\n%s", b, want)
	}
	// asagao gone and not on PATH: the hook does nothing and the commit succeeds
	if err := os.Remove(stub); err != nil {
		t.Fatal(err)
	}
	// (PATH has only git: an asagao installed next to it must not run on the real store.)
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	onlyGit := t.TempDir()
	if err := os.Symlink(gitPath, filepath.Join(onlyGit, "git")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", onlyGit)
	gitCommit(t, repo, "second")
	if b2, _ := os.ReadFile(rec); string(b2) != want {
		t.Errorf("hook ran without asagao:\n%s", b2)
	}
}
