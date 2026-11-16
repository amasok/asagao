package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asana-tui/internal/app"
	"github.com/amasok/asana-tui/internal/asana"
	"github.com/amasok/asana-tui/internal/richtext"
	"github.com/amasok/asana-tui/internal/store"
)

// legacyCreate is the body of the TUI's create closures as on main before a3 (copied).
func legacyCreate(ctx context.Context, st *store.Store, in app.CreateHereInput, now time.Time) (string, error) {
	nt := store.NewTask{Name: in.Name, DueOn: in.Due}
	var err error
	if in.Parent != "" {
		nt.ParentGID = in.Parent
		return st.CreateTaskLocal(ctx, nt, now)
	}
	if in.Project != "" {
		nt.ProjectGID, nt.SectionGID = in.Project, in.Section
		if nt.SectionGID == "" {
			if nt.SectionGID, err = st.FirstSection(ctx, in.Project); err != nil {
				return "", err
			}
		}
	} else {
		if nt.WorkspaceGID, err = st.DefaultWorkspace(ctx); err != nil {
			return "", err
		}
		if nt.AssigneeGID, err = st.MeGID(ctx); err != nil {
			return "", err
		}
	}
	return st.CreateTaskLocal(ctx, nt, now)
}

var attackLocalGID = regexp.MustCompile(`local[-_:][A-Za-z0-9-]+`)

func attackDump(t *testing.T, st *store.Store, gid string) string {
	t.Helper()
	ctx := context.Background()
	ops, err := st.ListOps(ctx)
	if err != nil {
		t.Fatal(err)
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
	return attackLocalGID.ReplaceAllString(string(b), "LOCAL")
}

func attackStore(t *testing.T, workspace, me bool) *store.Store {
	st := fixtureStore(t)
	ctx := context.Background()
	if workspace {
		if err := st.UpsertWorkspaces(ctx, []asana.Workspace{{GID: "w1", Name: "家"}}); err != nil {
			t.Fatal(err)
		}
	}
	if !me {
		if err := st.SetSyncState(ctx, store.KeyMeGID, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.UpsertProjects(ctx, "w1", []asana.Project{{GID: "pe", Name: "空", Members: []asana.Ref{{GID: "me"}}}}, "me"); err != nil {
		t.Fatal(err)
	}
	return st
}

// CreateHere gives the same error text and the same stored result as the TUI's code on
// main, for each combination of place, missing workspace/me, and odd names.
func TestCreateHereSameAsBeforeA3(t *testing.T) {
	now := time.Date(2026, 11, 13, 9, 30, 0, 0, time.UTC)
	names := []string{"牛乳", "  ", "\x1b[31mred"}
	places := map[string]app.CreateHereInput{
		"mytasks":         {},
		"project":         {Project: "p1"},
		"project+section": {Project: "p1", Section: "s2"},
		"emptyproject":    {Project: "pe"},
		"badproject":      {Project: "nope"},
		"subtask":         {Parent: "t1"},
		"badparent":       {Parent: "nope"},
		"subtask+project": {Parent: "t1", Project: "p1", Section: "s2"},
	}
	for place, base := range places {
		// a missing workspace or me only matters in My Tasks; elsewhere both are there
		cache := [][2]bool{{true, true}}
		if place == "mytasks" {
			cache = [][2]bool{{true, true}, {true, false}, {false, true}, {false, false}}
		}
		for _, c := range cache {
			ws, me := c[0], c[1]
			for _, name := range names {
				in := base
				in.Name, in.Due = name, "2026-11-14"
				label := fmt.Sprintf("%s ws=%v me=%v name=%q", place, ws, me, name)
				t.Run(label, func(t *testing.T) {
					a, b := attackStore(t, ws, me), attackStore(t, ws, me)
					gw, ew := legacyCreate(context.Background(), a, in, now)
					tasks := &app.Tasks{Store: b, Text: richtext.Converter{}, Clock: func() time.Time { return now }}
					gn, en := tasks.CreateHere(context.Background(), in)
					if fmt.Sprint(ew) != fmt.Sprint(en) || (ew == nil) != (en == nil) {
						t.Fatalf("error %v vs %v", ew, en)
					}
					if ew != nil {
						gw, gn = "", ""
					}
					if dw, dn := attackDump(t, a, gw), attackDump(t, b, gn); dw != dn {
						t.Errorf("\nmain: %s\na3:   %s", dw, dn)
					}
				})
			}
		}
	}
}

// The created_at of the op is the Now() read when Enter is pressed, not when the cmd runs.
func TestCreateTaskUsesTimeOfKeyPress(t *testing.T) {
	st := fixtureStore(t)
	if err := st.UpsertWorkspaces(context.Background(), []asana.Workspace{{GID: "w1", Name: "家"}}); err != nil {
		t.Fatal(err)
	}
	cur := testNow
	m := New(context.Background(), Deps{Store: st, Now: func() time.Time { return cur }})
	h := &harness{t: t, m: m}
	h.send(tea.WindowSizeMsg{Width: 140, Height: 30})
	h.run(m.Init())
	h.key("a")
	h.typeText("時刻")
	cur = testNow.Add(3 * time.Hour)
	want := cur
	cmd := h.m.createTask("時刻")
	cur = testNow.Add(9 * time.Hour) // clock moves before the cmd runs
	_ = cmd()
	ops, err := st.ListOps(context.Background())
	if err != nil || len(ops) != 1 {
		t.Fatalf("%v %+v", err, ops)
	}
	if !strings.Contains(ops[0].CreatedAt, want.UTC().Format("2006-01-02T15:04")) && !strings.Contains(ops[0].CreatedAt, want.Format("2006-01-02T15:04")) {
		t.Fatalf("created_at = %q, want key-press time %v", ops[0].CreatedAt, want)
	}
}
