package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	usecase "github.com/amasok/asagao/internal/app" // the cli package has its own type app
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/richtext"
	"github.com/amasok/asagao/internal/sync"
	"github.com/amasok/asagao/internal/termsafe"
)

// The git commands (#8): `git scan` reads commit messages and comments on the tasks they
// reference ("#<gid>", "asana#<gid>"), completing those they fix ("fixes #<gid>"); the
// changes go through the outbox like any other write. `git install-hook` puts a
// post-commit hook that runs `git scan --hook HEAD` after each commit.

func (a *app) gitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "git",
		Short: "git のコミットメッセージからタスクにコメント・完了する",
		Long: "コミットメッセージの #<タスクID> でタスクにコメントし、fixes #<タスクID> で完了にします。\n" +
			"#<数字> は 10 桁以上のときだけタスク ID として読みます（#123 のような GitHub の Issue 番号は読みません）。\n" +
			"短い ID は asana#<タスクID> と書きます。完了にするキーワードは fix / fixes / fixed / close / closes / closed /\n" +
			"resolve / resolves / resolved（大文字小文字を問わず、同じ行で ID の直前）。",
	}
	cmd.AddCommand(a.gitScanCmd(), a.gitInstallHookCmd(), a.gitUninstallHookCmd())
	return cmd
}

// gitScanCommit is one commit of `git scan --json`.
type gitScanCommit struct {
	SHA     string                  `json:"sha"`
	Subject string                  `json:"subject"`
	Actions []usecase.GitLinkAction `json:"actions"`
}

// gitScanResult is the JSON output of `git scan`.
type gitScanResult struct {
	DryRun  bool            `json:"dry_run,omitempty"`
	Commits []gitScanCommit `json:"commits"`
	// Planned is what --dry-run would queue (as the other write commands' dry run says).
	Planned []plannedChange  `json:"planned,omitempty"`
	Sent    bool             `json:"sent"`
	Push    *sync.PushResult `json:"push,omitempty"`
	// Discarded lists queued changes dropped because what they changed was deleted in Asana.
	Discarded []readmodel.Discard `json:"discarded,omitempty"`
	Error     string              `json:"error,omitempty"`
	// Warnings are what did not stop the command: the tasks could not be refreshed from
	// Asana, say.
	Warnings []string `json:"warnings,omitempty"`
}

func (a *app) gitScanCmd() *cobra.Command {
	var dir string
	var offline, hook bool
	cmd := &cobra.Command{
		Use:   "scan [<コミット>|<範囲>...]",
		Short: "コミットのメッセージを読んで、参照しているタスクにコメント・完了する",
		Long: "コミット（省略時は HEAD）のメッセージにあるタスク ID のタスクにコメントし、fixes #<タスクID> なら完了にします。\n" +
			"main..HEAD のような範囲も渡せます。変更は outbox に積んで、オンラインならすぐ送ります。\n" +
			"同じコミットを何度読んでも、コメントは 1 つだけです（コメントの末尾の asagao-git: <コミット> で見分けます）。\n" +
			"オンラインなら、判断の前にタスクを Asana から取り直します。",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				args = []string{"HEAD"}
			}
			err := a.gitScan(cmd, dir, args, offline, hook)
			if err != nil && hook {
				// A hook does not fail the commit (it is made already): say what went wrong.
				fmt.Fprintln(cmd.ErrOrStderr(), "asagao:", termsafe.String(err.Error()))
				return nil
			}
			return err
		},
	}
	cmd.Flags().StringVarP(&dir, "repo", "C", ".", "git リポジトリのフォルダ")
	cmd.Flags().BoolVar(&offline, "offline", false, "Asana に問い合わせず、ローカルストアだけで判断して outbox に積む（送るのは次の sync）")
	a.addDryRunFlag(cmd) // #3: the commits are handled on a throwaway copy of the store
	cmd.Flags().BoolVar(&hook, "hook", false, "post-commit hook から呼ぶ（rebase などで作られたコミットは飛ばす。Asana から取り直さず、送信は短く 1 回だけ試す。失敗しても 0 で終わる）")
	_ = cmd.Flags().MarkHidden("hook")
	return cmd
}

