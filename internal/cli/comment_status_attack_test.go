package cli

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asana-tui/internal/richtext"
	"github.com/amasok/asana-tui/internal/store"
)

// legacyComment and legacyStatus are the bodies of `comment add` and `project status post`
// as they were on main before a4 (copied verbatim, minus the push report).
func legacyComment(ctx context.Context, st *store.Store, args []string) error {
	me, err := st.Me(ctx)
	if err != nil {
		return err
	}
	text := strings.Join(args[1:], " ")
	_, err = st.AddRichCommentLocal(ctx, args[0], richtext.Comment.PlainFromMarkdown(text), richtext.Comment.FromMarkdown(text), me, time.Now())
	return err
}

func legacyStatus(ctx context.Context, st *store.Store, project, status, title, text string) error {
	gid, err := st.ResolveProject(ctx, project)
	if err != nil {
		return err
	}
	me, err := st.Me(ctx)
	if err != nil {
		return err
	}
	_, err = st.CreateStatusLocal(ctx, gid, status, title, richtext.StatusUpdate.PlainFromMarkdown(text), richtext.StatusUpdate.FromMarkdown(text), me, time.Now())
	return err
}

var (
	cliTimeRe = regexp.MustCompile(`\d{4}-\d\d-\d\dT[0-9:.]+(Z|[+-]\d\d:\d\d)`)
	cliURLRe  = regexp.MustCompile(`127\.0\.0\.1:\d+`)
)

func normalizeCLI(s string) string {
	s = localGID.ReplaceAllString(s, "LOCAL")
	s = cliTimeRe.ReplaceAllString(s, "TIME")
	return cliURLRe.ReplaceAllString(s, "HOST")
}

func dumpCommentStatus(t *testing.T, path string) string {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ops, _ := st.ListOps(ctx)
	out := map[string]any{"ops": ops}
	for _, g := range []string{"t1", "t2"} {
		if d, err := st.GetTask(ctx, g); err == nil {
			out[g] = d
		}
	}
	if d, err := st.StatusUpdates(ctx, "p1"); err == nil {
		out["statuses"] = d
	}
	b, _ := json.Marshal(out)
	return normalizeCLI(string(b))
}

func TestCommentAndStatusPostSameAsBeforeA4(t *testing.T) {
	type tc struct {
		name   string
		args   []string // CLI arguments
		legacy func(ctx context.Context, st *store.Store) error
		noMe   bool
	}
	cmt := func(args ...string) tc {
		return tc{args: append([]string{"comment", "add"}, args...),
			legacy: func(ctx context.Context, st *store.Store) error { return legacyComment(ctx, st, args) }}
	}
	post := func(project, status, title, text string) tc {
		a := []string{"project", "status", "post", project, "--status", status, "--title", title, "--text", text}
		return tc{args: a, legacy: func(ctx context.Context, st *store.Store) error {
			return legacyStatus(ctx, st, project, status, title, text)
		}}
	}
	cases := map[string]tc{
		"comment":                cmt("t1", "*強調*", "`c`", "<b>x</b>"),
		"comment multiword":      cmt("t1", "a", "b  c"),
		"comment layout":         cmt("t1", "# 予定\n\n---\n\n![図](asana:42)"),
		"comment blank":          cmt("t1", " ", "\t"),
		"comment markup only":    cmt("t1", "<br>"),
		"comment unknown task":   cmt("nope", "x"),
		"comment unknown, blank": cmt("nope", ""),
		"comment escapes":        cmt("t2", "\x1b]0;t\x07", "a\x1b[31m"),
		"status":                 post("新居", "at_risk", "遅れ", "**家具**が遅れる\n\n- a"),
		"status by gid default":  post("p1", "on_track", "", "x"),
		"status layout":          post("p1", "complete", "週報", "# 予定\n\n---\n\n![図](asana:42)"),
		"status bad type":        post("p1", "fine", "", "x"),
		"status bad type, empty": post("p1", "fine", "", ""),
		"status empty text":      post("p1", "on_track", "", ""),
		"status bad project":     post("ない", "fine", "", ""),
		"status escapes":         post("p1", "on_hold", "t\x1b[0m", "\x1b]0;t\x07"),
	}
	noMe := cmt("t1", "x")
	noMe.noMe = true
	cases["comment no me"] = noMe
	noMeS := post("p1", "on_track", "", "x")
	noMeS.noMe = true
	cases["status no me"] = noMeS

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			clearMe := func(path string) {
				if !c.noMe {
					return
				}
				st, err := store.Open(ctx, path)
				if err != nil {
					t.Fatal(err)
				}
				defer st.Close()
				if err := st.SetSyncState(ctx, store.KeyMeGID, ""); err != nil {
					t.Fatal(err)
				}
			}
			pa := writeCLIEnv(t)
			clearMe(pa)
			st, err := store.Open(ctx, pa)
			if err != nil {
				t.Fatal(err)
			}
			ea := c.legacy(ctx, st)
			st.Close()

			pb := writeCLIEnv(t) // switches the data dir to a fresh one
			clearMe(pb)
			out, eb := runCLI(t, c.args...)
			if (ea == nil) != (eb == nil) {
				t.Fatalf("legacy err = %v, new err = %v\n%s", ea, eb, out)
			}
			if ea != nil && ea.Error() != eb.Error() {
				t.Fatalf("error text differs:\n legacy %q\n new    %q", ea, eb)
			}
			if ea == nil && !strings.HasPrefix(out, map[bool]string{true: "コメントしました", false: "ステータスを投稿しました"}[c.args[0] == "comment"]+"（オフラインのため未送信") {
				t.Fatalf("output = %q", out)
			}
			if da, db := dumpCommentStatus(t, pa), dumpCommentStatus(t, pb); da != db {
				t.Fatalf("stored result differs:\n legacy %s\n new    %s", da, db)
			}
		})
	}
}

// --json prints the new comment's gid (local) and the gid of the task, nothing else new.
func TestCommentAddJSONShape(t *testing.T) {
	writeCLIEnv(t)
	out, err := runCLI(t, "--json", "comment", "add", "t1", "x")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("%v: %q", err, out)
	}
	if m["task_gid"] != "t1" || !strings.HasPrefix(m["comment_gid"].(string), store.LocalPrefix) {
		t.Fatalf("json = %v", m)
	}
}
