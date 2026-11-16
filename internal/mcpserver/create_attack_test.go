package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"testing"
	"time"

	"github.com/amasok/asana-tui/internal/dates"
	"github.com/amasok/asana-tui/internal/richtext"
	"github.com/amasok/asana-tui/internal/store"
)

// legacyCreate is the create_task body as it was on main before a1 (copied verbatim,
// minus the push).
func legacyCreate(ctx context.Context, st *store.Store, now time.Time, in CreateTaskInput) (string, error) {
	nt := store.NewTask{Name: in.Name, ParentGID: in.Parent}
	if in.Notes != "" {
		nt.Notes, nt.HTMLNotes = richtext.TaskNotes.PlainFromMarkdown(in.Notes), richtext.TaskNotes.FromMarkdown(in.Notes)
	}
	var err error
	if nt.DueOn, err = dates.Parse(in.DueOn, now); err != nil {
		return "", fmt.Errorf("due_on: %w", err)
	}
	assignee := in.Assignee
	if in.Parent != "" {
	} else if in.Project != "" {
		if nt.ProjectGID, err = st.ResolveProject(ctx, in.Project); err != nil {
			return "", err
		}
		if in.Section != "" {
			nt.SectionGID, err = st.ResolveSection(ctx, nt.ProjectGID, in.Section)
		} else {
			nt.SectionGID, err = st.FirstSection(ctx, nt.ProjectGID)
		}
		if err != nil {
			return "", err
		}
	} else {
		if nt.WorkspaceGID, err = st.DefaultWorkspace(ctx); err != nil {
			return "", err
		}
		if assignee == "" {
			assignee = "me"
		}
	}
	if nt.AssigneeGID, err = st.ResolveUser(ctx, assignee); err != nil {
		return "", err
	}
	tags, err := st.ResolveTagEdit(ctx, in.Tags, nil)
	if err != nil {
		return "", err
	}
	gid, err := st.CreateTaskLocal(ctx, nt, now)
	if err != nil {
		return "", err
	}
	if err := st.ApplyTagEdit(ctx, gid, tags, now); err != nil {
		return "", err
	}
	return gid, nil
}

var localGIDRe = regexp.MustCompile(`local[-_:][A-Za-z0-9-]+`)

func dumpAll(t *testing.T, st *store.Store) string {
	t.Helper()
	ctx := context.Background()
	ops, err := st.ListOps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tasks := map[string]any{}
	for i := range ops {
		ops[i].CreatedAt = ""
		if g := ops[i].TaskGID; g != "" {
			if d, err := st.GetTask(ctx, g); err == nil {
				tasks[localGIDRe.ReplaceAllString(g, "LOCAL")] = d
			}
		}
	}
	b, _ := json.Marshal(map[string]any{"ops": ops, "tasks": tasks})
	return localGIDRe.ReplaceAllString(string(b), "LOCAL")
}

func TestCreateTaskSameAsBeforeA1(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]any
	}{
		{"plain", map[string]any{"name": "a"}},
		{"due", map[string]any{"name": "a", "due_on": "+7d"}},
		{"bad due", map[string]any{"name": "a", "due_on": "someday"}},
		{"bad due and bad assignee", map[string]any{"name": "a", "due_on": "x", "assignee": "誰か"}},
		{"bad due and bad project", map[string]any{"name": "a", "due_on": "x", "project": "ない"}},
		{"bad assignee", map[string]any{"name": "a", "assignee": "誰か"}},
		{"bad assignee with tags", map[string]any{"name": "a", "assignee": "誰か", "tags": []string{"新タグ"}}},
		{"none", map[string]any{"name": "a", "assignee": "none"}},
		{"project", map[string]any{"name": "a", "project": "新居"}},
		{"project section", map[string]any{"name": "a", "project": "新居", "section": "手続き"}},
		{"project bad section", map[string]any{"name": "a", "project": "新居", "section": "ない"}},
		{"bad project", map[string]any{"name": "a", "project": "ない"}},
		{"section alone", map[string]any{"name": "a", "section": "家具"}},
		{"subtask", map[string]any{"name": "a", "parent": "t1"}},
		{"subtask ignores project", map[string]any{"name": "a", "parent": "t1", "project": "ない", "section": "ない"}},
		{"subtask assignee", map[string]any{"name": "a", "parent": "t1", "assignee": "誰か"}},
		{"subtask default assignee", map[string]any{"name": "a", "parent": "t1"}},
		{"tags", map[string]any{"name": "a", "tags": []string{"新タグ", "#もう一つ"}}},
		{"empty tag", map[string]any{"name": "a", "tags": []string{"#"}}},
		{"empty tag among good", map[string]any{"name": "a", "tags": []string{"ok", " "}}},
		{"tags and project", map[string]any{"name": "a", "project": "新居", "tags": []string{"x"}}},
		{"notes", map[string]any{"name": "a", "notes": "*強調* `c` <b>x</b>\n\n- 項目\n# 見出し"}},
		{"control chars", map[string]any{"name": "a\x1b[31m", "notes": "\x1b]0;t\x07", "tags": []string{"t\x1b[0m"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			a, b := seed(t), seed(t)
			var in CreateTaskInput
			raw, _ := json.Marshal(tc.in)
			if err := json.Unmarshal(raw, &in); err != nil {
				t.Fatal(err)
			}
			_, ea := legacyCreate(ctx, a, testNow, in)
			cs := connect(t, &Server{Store: b})
			res := call(t, cs, "create_task", tc.in, nil)
			if (ea == nil) == res.IsError {
				t.Fatalf("legacy err = %v, new IsError = %v (%s)", ea, res.IsError, toolError(res))
			}
			if ea != nil && toolError(res) != ea.Error() {
				t.Fatalf("error text differs:\n legacy %q\n new    %q", ea, toolError(res))
			}
			if da, db := dumpAll(t, a), dumpAll(t, b); da != db {
				t.Fatalf("stored result differs:\n legacy %s\n new    %s", da, db)
			}
		})
	}
}

// Tags that do not resolve write nothing, and the same time is used for every write.
func TestCreateTaskUsesOneTimestamp(t *testing.T) {
	st := seed(t)
	calls := 0
	s := &Server{Store: st}
	cs := connect(t, s)
	inner := s.Now
	s.Now = func() time.Time { calls++; return inner() }
	call(t, cs, "create_task", map[string]any{"name": "a", "due_on": "tomorrow", "tags": []string{"x"}}, nil)
	if calls != 1 {
		t.Fatalf("Now read %d times, want 1", calls)
	}
}
