package app_test

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/richtext"
	"github.com/amasok/asagao/internal/store"
)

// The comment and status update use cases write what the entry points wrote before them
// (stage a4): the same rows, the same outbox entries (payload, time) and the same errors.
// Each case runs the entry point code of main before a4, pasted below, on one store and the
// use case on another, seeded the same, with the same richtext, so that a change in the
// conversion itself (made in internal/richtext) does not change this comparison.

var postNow = time.Date(2026, 11, 17, 9, 30, 0, 0, time.UTC)

// Before a4: TUI addComment (tui/write.go), TUI applyComment (tui/edit.go), CLI comment add
// (cli/write.go) and MCP add_comment (mcpserver/server.go) were all this.
func mainAddComment(ctx context.Context, st *store.Store, taskGID, text string, now time.Time) (string, error) {
	me, err := st.Me(ctx)
	if err != nil {
		return "", err
	}
	return st.AddRichCommentLocal(ctx, taskGID, richtext.Comment.PlainFromMarkdown(text), richtext.Comment.FromMarkdown(text), me, now)
}

// Before a4: CLI project status post (cli/project.go) and MCP create_status_update
// (mcpserver/server.go).
func mainCreateStatus(ctx context.Context, st *store.Store, project, status, title, text string, now time.Time) error {
	gid, err := st.ResolveProject(ctx, project)
	if err != nil {
		return err
	}
	me, err := st.Me(ctx)
	if err != nil {
		return err
	}
	_, err = st.CreateStatusLocal(ctx, gid, status, title, richtext.StatusUpdate.PlainFromMarkdown(text), richtext.StatusUpdate.FromMarkdown(text), me, now)
	return err
}

// Before a4: TUI applyStatus (tui/projects.go), with the project's gid.
func mainCreateStatusHere(ctx context.Context, st *store.Store, projectGID, status, title, body string, now time.Time) error {
	me, err := st.Me(ctx)
	if err != nil {
		return err
	}
	_, err = st.CreateStatusLocal(ctx, projectGID, status, title, richtext.StatusUpdate.PlainFromMarkdown(body), richtext.StatusUpdate.FromMarkdown(body), me, now)
	return err
}

func postStore(t *testing.T, withMe bool) *store.Store {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	if withMe {
		must(st.SetSyncState(ctx, store.KeyMeGID, "me"))
		must(st.UpsertUsers(ctx, []asana.User{{GID: "me", Name: "私"}}))
	}
	must(st.UpsertProjects(ctx, "w1", []asana.Project{
		{GID: "p1", Name: "新居プロジェクト", Members: []asana.Ref{{GID: "me"}}},
		{GID: "p2", Name: "p1"}, // a project named like the gid of another
	}, "me"))
	must(st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "ソファを買う"}}))
	return st
}

