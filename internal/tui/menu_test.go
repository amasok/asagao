package tui

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// menuEntry is how an entry shows in the menu: its key and the start of its label (the
// rest may be cut to fit the column).
func menuEntry(a action) string {
	return a.key + "  " + string([]rune(a.label)[:2])
}

var menuPageRe = regexp.MustCompile(`メニュー (\d+)/(\d+)`)

// menuScreens opens the menu and returns the screen of every page, turning them with tab.
func menuScreens(t *testing.T, h *harness) []string {
	t.Helper()
	h.key("space")
	first := h.screen()
	n := 1
	if mm := menuPageRe.FindStringSubmatch(first); mm != nil {
		if mm[1] != "1" {
			t.Fatalf("the menu does not open on its first page:\n%s", first)
		}
		n, _ = strconv.Atoi(mm[2])
	}
	screens := []string{first}
	for p := 2; p <= n; p++ {
		h.key("tab")
		s := h.screen()
		if !h.m.leader {
			t.Fatalf("tab closed the menu on page %d", p-1)
		}
		if !strings.Contains(s, fmt.Sprintf("メニュー %d/%d", p, n)) {
			t.Fatalf("not on page %d/%d:\n%s", p, n, s)
		}
		screens = append(screens, s)
	}
	return screens
}

// The space menu fits the screen, and every entry is on one of its pages, on a 30-line
// screen and on smaller ones too (#44).
func TestTUIMenuReachesEveryEntry(t *testing.T) {
	for _, sz := range [][2]int{{140, 30}, {80, 30}, {80, 24}, {80, 20}, {100, 20}, {60, 12}, {40, 8}} {
		t.Run(fmt.Sprintf("%dx%d", sz[0], sz[1]), func(t *testing.T) {
			h := openProjectList(t, fixtureStore(t))
			h.send(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
			entries := h.m.available()
			if len(entries) < 40 {
				t.Fatalf("only %d entries: the test needs a full menu", len(entries))
			}
			screens := menuScreens(t, h)
			for i, s := range screens {
				lines := strings.Split(s, "\n")
				if len(lines) != sz[1] {
					t.Errorf("page %d is %d lines high on a %d-line screen", i+1, len(lines), sz[1])
				}
				for _, l := range lines {
					if ansi.StringWidth(l) > sz[0] {
						t.Errorf("page %d: line wider than %d: %q", i+1, sz[0], l)
					}
				}
			}
			all := strings.Join(screens, "\n")
			for _, a := range entries {
				if !strings.Contains(all, menuEntry(a)) {
					t.Errorf("%q (%s) is on no page of the menu", a.label, a.key)
				}
			}
			if t.Failed() {
				t.Logf("pages:\n%s", all)
			}
		})
	}
}

// The menu is on one page where it fits, below the task list as before. (The height
// leaves room for the menu as it grows: 41 rows are needed with #37's entries.)
func TestTUIMenuOnePageWhenItFits(t *testing.T) {
	h := openProjectList(t, fixtureStore(t))
	h.send(tea.WindowSizeMsg{Width: 140, Height: 44})
	h.key("space")
	s := h.screen()
	if menuPageRe.MatchString(s) || !strings.Contains(s, "メニュー（キーを押す・esc で閉じる）") {
		t.Errorf("a menu that fits is paged:\n%s", s)
	}
	if !strings.Contains(s, "ソファを買う") {
		t.Errorf("the task list is gone under a menu that fits:\n%s", s)
	}
}

// Pages turn both ways and round, the menu stays open meanwhile, and a menu key still
// runs its entry from any page (also one not shown on it).
func TestTUIMenuPageKeys(t *testing.T) {
	h := openProjectList(t, fixtureStore(t))
	h.send(tea.WindowSizeMsg{Width: 80, Height: 20})
	h.key("space")
	_, p, n, _ := h.m.currentMenuPage()
	if p != 0 || n < 2 {
		t.Fatalf("page %d of %d: want the first of several", p, n)
	}
	page := func() int { _, p, _, _ := h.m.currentMenuPage(); return p }
	for _, k := range menuNext {
		h.m.menuPage = 0
		h.send(keyMsg(k))
		if !h.m.leader || page() != 1 {
			t.Errorf("%s: open %v, page %d (want 1)", k, h.m.leader, page())
		}
	}
	for _, k := range menuPrev {
		h.m.menuPage = 1
		h.send(keyMsg(k))
		if !h.m.leader || page() != 0 {
			t.Errorf("%s: open %v, page %d (want 0)", k, h.m.leader, page())
		}
	}
	h.m.menuPage = 0
	h.key("k") // round to the last page
	if page() != n-1 {
		t.Errorf("k on the first page: page %d, want %d", page(), n-1)
	}
	h.key("j") // and back to the first
	if page() != 0 {
		t.Errorf("j on the last page: page %d, want 0", page())
	}
	h.key("tab", "esc")
	if h.m.leader {
		t.Fatal("esc did not close the menu")
	}
	h.key("space")
	if page() != 0 {
		t.Errorf("the menu reopens on page %d", page()+1)
	}
	h.key("?") // on the last page, not the first
	if h.m.leader || !h.m.showHelp {
		t.Errorf("? from page 1 did not open the help: menu %v, help %v", h.m.leader, h.m.showHelp)
	}
}

// The page keys are not menu keys, so turning a page never runs an entry.
func TestMenuPageKeysAreFree(t *testing.T) {
	for _, a := range (&Model{}).actions() {
		if slices.Contains(menuNext, a.key) || slices.Contains(menuPrev, a.key) {
			t.Errorf("%s uses the page key %q", a.id, a.key)
		}
	}
}

func keyMsg(k string) tea.KeyPressMsg {
	switch k {
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "pgdown":
		return tea.KeyPressMsg{Code: tea.KeyPgDown}
	case "pgup":
		return tea.KeyPressMsg{Code: tea.KeyPgUp}
	case "ctrl+d":
		return tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}
	case "ctrl+u":
		return tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}
	}
	return tea.KeyPressMsg{Code: []rune(k)[0], Text: k}
}