func (a *app) gitScan(cmd *cobra.Command, dir string, revs []string, offline, hook bool) error {
	ctx := cmd.Context()
	dryRun := a.dryRun
	out := cmd.OutOrStdout()
	var commits []usecase.GitCommit
	var err error
	if hook {
		commits, err = hookCommits(ctx, dir)
	} else {
		commits, err = readCommits(ctx, dir, revs)
	}
	if err != nil {
		return err
	}
	res := gitScanResult{DryRun: dryRun, Commits: []gitScanCommit{}}
	var gids []string
	seen := map[string]bool{}
	for _, c := range commits {
		for _, r := range c.Refs() {
			if r.TaskGID != "" && !seen[r.TaskGID] {
				seen[r.TaskGID] = true
				gids = append(gids, r.TaskGID)
			}
		}
	}
	customIDs := usecase.CustomIDs(commits)
	if len(gids) == 0 && len(customIDs) == 0 {
		if a.json {
			for _, c := range commits {
				res.Commits = append(res.Commits, gitScanCommit{SHA: c.SHA, Subject: c.Subject(), Actions: []usecase.GitLinkAction{}})
			}
			return a.printJSON(res)
		}
		if dryRun {
			_ = a.printDryRun(cmd, dryRunResult{})
		}
		if !hook {
			fmt.Fprintln(out, "タスクの参照はありませんでした")
		}
		return nil
	}

	st, err := a.openStore(ctx)
	if err != nil {
		return err
	}
	defer st.Close()
	// Tasks named by custom ID ("asana#ENG-123") are found in Asana (the hook gives it a
	// short try), or as found before.
	custom, warn := a.resolveCustomIDs(ctx, st, customIDs, offline, hook)
	if warn != "" {
		res.Warnings = append(res.Warnings, warn)
	}
	for _, id := range customIDs {
		if gid := custom[id]; gid != "" && !seen[gid] {
			seen[gid] = true
			gids = append(gids, gid)
		}
	}
	// The cache may not have the comment that marks a commit as handled (made on another
	// computer, say): refresh the tasks first, and judge by the cache when that fails. The
	// hook does not wait for Asana: it judges by the cache. (A dry run refreshes its copy.)
	if !offline && !hook && len(gids) > 0 {
		if warn := a.refreshTasks(ctx, st, gids); warn != "" {
			res.Warnings = append(res.Warnings, warn)
		}
	}
	link := usecase.GitLink{Store: st, Text: richtext.Converter{}, Clock: time.Now, CustomIDs: custom}
	wrote := false
	for _, c := range commits {
		acts, err := link.Apply(ctx, c, false) // a dry run writes to its copy
		if err != nil {
			return fmt.Errorf("%s: %w", shortRev(c.SHA), err)
		}
		if acts == nil {
			acts = []usecase.GitLinkAction{}
		}
		for i, act := range acts {
			wrote = wrote || act.Result == usecase.GitLinkCommented
			if dryRun {
				acts[i].CommentGID = "" // a local gid of the copy means nothing
			}
		}
		res.Commits = append(res.Commits, gitScanCommit{SHA: c.SHA, Subject: c.Subject(), Actions: acts})
	}

	if d, ok := st.(*dryRunStore); ok {
		if res.Planned, _, err = d.queuedChanges(ctx); err != nil {
			return err
		}
	}
	var pushErr error
	if wrote && !dryRun && !offline {
		push := a.pushUseCase(st)
		if hook {
			// One short try: a commit is not held up by a slow network. What is not sent
			// stays queued for the next sync (a request cut off is checked then).
			push.Timeout = hookPushTimeout
		}
		rep := push.Run(ctx, a.pushConnect(st), usecase.PushTarget{})
		res.Push, pushErr = rep.Result, rep.Err
		if pushErr != nil {
			res.Error = pushErr.Error()
		}
		// The commits' changes are told apart from one another by the discards shown below,
		// not by the target (the scan names many tasks).
		res.Sent = rep.Outcome == usecase.PushSent
	}
	if wrote && !dryRun {
		res.Discarded = takeDiscards(ctx, st, out, !a.json)
	}
	if a.json {
		return a.printJSON(res)
	}
	errOut := cmd.ErrOrStderr()
	for _, w := range res.Warnings {
		fmt.Fprintln(errOut, "注意:", w)
	}
	if dryRun { // as the other write commands' dry run, and the tasks left alone
		_ = a.printDryRun(cmd, dryRunResult{Planned: res.Planned})
		printGitScan(out, res, hook, true)
		return nil
	}
	printGitScan(out, res, hook, false)
	switch {
	case !wrote:
	case offline:
		fmt.Fprintln(out, "（outbox に積みました。次の sync で送ります）")
	case errors.Is(pushErr, sync.ErrAccountChanged):
		fmt.Fprintf(out, "（まだ送っていません）\n%v\n", accountChanged(ctx, st))
	case hook && errors.Is(pushErr, context.DeadlineExceeded):
		fmt.Fprintln(out, "（すぐには送れなかったため、次の sync で送ります）")
	case errors.Is(pushErr, sync.ErrPushBusy):
		fmt.Fprintln(out, "（別の asagao が送信中です。この変更もそちらで送られます）")
	case pushErr != nil:
		fmt.Fprintf(out, "（オフラインのため未送信。次の sync で送ります: %v）\n", pushErr)
	case !res.Sent:
		fmt.Fprintln(out, "（送信できなかった変更があります。`asagao outbox list` で確認してください）")
	default:
		fmt.Fprintln(out, "（Asana に送信しました）")
	}
	return nil
}

