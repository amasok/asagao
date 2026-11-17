package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/amasok/asagao/internal/asana"
)

func editInEditor(t *testing.T, change func(string) string) (*harness, func() []string, func() (string, string)) {
	t.Helper()
	st := fixtureStore(t)
	ctx := context.Background()
	_ = st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "ソファを買う", DueOn: "2026-11-20",
		Assignee:    &asana.Ref{GID: "me", Name: "Me"},
		Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}, Section: &asana.Ref{GID: "s1"}}}}})
	m := New(ctx, Deps{Store: st, Now: func() time.Time { return testNow }, RunEditor: fakeEditor(t, change)})
	h := &harness{t: t, m: m}
	h.send(tea.WindowSizeMsg{Width: 140, Height: 30})
	h.run(m.Init())
	h.key("h", "j", "j", "l", "e")
	ops := func() []string { return opList(t, st) }
	state := func() (string, string) {
		d, _ := st.GetTask(ctx, "t1")
		return d.Name, d.Memberships[0].SectionName
	}
	return h, ops, state
}

// A section that does not exist, or a due date that does not read, in the same edit as
// other changes: the error says which and nothing at all is written.
func TestEditorUnresolvedSectionOrDueWritesNothing(t *testing.T) {
	for _, tc := range []struct{ name, line, prefix string }{
		{"section", "section: 家具|section: ない場所", "section: "},
		{"due", "due: 2026-11-20|due: いつか", "due: "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			from, to, _ := strings.Cut(tc.line, "|")
			h, ops, state := editInEditor(t, func(s string) string {
				s = strings.Replace(s, "name: ソファを買う", "name: 変えた", 1)
				if tc.name == "due" {
					s = strings.Replace(s, "section: 家具", "section: 手続き", 1)
				}
				return strings.Replace(s, from, to, 1)
			})
			if got := ops(); len(got) != 0 {
				t.Errorf("queued %v", got)
			}
			if name, sec := state(); name != "ソファを買う" || sec != "家具" {
				t.Errorf("task changed: %q in %q", name, sec)
			}
			if !strings.Contains(h.m.status, tc.prefix) {
				t.Errorf("status = %q, want it to mention %q", h.m.status, tc.prefix)
			}
		})
	}
}

// A section change and a name change in one edit are two writes, the
// move first.
func TestEditorSectionAndNameMoveFirst(t *testing.T) {
	_, ops, state := editInEditor(t, func(s string) string {
		s = strings.Replace(s, "name: ソファを買う", "name: 変えた", 1)
		return strings.Replace(s, "section: 家具", "section: 手続き", 1)
	})
	if got := ops(); len(got) != 2 || !strings.HasPrefix(got[0], "move_task") {
		t.Errorf("ops = %v, want the move first then the update", got)
	}
	if name, sec := state(); name != "変えた" || sec != "手続き" {
		t.Errorf("task = %q in %q", name, sec)
	}
}
