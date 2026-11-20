package tui

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asagao/internal/config"
	"github.com/amasok/asagao/internal/readmodel"
)

func savedSearchHarness(t *testing.T, f SearchFile) *harness {
	t.Helper()
	m := New(context.Background(), Deps{Store: fixtureStore(t), Now: func() time.Time { return testNow }, Searches: f})
	h := &harness{t: t, m: m}
	h.send(tea.WindowSizeMsg{Width: 140, Height: 30})
	h.run(m.Init())
	return h
}

func savedSearchFile(t *testing.T, ss ...readmodel.SavedSearch) config.SearchFile {
	t.Helper()
	f := config.SearchFile{Path: filepath.Join(t.TempDir(), "searches.toml")}
	if err := f.SaveSearches(ss); err != nil {
		t.Fatal(err)
	}
	return f
}

// space / lists the saved searches; choosing one shows what it finds (relative dates read
// on today: testNow is 11-13), the header says so, and 解除 or the sidebar leaves it.
func TestSavedSearchPicker(t *testing.T) {
	h := savedSearchHarness(t, savedSearchFile(t,
		readmodel.SavedSearch{Name: "今週", DueBefore: "+7d", DueAfter: "today"},
		readmodel.SavedSearch{Name: "契約", Query: "契約"},
	))
	h.key("space", "/")
	p := h.m.picker
	if p == nil || p.kind != pickSavedSearch || len(p.items) != 2 || p.items[0].label != "今週" {
		t.Fatalf("picker: %+v\n%s", p, h.screen())
	}
	if !strings.Contains(h.screen(), "due_after=today due_before=+7d") {
		t.Fatalf("the conditions are shown:\n%s", h.screen())
	}
	h.key("enter")
	// mine, due 11-13..11-20: ソファ (11-20) and 電気 (11-13); not 転居届 (11-10)
	got := h.visibleGIDs()
	slices.Sort(got)
	if !slices.Equal(got, []string{"t1", "t3"}) {
		t.Fatalf("今週: %v\n%s", got, h.screen())
	}
	if s := h.screen(); !strings.Contains(s, "保存: 今週") || !strings.Contains(s, "保存した検索「今週」(2)") {
		t.Fatalf("header:\n%s", s)
	}

	// The list's own filter narrows it: 期限切れ is none of them.
	h.chooseFilter(filterOverdue)
	if got := h.visibleGIDs(); len(got) != 0 {
		t.Fatalf("今週 + 期限切れ: %v", got)
	}

	// Choosing another resets the filter; a query search finds everyone's.
	h.key("space", "/")
	if h.m.picker.items[0].value != clearSavedSearch || h.m.picker.cursor != 1 {
		t.Fatalf("解除 first, cursor on the current one: %+v", h.m.picker)
	}
	h.key("j", "enter")
	got = h.visibleGIDs()
	slices.Sort(got)
	if !slices.Equal(got, []string{"t3", "t4"}) || h.m.list.filter != filterOpen {
		t.Fatalf("契約: %v (filter %v)", got, h.m.list.filter)
	}

	h.key("space", "/", "g", "enter") // 解除
	if h.m.list.saved != nil || strings.Contains(h.screen(), "保存:") {
		t.Fatalf("解除:\n%s", h.screen())
	}

	h.key("space", "/", "enter")
	if h.m.list.saved == nil {
		t.Fatal("not shown")
	}
	h.m.moveScope(1)
	if h.m.list.saved != nil {
		t.Fatal("moving in the sidebar should leave the saved search")
	}

	// A project made or archived moves the sidebar too.
	for _, msg := range []projectDoneMsg{{selectProject: "p1"}, {toMyTasks: true}} {
		h.key("space", "/", "enter")
		if h.m.list.saved == nil {
			t.Fatal("not shown")
		}
		h.send(msg)
		if h.m.list.saved != nil || strings.Contains(h.screen(), "保存:") {
			t.Fatalf("%+v should leave the saved search:\n%s", msg, h.screen())
		}
	}
}

// No file: a note instead of an empty picker. A broken file (control characters in a name):
// a status without them, and no picker.
func TestSavedSearchPickerFile(t *testing.T) {
	h := savedSearchHarness(t, config.SearchFile{Path: filepath.Join(t.TempDir(), "searches.toml")})
	h.key("space", "/")
	if h.m.picker != nil || !strings.Contains(h.m.status, "保存した検索はありません") {
		t.Fatalf("no searches: %q %+v", h.m.status, h.m.picker)
	}

	path := filepath.Join(t.TempDir(), "searches.toml")
	if err := os.WriteFile(path, []byte("[[search]]\nname = \"a\\u001b]0;x\\u0007\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h = savedSearchHarness(t, config.SearchFile{Path: path})
	h.key("space", "/")
	if h.m.picker != nil || h.m.status == "" || strings.ContainsAny(h.m.status, "\x1b\x07") {
		t.Fatalf("broken file: %q %+v", h.m.status, h.m.picker)
	}

	// Without a search file at all (tests, or nothing wired) there is nothing to pick.
	h = newHarness(t, 140, 30)
	h.key("space", "/")
	if h.m.picker != nil {
		t.Fatal("picker without a file")
	}
}
