package config

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/readmodel"
)

func TestSearchFileRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := SearchFile{}
	ss, err := f.LoadSearches()
	if err != nil || ss != nil {
		t.Fatalf("no file: %v %v", ss, err)
	}
	want := []readmodel.SavedSearch{
		{Name: "今週", DueBefore: "+7d"},
		{Name: "請求", Query: "請求書", Assignee: "me", Project: "新居", Section: "Done", Tag: "t", Field: "優先度=高",
			DueAfter: "today", Completion: "open", Overdue: true, ModifiedWithinDays: 3, Limit: 20},
	}
	if err := f.SaveSearches(want); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "asagao", "searches.toml")
	if path != SearchesFilePath() {
		t.Fatalf("path %s, want %s", SearchesFilePath(), path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// relative dates are kept as written; empty conditions are not written
	if !strings.Contains(string(b), `due_before = '+7d'`) && !strings.Contains(string(b), `due_before = "+7d"`) {
		t.Errorf("relative date not kept:\n%s", b)
	}
	if strings.Contains(string(b), "section = ''") || strings.Contains(string(b), `tag = ""`) {
		t.Errorf("empty conditions written:\n%s", b)
	}
	got, err := f.LoadSearches()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got, want)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("mode %v, want 0600", fi.Mode().Perm())
		}
	}
	// no temporary file is left behind
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range entries {
		if e.Name() != "searches.toml" {
			t.Errorf("left over: %s", e.Name())
		}
	}
}

func TestSearchFileRejectsBrokenAndUnknown(t *testing.T) {
	for name, body := range map[string]string{
		"not toml":           "[[search]\nname = 'x'\n",
		"unknown top key":    "foo = 1\n[[search]]\nname = 'x'\n",
		"unknown search key": "[[search]]\nname = 'x'\ndue = 'today'\n",
		"wrong type":         "[[search]]\nname = 'x'\nlimit = 'ten'\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "searches.toml")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			ss, err := SearchFile{Path: path}.LoadSearches()
			if err == nil {
				t.Fatalf("loaded %+v", ss)
			}
			if !strings.Contains(err.Error(), path) {
				t.Errorf("error does not name the file: %v", err)
			}
		})
	}
}

// Saving replaces the file in one rename: a file that was 0644 becomes 0600, and a symbolic
// link is kept (the file it points to is replaced).
func TestSearchFileReplacesTheFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("modes and links")
	}
	dir := t.TempDir()
	real := filepath.Join(dir, "dotfiles", "searches.toml")
	if err := os.MkdirAll(filepath.Dir(real), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(real, []byte("[[search]]\nname = 'old'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "searches.toml")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	f := SearchFile{Path: link}
	if err := f.SaveSearches([]readmodel.SavedSearch{{Name: "new"}}); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link replaced: %v %v", fi, err)
	}
	fi, err := os.Stat(real)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("target: %v %v", fi, err)
	}
	got, err := f.LoadSearches()
	if err != nil || len(got) != 1 || got[0].Name != "new" {
		t.Fatalf("got %+v %v", got, err)
	}
}