// hookPushTimeout is how long the hook tries to send what it queued.
var hookPushTimeout = 3 * time.Second

// resolveCustomIDs finds the gids of the tasks with the custom IDs (app.ResolveCustomIDs):
// in Asana unless offline (the hook waits for it at most hookPushTimeout), else as found
// before. It returns a warning when Asana could not be asked.
func (a *app) resolveCustomIDs(ctx context.Context, st Store, ids []string, offline, hook bool) (map[string]string, string) {
	if len(ids) == 0 {
		return nil, ""
	}
	var find usecase.CustomIDFinder
	var problem error
	if !offline {
		timeout := 20 * time.Second
		if hook {
			timeout = hookPushTimeout
		}
		// The limit is Asana's only: the gids found before are read without it.
		deadline := time.Now().Add(timeout)
		actx, cancel := context.WithDeadline(ctx, deadline)
		api, err := a.apiClient(actx)
		cancel()
		if err != nil {
			problem = err
		} else {
			find = deadlineFinder{a.syncer(api, st), deadline}
		}
	}
	custom, err := usecase.ResolveCustomIDs(ctx, st, find, ids)
	if err == nil {
		err = problem
	}
	if err != nil {
		return custom, fmt.Sprintf("Asana でタスクの ID（%s）を引けませんでした。前に見つけたものだけを使います（%v）", strings.Join(ids, ", "), err)
	}
	return custom, ""
}

// deadlineFinder asks Asana for custom IDs until deadline.
type deadlineFinder struct {
	f        usecase.CustomIDFinder
	deadline time.Time
}

func (d deadlineFinder) TaskByCustomID(ctx context.Context, id string) (string, error) {
	ctx, cancel := context.WithDeadline(ctx, d.deadline)
	defer cancel()
	return d.f.TaskByCustomID(ctx, id)
}

