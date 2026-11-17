package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/amasok/asagao/internal/domain/commitref"
	"github.com/amasok/asagao/internal/readmodel"
)

// GitLinkStore is the part of the local cache that the git link use case needs.
// *store.Store satisfies it.
type GitLinkStore interface {
	GetTask(ctx context.Context, gid string) (*readmodel.TaskDetail, error)
	Me(ctx context.Context) (readmodel.UserRef, error)
	AddRichCommentLocal(ctx context.Context, taskGID, text, htmlText string, author readmodel.UserRef, now time.Time) (string, error)
	UpdateTaskLocal(ctx context.Context, gid string, changes map[string]any, now time.Time) error
}

// GitCommit is a git commit as the git link use case reads it.
type GitCommit struct {
	SHA     string // the full object name
	Message string // the whole message (git log --format=%B)
	Repo    string // the repository's name, shown in the comment ("" to leave it out)
	// Amends is set when the commit amends another one: that commit's message. Its refs
	// were handled when it was made (commitref.Amended).
	Amends *string
}

// Refs are the task references of the commit to handle.
func (c GitCommit) Refs() []commitref.Ref {
	refs := commitref.Parse(c.Message)
	if c.Amends != nil {
		refs = commitref.Amended(commitref.Parse(*c.Amends), refs)
	}
	return refs
}

// Subject is the first line of the message.
func (c GitCommit) Subject() string {
	s, _, _ := strings.Cut(strings.TrimLeft(c.Message, "\n"), "\n")
	return s
}

// What GitLink did for a task (GitLinkAction.Result).
const (
	GitLinkCommented = "commented" // a comment was added (and the task completed, if Completed)
	GitLinkAlready   = "already"   // the task has the commit's comment: nothing to do
	GitLinkNotFound  = "not_found" // the task is not in the cache: nothing done
)

// GitLinkAction is what GitLink did, or would do, for one task referenced by a commit.
type GitLinkAction struct {
	Commit   string `json:"commit"`
	TaskGID  string `json:"task_gid"`
	TaskName string `json:"task_name,omitempty"`
	// Fixes is set when the commit says the task is fixed.
	Fixes bool `json:"fixes"`
	// Result is GitLinkCommented, GitLinkAlready or GitLinkNotFound.
	Result string `json:"result"`
	// Completed is set when the task was completed (it was not completed before).
	Completed bool `json:"completed,omitempty"`
	// CommentGID is the local gid of the comment added.
	CommentGID string `json:"comment_gid,omitempty"`
}

// GitLink comments on the tasks a commit references, and completes those it fixes (#8).
// The changes are local and queued in the outbox; sending them, and refreshing the tasks
// from Asana before, is the entry point's.
type GitLink struct {
	Store GitLinkStore
	Text  RichText
	Clock Clock
}

// Apply handles the references in c. A task is handled once per commit: the comment it
// gets ends with commitref.Marker(c.SHA), and a task that has a comment with the marker
// (sent or still queued) is left as it is. So a commit handled again (a second scan, a
// scan over a range the hook saw) neither comments twice nor completes again a task that
// was reopened since. With dryRun nothing is written, and the actions say what would be
// done. A task not in the cache is reported (GitLinkNotFound) and the others are handled.
func (g *GitLink) Apply(ctx context.Context, c GitCommit, dryRun bool) ([]GitLinkAction, error) {
	var out []GitLinkAction
	for _, ref := range c.Refs() {
		act, err := g.apply(ctx, c, ref, dryRun)
		if err != nil {
			return out, err
		}
		out = append(out, act)
	}
	return out, nil
}

func (g *GitLink) apply(ctx context.Context, c GitCommit, ref commitref.Ref, dryRun bool) (GitLinkAction, error) {
	act := GitLinkAction{Commit: c.SHA, TaskGID: ref.TaskGID, Fixes: ref.Fixes}
	d, err := g.Store.GetTask(ctx, ref.TaskGID)
	if errors.Is(err, readmodel.ErrNotFound) {
		act.Result = GitLinkNotFound
		return act, nil
	}
	if err != nil {
		return act, err
	}
	act.TaskName = d.Name
	marker := commitref.Marker(c.SHA)
	for _, s := range d.Stories {
		if strings.Contains(s.Text, marker) {
			act.Result = GitLinkAlready
			return act, nil
		}
	}
	act.Result = GitLinkCommented
	act.Completed = ref.Fixes && !d.Completed
	if dryRun {
		return act, nil
	}
	now := g.Clock()
	// Complete first: if the comment then fails, the commit is not marked as handled and
	// the next run tries again (completing a completed task is skipped).
	if act.Completed {
		if err := g.Store.UpdateTaskLocal(ctx, ref.TaskGID, map[string]any{"completed": true}, now); err != nil {
			return act, err
		}
	}
	me, err := g.Store.Me(ctx)
	if err != nil {
		return act, err
	}
	html, plain := g.Text.Comment(GitComment(c, act.Completed))
	act.CommentGID, err = g.Store.AddRichCommentLocal(ctx, ref.TaskGID, plain, html, me, now)
	return act, err
}

// GitComment is the comment (Markdown) for commit c: what happened, the message in a code
// block (so that it is not read as Markdown) and the marker.
func GitComment(c GitCommit, completed bool) string {
	var b strings.Builder
	b.WriteString("コミット `" + shortSHA(c.SHA) + "`")
	if repo := strings.TrimSpace(strings.Map(repoRune, c.Repo)); repo != "" { // in code: "my_repo" is not emphasis
		b.WriteString(" (`" + repo + "`)")
	}
	if completed {
		b.WriteString(" で完了にしました:\n\n")
	} else {
		b.WriteString(" で言及されました:\n\n")
	}
	msg := strings.Trim(c.Message, "\n")
	fence := strings.Repeat("`", max(3, longestRun(msg, '`')+1))
	b.WriteString(fence + "\n" + msg + "\n" + fence + "\n\n")
	b.WriteString(commitref.Marker(c.SHA) + "\n")
	return b.String()
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func longestRun(s string, c byte) int {
	best, n := 0, 0
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			n++
			best = max(best, n)
		} else {
			n = 0
		}
	}
	return best
}

// repoRune keeps the repository's name on one line of inline code.
func repoRune(r rune) rune {
	switch {
	case r == '`':
		return -1
	case r < ' ' || r == 0x7f:
		return ' '
	}
	return r
}