// snapshot is what a store holds after the write, with the random local gids numbered in
// the order they appear.
func snapshot(t *testing.T, st *store.Store) string {
	t.Helper()
	ctx := context.Background()
	var b strings.Builder
	ops, err := st.ListOps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range ops {
		fmt.Fprintf(&b, "op %s task=%s state=%s at=%s payload=%v\n", op.Kind, op.TaskGID, op.State, op.CreatedAt, op.Payload)
	}
	d, err := st.GetTask(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range d.Stories {
		fmt.Fprintf(&b, "story %s %s/%s text=%q html=%q by=%+v at=%s pending=%v\n", s.GID, s.Type, s.ResourceSubtype, s.Text, s.HTMLText, s.CreatedBy, s.CreatedAt, s.Pending)
	}
	for _, p := range []string{"p1", "p2"} {
		us, err := st.StatusUpdates(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		for _, u := range us {
			fmt.Fprintf(&b, "status %s %s %s title=%q text=%q html=%q author=%q at=%s pending=%v\n", p, u.GID, u.StatusType, u.Title, u.Text, u.HTMLText, u.Author, u.CreatedAt, u.Pending)
		}
	}
	return numberLocalGIDs(b.String())
}

func numberLocalGIDs(s string) string {
	var out strings.Builder
	seen := map[string]int{}
	for {
		i := strings.Index(s, store.LocalPrefix)
		if i < 0 {
			out.WriteString(s)
			return out.String()
		}
		out.WriteString(s[:i])
		j := i + len(store.LocalPrefix)
		for j < len(s) && (s[j] >= '0' && s[j] <= '9' || s[j] >= 'a' && s[j] <= 'z' || s[j] >= 'A' && s[j] <= 'Z') {
			j++
		}
		gid := s[i:j]
		if _, ok := seen[gid]; !ok {
			seen[gid] = len(seen) + 1
		}
		fmt.Fprintf(&out, "<local %d>", seen[gid])
		s = s[j:]
	}
}

var postTexts = []string{
	"",
	"   \n",
	"よろしくお願いします",
	"# 予定\n\n---\n\n![図](asana:42)",
	"**太字** と _斜体_ と ~~取り消し~~ と `code`\n\n[リンク](https://example.com) https://example.com/bare",
	"- a\n- b\n  - c\n\n1. one\n2. two\n\n> 引用\n\n```\nfn()\n```",
	"<script>alert(1)</script> & <b>x</b>",
	"@[花子](asana:user/12) さん\n\n|a|b|\n|-|-|\n|1|2|",
	"\x1b[31m赤\x1b[0m",
}

func TestCommentsAddWritesWhatMainWrote(t *testing.T) {
	ctx := context.Background()
	for _, withMe := range []bool{true, false} {
		for _, task := range []string{"t1", "t9"} {
			for i, text := range postTexts {
				t.Run(fmt.Sprintf("me=%v/%s/%d", withMe, task, i), func(t *testing.T) {
					old, now := postStore(t, withMe), postStore(t, withMe)
					_, oldErr := mainAddComment(ctx, old, task, text, postNow)
					c := app.Comments{Store: now, Text: richtext.Converter{}, Clock: func() time.Time { return postNow }}
					_, newErr := c.Add(ctx, task, text)
					sameResult(t, oldErr, newErr, snapshot(t, old), snapshot(t, now))
				})
			}
		}
	}
}

func TestStatusCreateWritesWhatMainWrote(t *testing.T) {
	ctx := context.Background()
	type input struct{ project, status, title string }
	inputs := []input{
		{"新居プロジェクト", "on_track", "週報"},
		{"新居", "at_risk", ""},    // part of the name
		{"p1", "complete", "完了"}, // the gid of p1 and the name of p2
		{"p2", "on_hold", ""},
		{"旧居", "on_track", ""}, // no such project
		{"新居プロジェクト", "great", ""},
		{"新居プロジェクト", "", ""},
	}
	for _, withMe := range []bool{true, false} {
		for _, in := range inputs {
			for i, text := range postTexts {
				for _, here := range []bool{false, true} {
					t.Run(fmt.Sprintf("me=%v/%s/%s/%d/here=%v", withMe, in.project, in.status, i, here), func(t *testing.T) {
						old, now := postStore(t, withMe), postStore(t, withMe)
						s := app.Status{Store: now, Text: richtext.Converter{}, Clock: func() time.Time { return postNow }}
						var oldErr, newErr error
						ai := app.StatusInput{Project: in.project, Status: in.status, Title: in.title, Text: text}
						if here {
							oldErr = mainCreateStatusHere(ctx, old, in.project, in.status, in.title, text, postNow)
							_, newErr = s.CreateHere(ctx, ai)
						} else {
							oldErr = mainCreateStatus(ctx, old, in.project, in.status, in.title, text, postNow)
							_, newErr = s.Create(ctx, ai)
						}
						sameResult(t, oldErr, newErr, snapshot(t, old), snapshot(t, now))
					})
				}
			}
		}
	}
}

func sameResult(t *testing.T, oldErr, newErr error, oldSnap, newSnap string) {
	t.Helper()
	if fmt.Sprint(oldErr) != fmt.Sprint(newErr) || reflect.TypeOf(oldErr) != reflect.TypeOf(newErr) {
		t.Errorf("error %T %v, main had %T %v", newErr, newErr, oldErr, oldErr)
	}
	if oldSnap != newSnap {
		t.Errorf("store after the write:\n%s\nmain wrote:\n%s", newSnap, oldSnap)
	}
}
