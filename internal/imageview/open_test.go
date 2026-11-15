package imageview

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeOpen records the commands OpenFiles would start.
func fakeOpen(t *testing.T, os string) *[]string {
	t.Helper()
	var cmds []string
	oldGOOS, oldStart, oldQ := goos, startCmd, quarantine
	t.Cleanup(func() { goos, startCmd, quarantine = oldGOOS, oldStart, oldQ })
	goos = os
	startCmd = func(name string, args ...string) error {
		cmds = append(cmds, name+" "+strings.Join(args, " "))
		return nil
	}
	quarantine = func(string) error { return nil }
	return &cmds
}

func file(t *testing.T, dir, name string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("x"), mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRisky(t *testing.T) {
	dir := t.TempDir()
	for name, want := range map[string]bool{
		"見積.pdf": false, "photo.PNG": false, "notes.txt": false, "README": false,
		"evil.terminal": true, "evil.TERMINAL": true, "evil.command": true, "x.webloc": true,
		"x.jar": true, "x.app": true, "x.sh": true, "x.pkg": true, "x.fileloc": true,
		"x.terminal.": true, "x.terminal ": true, "x.inetloc": true, "x.scpt": true,
		"x.webarchive": true, "x.afploc": true, "x.ftploc": true, "x.alfredworkflow": true,
	} {
		if got := Risky(file(t, dir, name, 0o600)); got != want {
			t.Errorf("Risky(%q) = %v, want %v", name, got, want)
		}
	}
	// macOS opens an executable file without extension in Terminal.
	if !Risky(file(t, dir, "run-me", 0o755)) {
		t.Error("an executable file must be risky")
	}
}

func TestOpenFilesRevealsRiskyFiles(t *testing.T) {
	dir := t.TempDir()
	pdf, term := file(t, dir, "a.pdf", 0o600), file(t, dir, "b.terminal", 0o600)

	cmds := fakeOpen(t, "darwin")
	revealed, err := OpenFiles(pdf, term)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"open " + pdf, "open -R " + term}
	if strings.Join(*cmds, "|") != strings.Join(want, "|") || len(revealed) != 1 || revealed[0] != term {
		t.Errorf("darwin: cmds %q revealed %v, want %q", *cmds, revealed, want)
	}

	cmds = fakeOpen(t, "linux")
	if _, err := OpenFiles(term, pdf); err != nil {
		t.Fatal(err)
	}
	want = []string{"xdg-open " + pdf, "xdg-open " + dir}
	if strings.Join(*cmds, "|") != strings.Join(want, "|") {
		t.Errorf("linux: cmds %q, want %q", *cmds, want)
	}
}

// Every file is quarantined before anything is opened; a failure opens nothing.
func TestOpenFilesQuarantinesFirst(t *testing.T) {
	dir := t.TempDir()
	cmds := fakeOpen(t, "darwin")
	var marked []string
	quarantine = func(p string) error { marked = append(marked, p); return nil }
	pdf := file(t, dir, "a.pdf", 0o600)
	if _, err := OpenFiles(pdf); err != nil || len(marked) != 1 || len(*cmds) != 1 {
		t.Fatalf("marked %v cmds %v err %v", marked, *cmds, err)
	}
	*cmds = nil
	quarantine = func(string) error { return os.ErrPermission }
	if _, err := OpenFiles(pdf); err == nil || len(*cmds) != 0 {
		t.Fatalf("quarantine failure must not open: cmds %v err %v", *cmds, err)
	}
}
