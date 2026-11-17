package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/readmodel"
)

// Names and conditions with TOML-special / odd characters survive a round trip.
func TestSearchFileAttackRoundTripOddStrings(t *testing.T) {
	f := SearchFile{Path: filepath.Join(t.TempDir(), "s.toml")}
	want := []readmodel.SavedSearch{
		{Name: `quote " and \ backslash`, Query: "line'single' \"double\" ] [[search]] name = \"x\""},
		{Name: "日本語 🌸", Tag: "# not a comment", Field: "a=b=c"},
		{Name: "[table]", Project: "'''"},
	}
	if err := f.SaveSearches(want); err != nil {
		t.Fatal(err)
	}
	got, err := f.LoadSearches()
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v err %v", got, err)
	}
}

func TestSearchFileAttackBrokenFilesAreErrorsAndUntouched(t *testing.T) {
	for name, body := range map[string]string{
		"not toml":     "[[search\nname=",
		"unknown key":  "[[search]]\nname = 'a'\nduee = 'today'\n",
		"wrong type":   "[[search]]\nname = 'a'\nlimit = 'ten'\n",
		"unknown top":  "[[search]]\nname = 'a'\n[other]\nx = 1\n",
		"binary":       "\x00\x01\xff\xfe",
		"name is list": "[[search]]\nname = ['a']\n",
	} {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "s.toml")
			os.WriteFile(p, []byte(body), 0o600)
			s := app.Searches{File: SearchFile{Path: p}}
			if _, err := s.List(); err == nil {
				t.Fatal("List should fail")
			}
			if err := s.Save(readmodel.SavedSearch{Name: "new"}, true); err == nil {
				t.Fatal("Save must not overwrite a broken file")
			}
			if err := s.Remove("a"); err == nil {
				t.Fatal("Remove must fail on a broken file")
			}
			if b, _ := os.ReadFile(p); string(b) != body {
				t.Fatalf("broken file was rewritten: %q", b)
			}
		})
	}
}

// A failed rename (the target is a directory) leaves no temporary file behind.
func TestSearchFileAttackFailedRenameLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "s.toml")
	if err := os.Mkdir(p, 0o700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(p, "keep"), nil, 0o600)
	if err := (SearchFile{Path: p}).SaveSearches([]readmodel.SavedSearch{{Name: "a"}}); err == nil {
		t.Fatal("want error")
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Fatalf("temporary file left: %v", ents)
	}
}

// A dangling symlink, and a symlink to a file in another directory.
func TestSearchFileAttackSymlinks(t *testing.T) {
	dir, other := t.TempDir(), t.TempDir()
	target := filepath.Join(other, "real.toml")
	link := filepath.Join(dir, "s.toml")
	if err := os.Symlink(target, link); err != nil {
		t.Skip(err)
	}
	f := SearchFile{Path: link}
	if err := f.SaveSearches([]readmodel.SavedSearch{{Name: "a"}}); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the link should stay a link (dangling target created): %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("target should be written: %v", err)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Fatalf("temp file left in link dir: %v", ents)
	}
}

// An existing file with loose permissions is 0600 after a save; a missing folder is created 0700.
func TestSearchFileAttackPermissions(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sub", "deep", "s.toml")
	f := SearchFile{Path: p}
	if err := f.SaveSearches([]readmodel.SavedSearch{{Name: "a"}}); err != nil {
		t.Fatal(err)
	}
	os.Chmod(p, 0o644)
	if err := f.SaveSearches([]readmodel.SavedSearch{{Name: "b"}}); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
}

// Two `searches save` running at once must both end up in the file (no lost update).
func TestSearchFileAttackConcurrentSavesKeepAll(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.toml")
	const n = 12
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			s := app.Searches{File: SearchFile{Path: p}}
			if err := s.Save(readmodel.SavedSearch{Name: fmt.Sprintf("s%02d", i)}, false); err != nil {
				t.Errorf("save %d: %v", i, err)
			}
		}()
	}
	close(start)
	wg.Wait()
	got, err := (app.Searches{File: SearchFile{Path: p}}).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != n {
		t.Fatalf("lost updates: %d of %d saved", len(got), n)
	}
}
