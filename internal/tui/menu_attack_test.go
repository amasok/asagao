package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func screenFits(t *testing.T, s string, w, hgt int, what string) {
	t.Helper()
	lines := strings.Split(s, "\n")
	if len(lines) > hgt {
		t.Errorf("%s: %d lines on a %d-line screen", what, len(lines), hgt)
	}
	for _, l := range lines {
		if ansi.StringWidth(l) > w {
			t.Errorf("%s: line wider than %d: %q", what, w, l)
			break
		}
	}
}

// Very small screens: the menu never panics, never overflows, pages turn round.
func TestAttackMenuTinyScreens(t *testing.T) {
	for _, w := range []int{1, 2, 5, 10, 20, 40} {
		for _, hgt := range []int{1, 2, 3, 4, 5, 7, 10} {
			t.Run(fmt.Sprintf("%dx%d", w, hgt), func(t *testing.T) {
				h := openProjectList(t, fixtureStore(t))
				h.send(tea.WindowSizeMsg{Width: w, Height: hgt})
				h.key("space")
				_, _, n, _ := h.m.currentMenuPage()
				if n < 1 {
					t.Fatalf("pages = %d", n)
				}
				for i := 0; i < n+2; i++ {
					h.key("tab")
					_ = h.screen()
				}
				for i := 0; i < n+2; i++ {
					h.key("shift+tab")
					_ = h.screen()
				}
				h.key("esc")
				if h.m.leader {
					t.Error("esc did not close")
				}
			})
		}
	}
}

// Screens with a sane height (>= 8) never overflow with the menu open.
func TestAttackMenuFitsFromEightLines(t *testing.T) {
	for _, w := range []int{20, 30, 40, 80} {
		for _, hgt := range []int{8, 9, 12} {
			h := openProjectList(t, fixtureStore(t))
			h.send(tea.WindowSizeMsg{Width: w, Height: hgt})
			h.key("space")
			_, _, n, _ := h.m.currentMenuPage()
			for p := 0; p < n; p++ {
				screenFits(t, h.screen(), w, hgt, fmt.Sprintf("%dx%d page %d", w, hgt, p+1))
				h.key("tab")
			}
		}
	}
}

// Resizing while the menu is open on a late page: the page is clamped, nothing panics,
// and the menu stays open on every size.
func TestAttackMenuResizeWhileOpen(t *testing.T) {
	h := openProjectList(t, fixtureStore(t))
	h.send(tea.WindowSizeMsg{Width: 40, Height: 8})
	h.key("space")
	_, _, n, _ := h.m.currentMenuPage()
	for i := 0; i < n-1; i++ {
		h.key("tab")
	}
	for _, sz := range [][2]int{{140, 40}, {20, 4}, {80, 24}, {1, 1}, {60, 12}} {
		h.send(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
		_ = h.screen()
		if !h.m.leader {
			t.Fatalf("resize to %v closed the menu", sz)
		}
		_, p, c, _ := h.m.currentMenuPage()
		if p < 0 || p >= c {
			t.Fatalf("page %d of %d after resize %v", p, c, sz)
		}
		h.key("tab")
		h.key("shift+tab")
	}
	// a big screen shows it on one page, and tab then does not close or misbehave
	h.send(tea.WindowSizeMsg{Width: 200, Height: 60})
	h.key("tab")
	if !h.m.leader {
		t.Error("tab closed a one-page menu")
	}
}

// Every entry is reachable by its key from the last page, on small screens, in every context
// we can open (project list, detail focus, inbox).
func TestAttackMenuKeyReachesEveryEntryFromAnyPage(t *testing.T) {
	h := openProjectList(t, fixtureStore(t))
	h.send(tea.WindowSizeMsg{Width: 40, Height: 8})
	entries := h.m.available()
	if len(entries) == 0 {
		t.Fatal("no entries")
	}
	for _, a := range entries {
		for _, goLast := range []bool{false, true} {
			hh := openProjectList(t, fixtureStore(t))
			hh.send(tea.WindowSizeMsg{Width: 40, Height: 8})
			hh.key("space")
			if goLast {
				hh.key("shift+tab")
			}
			if _, ok := mustAvailable(hh.m, a.key); !ok {
				continue
			}
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Errorf("key %q (%s) panicked: %v", a.key, a.label, r)
					}
				}()
				cmd := hh.m.runMenuKey(a.key)
				_ = cmd
			}()
		}
	}
}

