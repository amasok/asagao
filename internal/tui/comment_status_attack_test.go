package tui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/richtext"
	"github.com/amasok/asagao/internal/store"
)

// The comment and status-update closures as on main before a4 (copied verbatim).
func legacyComment(ctx context.Context, st *store.Store, gid, text string, now time.Time) writeDoneMsg {
	me, err := st.Me(ctx)
	if err != nil {
		return writeDoneMsg{err: err}
	}
	if _, err := st.AddRichCommentLocal(ctx, gid, richtext.Comment.PlainFromMarkdown(text), richtext.Comment.FromMarkdown(text), me, now); err != nil {
		return writeDoneMsg{err: err}
	}
	return writeDoneMsg{status: "コメントしました"}
}

func legacyStatus(ctx context.Context, st *store.Store, project, status, title, body string, now time.Time) writeDoneMsg {
	me, err := st.Me(ctx)
	if err != nil {
		return writeDoneMsg{err: err}
	}
	if _, err := st.CreateStatusLocal(ctx, project, status, title, richtext.StatusUpdate.PlainFromMarkdown(body), richtext.StatusUpdate.FromMarkdown(body), me, now); err != nil {
		return writeDoneMsg{err: err}
	}
	return writeDoneMsg{status: "ステータスを投稿しました（" + store.StatusLabel(status) + "）"}
}

var commentLocalGID = regexp.MustCompile(`local[-_:][A-Za-z0-9-]+`)

func dumpCS(t *testing.T, st *store.Store) string {
	t.Helper()
	ctx := context.Background()
	ops, err := st.ListOps(ctx)
	if err != nil {
		t.Fatal(err)
	}
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
	return commentLocalGID.ReplaceAllString(string(b), "LOCAL")
}

func TestCommentAndStatusSameAsBeforeA4(t *testing.T) {
	md := "# 予定\n\n---\n\n![図](asana:42)"
	type tc struct {
		name         string
		kind         string // line, editor, status
		gid, project string
		text         string // comment text or status body
		status, ttl  string
		noMe         bool
	}
	cases := []tc{
		{name: "line", kind: "line", gid: "t1", text: "*強調* `c` <b>x</b>"},
		{name: "line layout", kind: "line", gid: "t1", text: md},
		{name: "line blank", kind: "line", gid: "t1", text: "  "},
		{name: "line markup only", kind: "line", gid: "t1", text: "<br>"},
		{name: "line unknown task", kind: "line", gid: "nope", text: "x"},
		{name: "line escapes", kind: "line", gid: "t1", text: "\x1b]0;t\x07a\x1b[31m"},
		{name: "line no me", kind: "line", gid: "t1", text: "x", noMe: true},
		{name: "editor", kind: "editor", gid: "t2", text: "**明日**見に行く\n\n- a"},
		{name: "editor layout", kind: "editor", gid: "t2", text: md},
		{name: "editor unknown task", kind: "editor", gid: "nope", text: "x"},
		{name: "status", kind: "status", project: "p1", status: "at_risk", ttl: "遅れ", text: "**家具**が遅れる\n\n- a"},
		{name: "status layout", kind: "status", project: "p1", status: "on_track", ttl: "週報", text: md},
		{name: "status bad type", kind: "status", project: "p1", status: "fine", text: "x"},
		{name: "status unknown project", kind: "status", project: "nope", status: "on_track", text: "x"},
		{name: "status markup only", kind: "status", project: "p1", status: "complete", text: "<br>"},
		{name: "status escapes", kind: "status", project: "p1", status: "on_hold", ttl: "t\x1b[0m", text: "\x1b]0;t\x07"},
		{name: "status no me", kind: "status", project: "p1", status: "on_track", text: "x", noMe: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			a, b := fixtureStore(t), fixtureStore(t)
			if c.noMe {
				for _, st := range []*store.Store{a, b} {
					if err := st.SetSyncState(ctx, store.KeyMeGID, ""); err != nil {
						t.Fatal(err)
					}
				}
			}
			now := testNow
			m := New(ctx, Deps{Store: b, Now: func() time.Time { return now }})
			dir := t.TempDir()
			write := func(s string) string {
				p := filepath.Join(dir, "doc.md")
				if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
					t.Fatal(err)
				}
				return p
			}
			var want writeDoneMsg
			var cmd func() any
			switch c.kind {
			case "line":
				want = legacyComment(ctx, a, c.gid, c.text, now)
				cmd = func() any { return m.addComment(c.gid, c.text)() }
			case "editor":
				want = legacyComment(ctx, a, c.gid, c.text, now)
				p := write(commentTemplate + c.text)
				cmd = func() any { return m.applyComment(commentDoneMsg{gid: c.gid, path: p})() }
			case "status":
				want = legacyStatus(ctx, a, c.project, c.status, c.ttl, c.text, now)
				p := write("---\nstatus: " + c.status + "\ntitle: " + c.ttl + "\n---\n\n" + c.text)
				cmd = func() any { return m.applyStatus(statusEditDoneMsg{project: c.project, path: p})() }
			}
			got, ok := cmd().(writeDoneMsg)
			if !ok {
				t.Fatalf("not a writeDoneMsg: %T", got)
			}
			if got.status != want.status || (got.err == nil) != (want.err == nil) || (got.err != nil && got.err.Error() != want.err.Error()) {
				t.Fatalf("result differs:\n main %+v\n new  %+v", want, got)
			}
			if da, db := dumpCS(t, a), dumpCS(t, b); da != db {
				t.Fatalf("stored result differs:\n main %s\n new  %s", da, db)
			}
		})
	}
}

// The time stored is the Now() read when the key is pressed, not when the cmd runs, and
// it is read once.
func TestCommentAndStatusUseTimeOfKeyPress(t *testing.T) {
	ctx := context.Background()
	st := fixtureStore(t)
	cur := testNow
	reads := 0
	m := New(ctx, Deps{Store: st, Now: func() time.Time { reads++; return cur }})
	dir := t.TempDir()
	p1 := filepath.Join(dir, "c.md")
	p2 := filepath.Join(dir, "s.md")
	if err := os.WriteFile(p1, []byte(commentTemplate+"b"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p2, []byte("---\nstatus: on_track\n---\n\nc"), 0o600); err != nil {
		t.Fatal(err)
	}
	cur = testNow.Add(3 * time.Hour)
	want := cur
	reads = 0
	c1 := m.addComment("t1", "a")
	c2 := m.applyComment(commentDoneMsg{gid: "t1", path: p1})
	c3 := m.applyStatus(statusEditDoneMsg{project: "p1", path: p2})
	if reads != 3 {
		t.Fatalf("Now read %d times while building the cmds, want 3", reads)
	}
	cur = testNow.Add(9 * time.Hour)
	for _, c := range []func() any{func() any { return c1() }, func() any { return c2() }, func() any { return c3() }} {
		if msg := c().(writeDoneMsg); msg.err != nil {
			t.Fatal(msg.err)
		}
	}
	if reads != 3 {
		t.Fatalf("Now read %d times in total, want 3 (not again when the cmds run)", reads)
	}
	ops, err := st.ListOps(ctx)
	if err != nil || len(ops) != 3 {
		t.Fatalf("%v %+v", err, ops)
	}
	for _, op := range ops {
		if !regexp.MustCompile(want.UTC().Format("2006-01-02T15:04")).MatchString(op.CreatedAt) &&
			!regexp.MustCompile(want.Format("2006-01-02T15:04")).MatchString(op.CreatedAt) {
			t.Errorf("%s created_at = %q, want key-press time %v", op.Kind, op.CreatedAt, want)
		}
	}
}