// refreshTasks fetches the tasks gids from Asana. It returns a warning when that could not
// be done, for the commands to go on with the cache.
func (a *app) refreshTasks(ctx context.Context, st Store, gids []string) string {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	api, err := a.apiClient(ctx)
	if err != nil {
		return fmt.Sprintf("Asana からタスクを取り直せませんでした。ローカルストアの内容で判断します（%v）", err)
	}
	s := a.syncer(api, st)
	var failed []string
	var last error
	for _, gid := range gids {
		var attErr *sync.AttachmentsError
		if err := s.FetchDetails(ctx, gid); err != nil && !errors.As(err, &attErr) {
			failed, last = append(failed, gid), err
		}
	}
	if len(failed) == 0 {
		return ""
	}
	return fmt.Sprintf("Asana から取り直せなかったタスクがあります。ローカルストアの内容で判断します: %s（%v）", strings.Join(failed, ", "), last)
}

// printGitScan prints what was done for each commit; with skipped, only the tasks left
// alone (handled already, not found), after a dry run's list of changes.
func printGitScan(w io.Writer, res gitScanResult, hook, skipped bool) {
	for _, c := range res.Commits {
		if skipped {
			c.Actions = slices.DeleteFunc(slices.Clone(c.Actions), func(act usecase.GitLinkAction) bool {
				return act.Result == usecase.GitLinkCommented
			})
			if len(c.Actions) == 0 {
				continue
			}
		}
		if len(c.Actions) == 0 && hook {
			continue
		}
		fmt.Fprintf(w, "%s %s\n", shortRev(c.SHA), c.Subject)
		if len(c.Actions) == 0 {
			fmt.Fprintln(w, "  タスクの参照はありません")
		}
		for _, act := range c.Actions {
			name := act.TaskName
			if name == "" {
				name = act.TaskGID
			} else {
				name += " (" + act.TaskGID + ")"
			}
			if act.CustomID != "" {
				name = act.CustomID + " " + name
			}
			switch {
			case act.Result == usecase.GitLinkNotFound && act.CustomID != "" && act.TaskGID == "":
				fmt.Fprintf(w, "  見つかりません: %s（Asana にその ID のタスクがないか、Asana に届きませんでした）\n", act.CustomID)
			case act.Result == usecase.GitLinkNotFound:
				fmt.Fprintf(w, "  見つかりません: %s（ローカルストアにありません。`asagao sync` のあとでもう一度）\n", act.TaskGID)
			case act.Result == usecase.GitLinkAlready:
				fmt.Fprintf(w, "  処理済み: %s（このコミットのコメントがあります）\n", name)
			case act.Completed:
				fmt.Fprintf(w, "  完了にしてコメント: %s\n", name)
			default:
				fmt.Fprintf(w, "  コメント: %s\n", name)
			}
		}
	}
}

func shortRev(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// git runs git in dir and returns its output. Its error output goes into the error.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	c := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	if err := c.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return stdout.String(), nil
}

// readCommits reads the commits revs name, each once, oldest first within a range. A rev
// with ".." is a range (git rev-list); any other names one commit.
func readCommits(ctx context.Context, dir string, revs []string) ([]usecase.GitCommit, error) {
	var shas []string
	seen := map[string]bool{}
	for _, rev := range revs {
		if strings.HasPrefix(rev, "-") || rev == "" {
			return nil, fmt.Errorf("コミットの指定が正しくありません: %q", rev)
		}
		var out string
		var err error
		if strings.Contains(rev, "..") {
			out, err = git(ctx, dir, "rev-list", "--reverse", rev, "--")
		} else {
			out, err = git(ctx, dir, "rev-parse", "--verify", "--quiet", rev+"^{commit}")
			if err != nil {
				err = fmt.Errorf("コミットが見つかりません: %q", rev)
			}
		}
		if err != nil {
			return nil, err
		}
		for _, sha := range strings.Fields(out) {
			if !seen[sha] {
				seen[sha] = true
				shas = append(shas, sha)
			}
		}
	}
	repo := repoName(ctx, dir)
	commits := make([]usecase.GitCommit, 0, len(shas))
	for _, sha := range shas {
		msg, err := git(ctx, dir, "log", "-1", "--format=%B", sha, "--")
		if err != nil {
			return nil, err
		}
		commits = append(commits, usecase.GitCommit{SHA: sha, Message: msg, Repo: repo})
	}
	return commits, nil
}

