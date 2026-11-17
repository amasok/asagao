package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/readmodel"
)

const (
	gitTaskA = "1204567890123456"
	gitTaskB = "1204567890123457"
	gitSHA   = "0123456789abcdef0123456789abcdef01234567"
)

// gitStore is a cache of tasks for the git link use case: it keeps the comments added
// (as stories, the way the store shows queued comments) and the completions.
type gitStore struct {
	tasks     map[string]*readmodel.TaskDetail
	calls     []string
	commentAt []time.Time
	failWrite error
	failGet   error
}

func newGitStore(gids ...string) *gitStore {
	f := &gitStore{tasks: map[string]*readmodel.TaskDetail{}}
	for _, gid := range gids {
		f.tasks[gid] = &readmodel.TaskDetail{}
		f.tasks[gid].GID, f.tasks[gid].Name = gid, "task "+gid
	}
	return f
}

func (f *gitStore) GetTask(_ context.Context, gid string) (*readmodel.TaskDetail, error) {
	if f.failGet != nil {
		return nil, f.failGet
	}
	d, ok := f.tasks[gid]
	if !ok {
		return nil, fmt.Errorf("task %s: %w", gid, readmodel.ErrNotFound)
	}
	cp := *d
	cp.Stories = append([]readmodel.StoryView(nil), d.Stories...)
	return &cp, nil
}

func (f *gitStore) Me(context.Context) (readmodel.UserRef, error) {
	return readmodel.UserRef{GID: "me", Name: "Me"}, nil
}

func (f *gitStore) AddRichCommentLocal(_ context.Context, taskGID, text, _ string, _ readmodel.UserRef, now time.Time) (string, error) {
	f.calls = append(f.calls, "comment "+taskGID)
	if f.failWrite != nil {
		return "", f.failWrite
	}
	f.tasks[taskGID].Stories = append(f.tasks[taskGID].Stories, readmodel.StoryView{GID: "local:c", Type: "comment", Text: text, Pending: true})
	f.commentAt = append(f.commentAt, now)
	return "local:c", nil
}

func (f *gitStore) UpdateTaskLocal(_ context.Context, gid string, changes map[string]any, _ time.Time) error {
	f.calls = append(f.calls, fmt.Sprintf("update %s %v", gid, changes))
	if c, ok := changes["completed"].(bool); ok {
		f.tasks[gid].Completed = c
	}
	return nil
}

func gitLink(st *gitStore) *GitLink {
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	return &GitLink{Store: st, Text: &fakeText{}, Clock: func() time.Time { return now }}
}

