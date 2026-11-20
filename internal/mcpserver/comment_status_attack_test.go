package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/wire"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/richtext"
	"github.com/amasok/asagao/internal/store"
)

// legacyAddComment and legacyCreateStatus are the add_comment and create_status_update
// bodies as they were on main before a4 (copied verbatim, minus the push).
func legacyAddComment(ctx context.Context, st *store.Store, now time.Time, in AddCommentInput) error {
	me, err := st.Me(ctx)
	if err != nil {
		return err
	}
	_, err = st.AddRichCommentLocal(ctx, in.GID, richtext.Comment.PlainFromMarkdown(in.Text), richtext.Comment.FromMarkdown(in.Text), me, now)
	return err
}

func legacyCreateStatus(ctx context.Context, st *store.Store, now time.Time, in CreateStatusInput) error {
	gid, err := st.ResolveProject(ctx, in.Project)
	if err != nil {
		return err
	}
	me, err := st.Me(ctx)
	if err != nil {
		return err
	}
	_, err = st.CreateStatusLocal(ctx, gid, in.Status, in.Title, richtext.StatusUpdate.PlainFromMarkdown(in.Text), richtext.StatusUpdate.FromMarkdown(in.Text), me, now)
	return err
}

// dumpCommentStatus is everything a comment or a status update can change.
func dumpCommentStatus(t *testing.T, st *store.Store, task, project string) string {
	t.Helper()
	ctx := context.Background()
	ops, err := st.ListOps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := range ops {
		ops[i].CreatedAt = ""
	}
	out := map[string]any{"ops": ops}
	if d, err := st.GetTask(ctx, task); err == nil {
		out["task"] = d
	}
	if project != "" {
		if d, err := st.StatusUpdates(ctx, project); err == nil {
			out["project"] = d
		}
	}
	b, _ := json.Marshal(out)
	return localGIDRe.ReplaceAllString(string(b), "LOCAL")
}

func TestAddCommentAndStatusSameAsBeforeA4(t *testing.T) {
	type tc struct {
		name string
		tool string
		in   map[string]any
	}
	cases := []tc{
		{"comment", "add_comment", map[string]any{"gid": "t1", "text": "*強調* `c` <b>x</b>\n\n- 項目"}},
		{"comment heading rule image", "add_comment", map[string]any{"gid": "t1", "text": "# 予定\n\n---\n\n![図](asana:42)"}},
		{"comment empty", "add_comment", map[string]any{"gid": "t1", "text": ""}},
		{"comment blank", "add_comment", map[string]any{"gid": "t1", "text": " \n\t "}},
		{"comment markup only", "add_comment", map[string]any{"gid": "t1", "text": "<br>"}},
		{"comment unknown task", "add_comment", map[string]any{"gid": "nope", "text": "x"}},
		{"comment empty gid", "add_comment", map[string]any{"gid": "", "text": "x"}},
		{"comment unknown task and empty", "add_comment", map[string]any{"gid": "nope", "text": ""}},
		{"comment escapes", "add_comment", map[string]any{"gid": "t1", "text": "\x1b]0;t\x07 a\x1b[31m"}},
		{"status", "create_status_update", map[string]any{"project": "新居", "status": "at_risk", "title": "遅れ", "text": "**家具**が遅れる\n\n- a"}},
		{"status by gid", "create_status_update", map[string]any{"project": "p1", "status": "complete", "text": "x"}},
		{"status layout", "create_status_update", map[string]any{"project": "p1", "status": "on_track", "text": "# 予定\n\n---\n\n![図](asana:42)"}},
		{"status bad type", "create_status_update", map[string]any{"project": "p1", "status": "fine", "text": "x"}},
		{"status empty type", "create_status_update", map[string]any{"project": "p1", "status": "", "text": "x"}},
		{"status empty text", "create_status_update", map[string]any{"project": "p1", "status": "on_track", "text": ""}},
		{"status bad type and empty text", "create_status_update", map[string]any{"project": "p1", "status": "x", "text": ""}},
		{"status bad project", "create_status_update", map[string]any{"project": "ない", "status": "bad", "text": ""}},
		{"status empty project", "create_status_update", map[string]any{"project": "", "status": "on_track", "text": "x"}},
		{"status escapes", "create_status_update", map[string]any{"project": "p1", "status": "on_hold", "title": "t\x1b[0m", "text": "\x1b]0;t\x07"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			a, b := seed(t), seed(t)
			raw, _ := json.Marshal(c.in)
			var ea error
			if c.tool == "add_comment" {
				var in AddCommentInput
				_ = json.Unmarshal(raw, &in)
				ea = legacyAddComment(ctx, a, testNow, in)
			} else {
				var in CreateStatusInput
				_ = json.Unmarshal(raw, &in)
				ea = legacyCreateStatus(ctx, a, testNow, in)
			}
			cs := connect(t, &Server{App: wire.UseCases(b)})
			res := call(t, cs, c.tool, c.in, nil)
			if (ea == nil) == res.IsError {
				t.Fatalf("legacy err = %v, new IsError = %v (%s)", ea, res.IsError, toolError(res))
			}
			if ea != nil && toolError(res) != ea.Error() {
				t.Fatalf("error text differs:\n legacy %q\n new    %q", ea, toolError(res))
			}
			if da, db := dumpCommentStatus(t, a, "t1", "p1"), dumpCommentStatus(t, b, "t1", "p1"); da != db {
				t.Fatalf("stored result differs:\n legacy %s\n new    %s", da, db)
			}
		})
	}
}