// hookCommits is the commit the post-commit hook is run for: HEAD, when it was made by
// `git commit` (HEAD's reflog says "commit", "commit (amend)", "commit (merge)"...).
// Commits that rebase, cherry-pick or revert make run the hook too; they repeat messages
// that were handled already, and are left out. An amend handles only the references the
// amended commit did not have. Without a reflog for HEAD (core.logAllRefUpdates=false, or
// a reflog whose newest entry is not HEAD) a new commit cannot be told from an amend or a
// rebase: a commit with references is left out with errNoReflog, which says how to read it.
func hookCommits(ctx context.Context, dir string) ([]usecase.GitCommit, error) {
	commits, err := readCommits(ctx, dir, []string{"HEAD"})
	if err != nil || len(commits) == 0 {
		return commits, err
	}
	entry, err := git(ctx, dir, "reflog", "-1", "--format=%H%x00%gs", "HEAD", "--")
	sha, action, _ := strings.Cut(strings.TrimSpace(entry), "\x00")
	if err != nil || sha != commits[0].SHA {
		if len(commits[0].Refs()) == 0 {
			return nil, nil
		}
		return nil, errNoReflog
	}
	if !strings.HasPrefix(action, "commit") {
		return nil, nil
	}
	if strings.HasPrefix(action, "commit (amend)") {
		if prev, err := git(ctx, dir, "log", "-1", "--format=%B", "HEAD@{1}", "--"); err == nil {
			commits[0].Amends = &prev
		}
	}
	return commits, nil
}

var errNoReflog = errors.New("HEAD の reflog がないため（core.logAllRefUpdates）、新しいコミットか amend・rebase かを見分けられません。" +
	"このコミットは読みませんでした。読むには `asagao git scan HEAD` を実行してください")

// repoName is the name of the repository's folder ("" if git does not say).
func repoName(ctx context.Context, dir string) string {
	top, err := git(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return ""
	}
	return filepath.Base(strings.TrimSpace(top))
}

// hookMarker is the line that marks the post-commit hook asagao installed.
const hookMarker = "# asagao-git-hook"

// hookLine is what a hook runs; it is shown for adding to a hook that exists already.
const hookLine = `asagao git scan --hook HEAD`

// hookScript is the post-commit hook, running the asagao at exe (or the one on PATH when
// exe is gone).
func hookScript(exe string) string {
	return "#!/bin/sh\n" +
		hookMarker + ": `asagao git install-hook` が入れた post-commit hook です。\n" +
		"# コミットメッセージの #<タスクID> でタスクにコメントし、fixes #<タスクID> で完了にします。\n" +
		"# 外すには `asagao git uninstall-hook`。\n" +
		"asagao=" + shQuote(exe) + "\n" +
		"if [ ! -x \"$asagao\" ]; then\n" +
		"\tasagao=$(command -v asagao) || exit 0\n" +
		"fi\n" +
		"\"$asagao\" git scan --hook HEAD\n" +
		"exit 0\n"
}

// isOurHook reports whether content is a hook asagao installed and nobody changed.
func isOurHook(content string) bool {
	_, rest, ok := strings.Cut(content, "\nasagao=")
	if !ok {
		return false
	}
	line, _, _ := strings.Cut(rest, "\n")
	exe, ok := shUnquote(line)
	return ok && content == hookScript(exe)
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// shUnquote reverses shQuote.
func shUnquote(s string) (string, bool) {
	if len(s) < 2 || s[0] != '\'' || s[len(s)-1] != '\'' {
		return "", false
	}
	out := strings.ReplaceAll(s[1:len(s)-1], `'\''`, "'")
	return out, shQuote(out) == s
}

// hookPath is where git looks for the post-commit hook of the repository at dir
// (core.hooksPath is followed; worktrees share the hooks of the main repository).
func hookPath(ctx context.Context, dir string) (string, error) {
	p, err := git(ctx, dir, "rev-parse", "--git-path", "hooks/post-commit")
	if err != nil {
		return "", err
	}
	p = strings.TrimSpace(p)
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}
	return p, nil
}