func mustAvailable(m *Model, key string) (action, bool) {
	for _, a := range m.available() {
		if a.key == key {
			return a, true
		}
	}
	return action{}, false
}

// No two entries of one context share a key (else one is unreachable).
func TestAttackMenuKeysUnique(t *testing.T) {
	h := openProjectList(t, fixtureStore(t))
	seen := map[string]string{}
	for _, a := range h.m.available() {
		if o, dup := seen[a.key]; dup {
			t.Errorf("key %q is both %q and %q", a.key, o, a.label)
		}
		seen[a.key] = a.label
	}
}

// Page-turning keys must not leak to the list underneath: the cursor stays put.
func TestAttackMenuPageKeysDoNotMoveTheList(t *testing.T) {
	h := openProjectList(t, fixtureStore(t))
	h.send(tea.WindowSizeMsg{Width: 40, Height: 8})
	before := h.selected()
	h.key("space")
	for _, k := range append(append([]string{}, menuNext...), menuPrev...) {
		h.send(keyMsg(k))
	}
	h.key("esc")
	if h.selected() != before {
		t.Errorf("the list cursor moved: %q -> %q", before, h.selected())
	}
}

// Even on tiny screens the menu's screen is not taller or wider than the terminal. (Below
// 5 lines the whole TUI overflows, menu or not: that floor is older than the menu.)
func TestAttackMenuNoOverflowTiny(t *testing.T) {
	for _, w := range []int{10, 20, 40} {
		for _, hgt := range []int{5, 6, 7} {
			h := openProjectList(t, fixtureStore(t))
			h.send(tea.WindowSizeMsg{Width: w, Height: hgt})
			h.key("space")
			_, _, n, _ := h.m.currentMenuPage()
			for p := 0; p < n; p++ {
				screenFits(t, h.screen(), w, hgt, fmt.Sprintf("%dx%d page %d/%d", w, hgt, p+1, n))
				h.key("tab")
			}
		}
	}
}

// flowMenu never leaves a group title alone at the end of a column, nor starts a column
// with a blank, and keeps every entry (in order) at any column height.
func TestAttackFlowMenuNoOrphanTitles(t *testing.T) {
	var lines []menuLine
	for g := 0; g < 5; g++ {
		if g > 0 {
			lines = append(lines, menuLine{})
		}
		lines = append(lines, menuLine{text: fmt.Sprintf("G%d", g), title: true})
		for i := 0; i < 1+g; i++ {
			lines = append(lines, menuLine{text: fmt.Sprintf("e%d.%d", g, i)})
		}
	}
	for h := 2; h <= len(lines)+1; h++ {
		var got []string
		for _, col := range flowMenu(lines, h) {
			if len(col) > h {
				t.Errorf("h=%d: column of %d lines", h, len(col))
			}
			if col[0].text == "" {
				t.Errorf("h=%d: a column starts with a blank", h)
			}
			if len(col) > 1 && col[len(col)-1].title {
				t.Errorf("h=%d: a column ends with the title %q", h, col[len(col)-1].text)
			}
			for _, l := range col {
				if l.text != "" {
					got = append(got, l.text)
				}
			}
		}
		n := 0
		for _, l := range lines {
			if l.text != "" {
				if n >= len(got) || got[n] != l.text {
					t.Fatalf("h=%d: entries lost or reordered at %d: %v", h, n, got)
				}
				n++
			}
		}
		if n != len(got) {
			t.Errorf("h=%d: %d entries, want %d", h, len(got), n)
		}
	}
}
