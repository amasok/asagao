package repoctx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The file is found in the folder or a parent, up to the repository's root.
func TestFind(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	deep := filepath.Join(repo, "a", "b")
	if err := os.MkdirAll(deep, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	// outside the repository: not used from inside it
	write(t, filepath.Join(root, FileName), `{"project": "外"}`)
	if c, err := Find(deep); err != nil || c != nil {
		t.Fatalf("found %+v %v outside the repository", c, err)
	}
	// at the repository's root
	write(t, filepath.Join(repo, FileName), `{"project": " 新居 ", "section": "家具", "task": "123"}`)
	c, err := Find(deep)
	if err != nil || c == nil {
		t.Fatalf("Find: %+v %v", c, err)
	}
	if c.Project != "新居" || c.Section != "家具" || c.Task != "123" || c.Path != filepath.Join(repo, FileName) {
		t.Errorf("context %+v", c)
	}
	// the nearest one wins
	write(t, filepath.Join(repo, "a", FileName), `{"task": "456"}`)
	if c, err := Find(deep); err != nil || c == nil || c.Task != "456" || c.Project != "" {
		t.Errorf("nearest: %+v %v", c, err)
	}
	// a worktree's .git is a file: it stops the search too
	wt := filepath.Join(root, "wt")
	write(t, filepath.Join(wt, ".git"), "gitdir: x")
	if c, err := Find(wt); err != nil || c != nil {
		t.Errorf("worktree: %+v %v", c, err)
	}
}

// The search does not go above the home folder, even for a repository whose root is above
// it.
func TestFindStopsAtHome(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, FileName), `{"project": "p"}`)
	write(t, filepath.Join(root, ".git", "HEAD"), "")
	home := filepath.Join(root, "home")
	deep := filepath.Join(home, "work")
	if err := os.MkdirAll(deep, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	if c, err := Find(deep); err != nil || c != nil {
		t.Errorf("found %+v %v above the home folder", c, err)
	}
	if c, err := Find(home); err != nil || c != nil {
		t.Errorf("from the home folder: %+v %v", c, err)
	}
	write(t, filepath.Join(deep, FileName), `{"project": "w"}`)
	if c, err := Find(deep); err != nil || c == nil || c.Project != "w" {
		t.Errorf("the folder's own: %+v %v", c, err)
	}
}

// A $HOME that is a symbolic link stops the search at the folder it leads to, and so does
// a folder reached through a link to the home folder.
func TestFindStopsAtSymlinkedHome(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real", "me")
	work := filepath.Join(real, "proj")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(base, ".git", "HEAD"), "")
	write(t, filepath.Join(base, FileName), `{"task": "1"}`)
	link := filepath.Join(base, "homelink")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", link) // $HOME is the link, the folder is the real path
	if c, err := Find(work); err != nil || c != nil {
		t.Errorf("symlinked $HOME did not stop the search: %+v %v", c, err)
	}
	t.Setenv("HOME", real) // $HOME is the real path, the folder is reached through the link
	if c, err := Find(filepath.Join(link, "proj")); err != nil || c != nil {
		t.Errorf("a folder under a link to $HOME did not stop the search: %+v %v", c, err)
	}
}

// Outside a repository only the folder itself is looked at: a parent folder (a shared
// /tmp, say) does not choose the project.
func TestFindNoRepository(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, FileName), `{"project": "p"}`)
	deep := filepath.Join(root, "x", "y")
	if err := os.MkdirAll(deep, 0o700); err != nil {
		t.Fatal(err)
	}
	if c, err := Find(deep); err != nil || c != nil {
		t.Errorf("used a parent's file outside a repository: %+v %v", c, err)
	}
	if c, err := Find(root); err != nil || c == nil || c.Project != "p" {
		t.Errorf("the folder's own: %+v %v", c, err)
	}
}

// An invalid file is an error, not ignored.
func TestInvalid(t *testing.T) {
	for _, tc := range []struct{ content, want string }{
		{`{"projet": "新居"}`, "projet"},
		{`{"project": 1}`, "JSON"},
		{`{"project": "a"} {}`, "余計"},
		{`not json`, "JSON"},
		{`{"section": "家具"}`, `"project" と一緒に`},
		{`{"project": "a\u001b[2Jb"}`, "制御文字"},
		{`{"project": "a\u009bb"}`, "制御文字"},
		{`{"task": "t1"}`, "数字"},
		{`{"task": "../users/me"}`, "「../users/me」"},
		{`{"task": "1\u001b[2J"}`, "「1[2J」"},
		{`{"task": "123?opt_fields=x"}`, "数字"},
	} {
		dir := t.TempDir()
		write(t, filepath.Join(dir, ".git", "HEAD"), "")
		write(t, filepath.Join(dir, FileName), tc.content)
		c, err := Find(dir)
		if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), FileName) {
			t.Errorf("%s: %+v %v, want an error with %q and the path", tc.content, c, err, tc.want)
		}
	}
}

func TestLoadRefusesOddFiles(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "big.json")
	write(t, big, `{"project": "`+strings.Repeat("a", maxSize)+`"}`)
	if _, err := Load(big); err == nil || !strings.Contains(err.Error(), "大きすぎ") {
		t.Errorf("big: %v", err)
	}
}
