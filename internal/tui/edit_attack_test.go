package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asagao/internal/store"
)

// editWith opens the editor on the first task, applies edit to the file and returns the
// screen after saving.
func editWith(t *testing.T, edit func(string) string) (*harness, *store.Store) {
	t.Helper()
	st := fixtureStore(t)
	m := New(context.Background(), onStore(st, Deps{Now: func() time.Time { return testNow },
		RunEditor: func(path string, done func(error) tea.Msg) tea.Cmd { return fakeEditor(t, edit)(path, done) }}))
	h := &harness{t: t, m: m}
	h.send(tea.WindowSizeMsg{Width: 140, Height: 30})
	h.run(m.Init())
	h.key("e")
	return h, st
}

func opsOf(t *testing.T, st *store.Store) []store.Op {
	t.Helper()
	ops, err := st.ListOps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return ops
}

// An invalid due date or assignee in the editor file writes nothing: not the other fields,
// and not the section move either.
func TestEditInvalidFieldWritesNothing(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(string) string
		want string
	}{
		{"bad due", func(s string) string { return strings.Replace(s, "\ndue: 2026-11-10", "\ndue: someday", 1) }, "due: "},
		{"bad assignee", func(s string) string { return strings.Replace(s, "\nassignee: me", "\nassignee: 誰か", 1) }, "assignee: "},
		{"both bad: due first", func(s string) string {
			s = strings.Replace(s, "\ndue: 2026-11-10", "\ndue: someday", 1)
			return strings.Replace(s, "\nassignee: me", "\nassignee: 誰か", 1)
		}, "due: "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, st := editWith(t, func(s string) string {
				s = tc.edit(s)
				s = strings.Replace(s, "name: 転居届を出す", "name: 変えた名前", 1)
				s = strings.Replace(s, "section: 手続き", "section: 家具", 1)
				return s + "\n本文を足す\n"
			})
			if n := len(opsOf(t, st)); n != 0 {
				t.Fatalf("queued %d ops: %+v", n, opsOf(t, st))
			}
			d, _ := st.GetTask(context.Background(), "t2")
			if d.Name != "転居届を出す" || d.Memberships[0].SectionName != "手続き" {
				t.Fatalf("task changed: %+v %+v", d.TaskSummary, d.Memberships)
			}
			if s := h.screen(); !strings.Contains(s, tc.want) {
				t.Fatalf("screen lacks %q:\n%s", tc.want, s)
			}
		})
	}
}

// An unknown section fails the whole edit: the name is not written either.
func TestEditUnknownSectionWritesNothing(t *testing.T) {
	h, st := editWith(t, func(s string) string {
		s = strings.Replace(s, "name: 転居届を出す", "name: 変えた名前", 1)
		return strings.Replace(s, "section: 手続き", "section: ない", 1)
	})
	if ops := opsOf(t, st); len(ops) != 0 {
		t.Fatalf("queued %+v", ops)
	}
	if s := h.screen(); !strings.Contains(s, "section: ") {
		t.Fatalf("no section error:\n%s", s)
	}
}

// The count in the status line is the number of fields, with a description counting once
// and a section move counting once.
func TestEditStatusCount(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(string) string
		want string
		ops  int
	}{
		{"notes only", func(s string) string { return s + "\n足した\n" }, "（1 項目）", 1},
		{"name and notes", func(s string) string {
			return strings.Replace(s, "name: 転居届を出す", "name: 新", 1) + "\n足した\n"
		}, "（2 項目）", 1},
		{"section only", func(s string) string { return strings.Replace(s, "section: 手続き", "section: 家具", 1) }, "（1 項目）", 1},
		{"everything", func(s string) string {
			s = strings.Replace(s, "name: 転居届を出す", "name: 新", 1)
			s = strings.Replace(s, "completed: false", "completed: true", 1)
			s = strings.Replace(s, "section: 手続き", "section: 家具", 1)
			return s + "\n足した\n"
		}, "（4 項目）", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, st := editWith(t, tc.edit)
			if s := h.screen(); !strings.Contains(s, tc.want) {
				t.Fatalf("want %s:\n%s", tc.want, s)
			}
			if ops := opsOf(t, st); len(ops) != tc.ops {
				t.Fatalf("ops = %d, want %d: %+v", len(ops), tc.ops, ops)
			}
		})
	}
}