func (a *app) gitInstallHookCmd() *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:   "install-hook",
		Short: "コミットのたびに git scan を呼ぶ post-commit hook を入れる",
		Long: "post-commit hook を入れて、コミットのたびにそのメッセージでタスクにコメント・完了します。\n" +
			"すでにほかの post-commit hook があるときは上書きせず、足す 1 行を表示します。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := hookPath(cmd.Context(), dir)
			if err != nil {
				return err
			}
			exe, err := os.Executable()
			if err != nil {
				exe = "asagao"
			}
			out := cmd.OutOrStdout()
			b, err := os.ReadFile(path)
			switch {
			case err == nil && isOurHook(string(b)):
				// Ours: rewrite it, for the path of this asagao.
				if err := writeHook(path, hookScript(exe), true); err != nil {
					return err
				}
				fmt.Fprintf(out, "post-commit hook は入っています（%s を更新しました）\n", path)
				return nil
			case err == nil && strings.Contains(string(b), hookLine):
				fmt.Fprintf(out, "post-commit hook は %s から asagao を呼んでいます。変更はありません\n", path)
				return nil
			case err == nil:
				return fmt.Errorf("post-commit hook がすでにあるため、上書きしません: %s\n"+
					"コミットのたびに asagao を呼ぶには、その hook に次の 1 行を足してください:\n  %s", path, hookLine)
			case !errors.Is(err, os.ErrNotExist):
				return err
			}
			if _, lerr := os.Lstat(path); lerr == nil { // a dangling symbolic link: not ours either
				return fmt.Errorf("post-commit hook がすでにあるため、上書きしません: %s", path)
			}
			if err := writeHook(path, hookScript(exe), false); err != nil {
				return err
			}
			fmt.Fprintf(out, "post-commit hook を入れました: %s\n", path)
			return nil
		},
	}
	cmd.Flags().StringVarP(&dir, "repo", "C", ".", "git リポジトリのフォルダ")
	return cmd
}

// writeHook writes an executable hook at path. Unless replace is set, a file there (made
// meanwhile) is not overwritten.
func writeHook(path, content string, replace bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if replace {
		tmp := path + ".asagao-tmp"
		if err := os.WriteFile(tmp, []byte(content), 0o755); err != nil {
			return err
		}
		if err := os.Chmod(tmp, 0o755); err != nil { // the umask may have taken the x bits
			_ = os.Remove(tmp)
			return err
		}
		return os.Rename(tmp, path)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("post-commit hook がすでにあるため、上書きしません: %s", path)
		}
		return err
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Chmod(path, 0o755)
}

func (a *app) gitUninstallHookCmd() *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:   "uninstall-hook",
		Short: "install-hook で入れた post-commit hook を外す",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := hookPath(cmd.Context(), dir)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			b, err := os.ReadFile(path)
			switch {
			case errors.Is(err, os.ErrNotExist):
				fmt.Fprintln(out, "post-commit hook は入っていません")
				return nil
			case err != nil:
				return err
			case !isOurHook(string(b)):
				if strings.Contains(string(b), hookLine) {
					return fmt.Errorf("post-commit hook は asagao が入れたものではない（または書き換えられている）ため、消しません: %s\n"+
						"asagao を呼ばないようにするには、その中の次の行を消してください:\n  %s", path, hookLine)
				}
				return fmt.Errorf("post-commit hook は asagao が入れたものではないため、消しません: %s", path)
			}
			if err := os.Remove(path); err != nil {
				return err
			}
			fmt.Fprintf(out, "post-commit hook を外しました: %s\n", path)
			return nil
		},
	}
	cmd.Flags().StringVarP(&dir, "repo", "C", ".", "git リポジトリのフォルダ")
	return cmd
}
