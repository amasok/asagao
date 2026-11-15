package imageview

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestReadKey(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("n \x03\x1b[C\x1b[D\x1b[Ax\x1bOq"))
	var got []string
	for range 9 {
		got = append(got, readKey(r))
	}
	// "\x1bO" is not a CSI sequence: it is dropped and "q" read on its own.
	want := []string{"n", "space", "ctrl+c", "right", "left", "", "x", "", "q"}
	if !slices.Equal(got, want) {
		t.Fatalf("keys = %q, want %q", got, want)
	}
	if k := readKey(r); k != "q" {
		t.Fatalf("end of input = %q, want q (quit)", k)
	}
	// A lone ESC (nothing buffered after it) is the esc key.
	lone := bufio.NewReader(strings.NewReader("\x1b"))
	if k := readKey(lone); k != "esc" {
		t.Fatalf("lone ESC = %q", k)
	}
}

func TestBrowse(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		shown       []int
		opened      []int
	}{
		{"next wraps around", "nnnq", []int{0, 1, 2, 0}, nil},
		{"previous wraps around", "pq", []int{0, 2}, nil},
		{"arrows, space, j/k", "\x1b[C \x1b[Dkq", []int{0, 1, 2, 1, 0}, nil},
		{"open the current image", "nonq", []int{0, 1, 1, 2}, []int{1}},
		{"esc quits", "n\x1b", []int{0, 1}, nil},
		{"ctrl+c quits", "\x03n", []int{0}, nil},
		{"end of input quits", "n", []int{0, 1}, nil},
		{"unknown keys redraw in place", "zq", []int{0, 0}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var shown, opened []int
			browse(bufio.NewReader(strings.NewReader(tc.input)), 3,
				func(i int) { shown = append(shown, i) },
				func(i int) { opened = append(opened, i) })
			if !slices.Equal(shown, tc.shown) || !slices.Equal(opened, tc.opened) {
				t.Fatalf("shown %v opened %v, want %v %v", shown, opened, tc.shown, tc.opened)
			}
		})
	}
}

func TestDraw(t *testing.T) {
	dir := t.TempDir()
	img := filepath.Join(dir, "1220507679870013-photo.png")
	if err := os.WriteFile(img, testImage(t, "png", 40, 20), 0o600); err != nil {
		t.Fatal(err)
	}
	paths := []string{filepath.Join(dir, "missing.png"), img}
	box := Box{Cols: 80, Rows: 24, CellW: 10, CellH: 20}

	var out bytes.Buffer
	draw(&out, paths, 1, ProtocolITerm, box, nil)
	s := out.String()
	if !strings.HasPrefix(s, "\x1b[2J\x1b[H\x1b]1337;File=inline=1;") {
		t.Fatalf("should clear the screen then draw the image: %q", s[:min(len(s), 60)])
	}
	if !strings.Contains(s, "\x1b[24;1H\x1b[2K photo.png  (2/2)") {
		t.Fatalf("caption should be on the last row with the name and position: %q", s[strings.LastIndex(s, "\a"):])
	}

	out.Reset()
	draw(&out, paths, 0, ProtocolKitty, box, nil)
	s = out.String()
	if !strings.HasPrefix(s, "\x1b_Ga=d,d=A,q=2\x1b\\") {
		t.Fatalf("kitty images should be deleted before redrawing: %q", s[:min(len(s), 40)])
	}
	if !strings.Contains(s, "\x1b[24;1H\x1b[2K missing.png: ") || strings.Contains(s, "(1/2)") {
		t.Fatalf("a missing file should show the error in the caption: %q", s)
	}

	out.Reset()
	draw(&out, paths, 1, ProtocolITerm, Box{Cols: 10, Rows: 1}, nil)
	if !strings.Contains(out.String(), "\x1b[3;1H") {
		t.Fatalf("tiny terminal: the image keeps one row and the caption goes below it: %q", out.String())
	}
}