// A mention comments; "fixes" completes and comments; both carry the commit's marker.
func TestGitLinkApply(t *testing.T) {
	st := newGitStore(gitTaskA, gitTaskB)
	c := GitCommit{SHA: gitSHA, Message: "Add login #" + gitTaskA + "\n\nfixes #" + gitTaskB + "\n", Repo: "web"}
	acts, err := gitLink(st).Apply(context.Background(), c, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []GitLinkAction{
		{Commit: gitSHA, TaskGID: gitTaskA, TaskName: "task " + gitTaskA, Result: GitLinkCommented, CommentGID: "local:c"},
		{Commit: gitSHA, TaskGID: gitTaskB, TaskName: "task " + gitTaskB, Fixes: true, Result: GitLinkCommented, Completed: true, CommentGID: "local:c"},
	}
	if !reflect.DeepEqual(acts, want) {
		t.Fatalf("actions\n got %+v\nwant %+v", acts, want)
	}
	wantCalls := []string{"comment " + gitTaskA, "update " + gitTaskB + " map[completed:true]", "comment " + gitTaskB}
	if !reflect.DeepEqual(st.calls, wantCalls) {
		t.Errorf("calls %q, want %q", st.calls, wantCalls)
	}
	for _, gid := range []string{gitTaskA, gitTaskB} {
		text := st.tasks[gid].Stories[0].Text
		if !strings.Contains(text, "asagao-git: "+gitSHA) || !strings.Contains(text, "Add login") {
			t.Errorf("comment on %s: %q", gid, text)
		}
	}
	if !strings.Contains(st.tasks[gitTaskB].Stories[0].Text, "完了にしました") || !strings.Contains(st.tasks[gitTaskA].Stories[0].Text, "言及されました") {
		t.Errorf("comments should say what happened: %q / %q", st.tasks[gitTaskA].Stories[0].Text, st.tasks[gitTaskB].Stories[0].Text)
	}
}

// Handling the same commit again adds nothing, even when the task was reopened since: the
// marker says it was handled.
func TestGitLinkApplyTwice(t *testing.T) {
	st := newGitStore(gitTaskA)
	c := GitCommit{SHA: gitSHA, Message: "fixes #" + gitTaskA}
	if _, err := gitLink(st).Apply(context.Background(), c, false); err != nil {
		t.Fatal(err)
	}
	st.tasks[gitTaskA].Completed = false // reopened by hand
	st.calls = nil
	acts, err := gitLink(st).Apply(context.Background(), c, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(acts) != 1 || acts[0].Result != GitLinkAlready || acts[0].Completed {
		t.Errorf("second run: %+v", acts)
	}
	if len(st.calls) != 0 || len(st.tasks[gitTaskA].Stories) != 1 || st.tasks[gitTaskA].Completed {
		t.Errorf("second run wrote: calls %q, stories %d, completed %v", st.calls, len(st.tasks[gitTaskA].Stories), st.tasks[gitTaskA].Completed)
	}
	// Another commit naming the same task is handled.
	acts, err = gitLink(st).Apply(context.Background(), GitCommit{SHA: strings.Repeat("f", 40), Message: "#" + gitTaskA}, false)
	if err != nil || len(acts) != 1 || acts[0].Result != GitLinkCommented {
		t.Errorf("another commit: %+v, %v", acts, err)
	}
}

// The marker must be the whole object name: a comment for another commit whose name starts
// the same does not count.
func TestGitLinkMarkerIsTheFullSHA(t *testing.T) {
	st := newGitStore(gitTaskA)
	st.tasks[gitTaskA].Stories = []readmodel.StoryView{{Type: "comment", Text: "asagao-git: " + gitSHA[:12]}}
	acts, err := gitLink(st).Apply(context.Background(), GitCommit{SHA: gitSHA, Message: "#" + gitTaskA}, false)
	if err != nil || len(acts) != 1 || acts[0].Result != GitLinkCommented {
		t.Errorf("%+v, %v", acts, err)
	}
}

// A task already completed is not completed again, but still gets the comment.
func TestGitLinkFixesCompletedTask(t *testing.T) {
	st := newGitStore(gitTaskA)
	st.tasks[gitTaskA].Completed = true
	acts, err := gitLink(st).Apply(context.Background(), GitCommit{SHA: gitSHA, Message: "fixes #" + gitTaskA}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(acts) != 1 || acts[0].Completed || acts[0].Result != GitLinkCommented || !acts[0].Fixes {
		t.Errorf("%+v", acts)
	}
	if !reflect.DeepEqual(st.calls, []string{"comment " + gitTaskA}) {
		t.Errorf("calls %q", st.calls)
	}
	if !strings.Contains(st.tasks[gitTaskA].Stories[0].Text, "言及されました") {
		t.Errorf("comment %q", st.tasks[gitTaskA].Stories[0].Text)
	}
}

// Dry run reports and writes nothing.
func TestGitLinkDryRun(t *testing.T) {
	st := newGitStore(gitTaskA)
	acts, err := gitLink(st).Apply(context.Background(), GitCommit{SHA: gitSHA, Message: "fixes #" + gitTaskA}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(acts) != 1 || acts[0].Result != GitLinkCommented || !acts[0].Completed || acts[0].CommentGID != "" {
		t.Errorf("%+v", acts)
	}
	if len(st.calls) != 0 || st.tasks[gitTaskA].Completed {
		t.Errorf("dry run wrote: %q", st.calls)
	}
}

// A task not in the cache is reported; the others are handled.
func TestGitLinkNotFound(t *testing.T) {
	st := newGitStore(gitTaskB)
	acts, err := gitLink(st).Apply(context.Background(), GitCommit{SHA: gitSHA, Message: "#" + gitTaskA + " #" + gitTaskB}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(acts) != 2 || acts[0].Result != GitLinkNotFound || acts[1].Result != GitLinkCommented {
		t.Errorf("%+v", acts)
	}
}

// Other store errors stop and are returned.
func TestGitLinkStoreError(t *testing.T) {
	st := newGitStore(gitTaskA)
	st.failGet = errors.New("disk")
	if _, err := gitLink(st).Apply(context.Background(), GitCommit{SHA: gitSHA, Message: "#" + gitTaskA}, false); err == nil || err.Error() != "disk" {
		t.Errorf("err %v", err)
	}
	st = newGitStore(gitTaskA)
	st.failWrite = errors.New("full")
	if _, err := gitLink(st).Apply(context.Background(), GitCommit{SHA: gitSHA, Message: "#" + gitTaskA}, false); err == nil || err.Error() != "full" {
		t.Errorf("err %v", err)
	}
}

// The message goes in a code block whose fence is longer than any run of backticks in it,
// so a "```" line in the message does not end it.
func TestGitComment(t *testing.T) {
	md := GitComment(GitCommit{SHA: gitSHA, Message: "\nSubject\n\n````\nx\n", Repo: "my_`repo\n"}, false)
	want := "コミット `0123456789ab` (`my_repo`) で言及されました:\n\n`````\nSubject\n\n````\nx\n`````\n\nasagao-git: " + gitSHA + "\n"
	if md != want {
		t.Errorf("got\n%q\nwant\n%q", md, want)
	}
	if md := GitComment(GitCommit{SHA: "abc", Message: "s"}, true); md != "コミット `abc` で完了にしました:\n\n```\ns\n```\n\nasagao-git: abc\n" {
		t.Errorf("got %q", md)
	}
}

func TestGitCommitSubject(t *testing.T) {
	if s := (GitCommit{Message: "\n\nfirst\nsecond"}).Subject(); s != "first" {
		t.Errorf("Subject = %q", s)
	}
}

// An amended commit handles only what its previous version did not have.
func TestGitLinkAmend(t *testing.T) {
	st := newGitStore(gitTaskA, gitTaskB)
	prev := "Add login #" + gitTaskA + " #" + gitTaskB
	c := GitCommit{SHA: gitSHA, Message: "Add login #" + gitTaskA + "\n\nfixes #" + gitTaskB, Amends: &prev}
	acts, err := gitLink(st).Apply(context.Background(), c, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(acts) != 1 || acts[0].TaskGID != gitTaskB || !acts[0].Completed {
		t.Errorf("%+v", acts)
	}
	if len(st.tasks[gitTaskA].Stories) != 0 {
		t.Errorf("the amend commented again on %s", gitTaskA)
	}
}