// One Now() read per write, and it is the time stored.
func TestAddCommentAndStatusReadTheClockOnce(t *testing.T) {
	for _, tool := range []struct {
		name string
		in   map[string]any
	}{
		{"add_comment", map[string]any{"gid": "t1", "text": "x"}},
		{"create_status_update", map[string]any{"project": "p1", "status": "on_track", "text": "x"}},
	} {
		st := seed(t)
		calls := 0
		s := &Server{App: wire.UseCases(st)}
		cs := connect(t, s)
		inner := s.Now
		s.Now = func() time.Time { calls++; return inner() }
		call(t, cs, tool.name, tool.in, nil)
		if calls != 1 {
			t.Errorf("%s: Now read %d times, want 1", tool.name, calls)
		}
	}
}

// A cancelled context and an ambiguous project name end the same way as on main.
func TestAddCommentAndStatusOddStatesSameAsBeforeA4(t *testing.T) {
	two := func(t *testing.T) *store.Store {
		st := seed(t)
		if err := st.UpsertProjects(context.Background(), "w1", []asana.Project{
			{GID: "p2", Name: "新居の片付け", Members: []asana.Ref{{GID: "me"}}},
			{GID: "p3", Name: "新居の掃除", Members: []asana.Ref{{GID: "me"}}},
		}, "me"); err != nil {
			t.Fatal(err)
		}
		return st
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, c := range []struct {
		name    string
		ctx     context.Context
		project string
	}{
		{"ambiguous", context.Background(), "新居の"},
		{"cancelled", cancelled, "p1"},
		{"cancelled, ambiguous", cancelled, "新居の"},
	} {
		t.Run(c.name, func(t *testing.T) {
			a, b := two(t), two(t)
			sa := legacyCreateStatus(c.ctx, a, testNow, CreateStatusInput{Project: c.project, Status: "on_track", Text: "x"})
			_, _, sb := (&Server{App: wire.UseCases(b), Now: func() time.Time { return testNow }}).createStatus(c.ctx, nil, CreateStatusInput{Project: c.project, Status: "on_track", Text: "x"})
			ca := legacyAddComment(c.ctx, a, testNow, AddCommentInput{GID: "t1", Text: "x"})
			_, _, cb := (&Server{App: wire.UseCases(b), Now: func() time.Time { return testNow }}).addComment(c.ctx, nil, AddCommentInput{GID: "t1", Text: "x"})
			for _, p := range [][2]error{{sa, sb}, {ca, cb}} {
				if (p[0] == nil) != (p[1] == nil) || (p[0] != nil && p[0].Error() != p[1].Error()) {
					t.Fatalf("legacy %v, new %v", p[0], p[1])
				}
				if p[0] != nil && errors.Is(p[0], store.ErrAmbiguous) != errors.Is(p[1], store.ErrAmbiguous) {
					t.Fatalf("errors.Is(ErrAmbiguous) differs")
				}
			}
			if da, db := dumpCommentStatus(t, a, "t1", "p1"), dumpCommentStatus(t, b, "t1", "p1"); da != db {
				t.Fatalf("stored result differs:\n legacy %s\n new    %s", da, db)
			}
		})
	}
}
