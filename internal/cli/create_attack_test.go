package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"testing"
	"time"

	usecase "github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/richtext"
	"github.com/amasok/asagao/internal/store"
)

// legacyCreateTask is the CLI's createTask as it was on main before a1 (copied verbatim),
// so the new one can be compared with it on the same input.
func legacyCreateTask(ctx context.Context, st *store.Store, o createOpts, now time.Time) (string, error) {
	nt := store.NewTask{Name: o.name, ParentGID: o.parent}
	if o.notes != "" {
		nt.Notes, nt.HTMLNotes = richtext.TaskNotes.PlainFromMarkdown(o.notes), richtext.TaskNotes.FromMarkdown(o.notes)
	}
	var err error
	if nt.DueOn, err = usecase.ParseDue(o.due, now); err != nil {
		return "", fmt.Errorf("--due: %w", err)
	}
	if o.parent != "" {
	} else if o.project != "" {
		if nt.ProjectGID, err = st.ResolveProject(ctx, o.project); err != nil {
			return "", err
		}
		if o.section != "" {
			nt.SectionGID, err = st.ResolveSection(ctx, nt.ProjectGID, o.section)
		} else {
			nt.SectionGID, err = st.FirstSection(ctx, nt.ProjectGID)
		}
		if err != nil {
			return "", err
		}
	} else if nt.WorkspaceGID, err = st.DefaultWorkspace(ctx); err != nil {
		return "", err
	}
	assignee := o.assignee
	if o.project == "" && o.parent == "" && assignee == "" {
		assignee = "me"
	}
	if nt.AssigneeGID, err = st.ResolveUser(ctx, assignee); err != nil {
		return "", fmt.Errorf("--assignee: %w", err)
	}
	return st.CreateTaskLocal(ctx, nt, now)
}

var localGID = regexp.MustCompile(`local[-_:][A-Za-z0-9-]+`)

// dump is everything a create can change, with the generated gids made comparable.
func dump(t *testing.T, st *store.Store, gid string) string {
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
	if gid != "" {
		d, err := st.GetTask(ctx, gid)
		if err != nil {
			t.Fatal(err)
		}
		out["task"] = d
	}
	b, _ := json.Marshal(out)
	return localGID.ReplaceAllString(string(b), "LOCAL")
}

// The new createTask and the one on main give the same error text, the same error
// identity and the same stored result for each input.
func TestCreateTaskSameAsBeforeA1(t *testing.T) {
	now := time.Date(2026, 11, 13, 10, 0, 0, 0, time.Local)
	st0 := createStore(t)
	// a parent to make subtasks of
	parent, err := st0.CreateTaskLocal(context.Background(), store.NewTask{Name: "親", WorkspaceGID: "w1"}, now)
	if err != nil {
		t.Fatal(err)
	}
	_ = parent
	cases := []createOpts{
		{name: "a"},
		{name: "a", due: "tomorrow"},
		{name: "a", due: "+7d"},
		{name: "a", due: "2026-02-30"},
		{name: "a", due: "someday"},
		{name: "a", due: "someday", assignee: "誰か"},
		{name: "a", due: "someday", project: "ない"},
		{name: "a", assignee: "誰か"},
		{name: "a", assignee: "none"},
		{name: "a", assignee: "part"},
		{name: "a", assignee: "u2", project: "新居"},
		{name: "a", assignee: "誰か", project: "新居"},
		{name: "a", assignee: "誰か", project: "ない"},
		{name: "a", project: "新居"},
		{name: "a", project: "新居", section: "家具"},
		{name: "a", project: "新居", section: "ない"},
		{name: "a", project: "ない", section: "家具"},
		{name: "a", section: "家具"},
		{name: "a", notes: "*強調* と `code` <b>x</b>\n\n- 項目"},
		{name: "a", notes: "# 見出し\n[リンク](https://example.com)"},
		{name: "", project: "新居"},
		{name: "a", parent: "PARENT"},
		{name: "a", parent: "PARENT", project: "新居", section: "ない"},
		{name: "a", parent: "PARENT", assignee: "誰か"},
		{name: "a", parent: "PARENT", due: "x"},
		{name: "a", parent: "nonexistent"},
		{name: "a\x1b[31m赤", project: "新居", notes: "\x1b]0;t\x07"},
	}
	for i, o := range cases {
		t.Run(fmt.Sprintf("%d", i), func(t *testing.T) {
			ctx := context.Background()
			a, b := createStore(t), createStore(t)
			pa, _ := a.CreateTaskLocal(ctx, store.NewTask{Name: "親", WorkspaceGID: "w1"}, now)
			pb, _ := b.CreateTaskLocal(ctx, store.NewTask{Name: "親", WorkspaceGID: "w1"}, now)
			oa, ob := o, o
			if o.parent == "PARENT" {
				oa.parent, ob.parent = pa, pb
			}
			ga, ea := legacyCreateTask(ctx, a, oa, now)
			gb, eb := createTask(ctx, localFor(b), ob, now)
			if (ea == nil) != (eb == nil) {
				t.Fatalf("legacy err = %v, new err = %v", ea, eb)
			}
			if ea != nil {
				if ea.Error() != eb.Error() {
					t.Fatalf("error text differs:\n legacy %q\n new    %q", ea, eb)
				}
				for _, target := range []error{store.ErrNotFound} {
					if errors.Is(ea, target) != errors.Is(eb, target) {
						t.Fatalf("errors.Is(%v) differs", target)
					}
				}
				if gb != "" || ga != "" {
					t.Fatalf("gid returned with an error: %q %q", ga, gb)
				}
			}
			if da, db := dump(t, a, ga), dump(t, b, gb); da != db {
				t.Fatalf("stored result differs:\n legacy %s\n new    %s", da, db)
			}
		})
	}
}
