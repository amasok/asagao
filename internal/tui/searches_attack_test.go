package tui

import (
	"os"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/readmodel"
)

// A saved search whose project cannot be resolved: an error in the status line (no control
// characters), no panic, and the picker can still leave it.
func TestSavedSearchAttackUnresolvableCondition(t *testing.T) {
	h := savedSearchHarness(t, savedSearchFile(t,
		readmodel.SavedSearch{Name: "壊れ", Project: "存在しないプロジェクト"},
		readmodel.SavedSearch{Name: "今週", DueBefore: "+7d"},
	))
	h.key("space", "/", "enter")
	if strings.ContainsAny(h.m.status, "\x1b\x00") {
		t.Fatalf("status has control characters: %q", h.m.status)
	}
	h.key("space", "/")
	if h.m.picker == nil {
		t.Fatalf("picker must still open; status=%q", h.m.status)
	}
}

// With a saved search shown, f / T / F / search, the board and the calendar keep it
// (they narrow it, they do not silently switch to the project's own list).
func TestSavedSearchAttackSurvivesViewSwitches(t *testing.T) {
	h := savedSearchHarness(t, savedSearchFile(t, readmodel.SavedSearch{Name: "今週", DueBefore: "+7d"}))
	h.key("space", "/", "enter")
	before := h.visibleGIDs()
	if len(before) == 0 {
		t.Fatal("search found nothing")
	}
	for _, k := range []string{"v", "v", "v"} { // cycle board / calendar / list
		h.key(k)
		if h.m.saved == nil {
			t.Fatalf("view switch %q dropped the saved search", k)
		}
	}
	h.chooseFilter(filterAll)
	if h.m.saved == nil {
		t.Fatal("filter dropped the saved search")
	}
	h.key("/")
	h.typeText("ソファ")
	h.key("enter")
	if h.m.saved == nil {
		t.Fatal("search words dropped the saved search")
	}
	for _, g := range h.visibleGIDs() {
		if g != "t1" {
			t.Errorf("search words should narrow within the saved search, got %v", h.visibleGIDs())
		}
	}
}

// The file is edited (search deleted / broken) while the search is shown: a reload keeps
// showing it (the copy), picking again reports the error and does not clear the view.
func TestSavedSearchAttackFileChangesUnderneath(t *testing.T) {
	f := savedSearchFile(t, readmodel.SavedSearch{Name: "今週", DueBefore: "+7d"})
	h := savedSearchHarness(t, f)
	h.key("space", "/", "enter")
	want := h.visibleGIDs()
	if err := os.WriteFile(f.Path, []byte("garbage ["), 0o600); err != nil {
		t.Fatal(err)
	}
	h.run(h.m.loadTasks())
	if got := h.visibleGIDs(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("reload changed the view: %v -> %v", want, got)
	}
	h.key("space", "/")
	if h.m.picker != nil || h.m.status == "" {
		t.Fatalf("broken file should give a status, not a picker: %q", h.m.status)
	}
}
