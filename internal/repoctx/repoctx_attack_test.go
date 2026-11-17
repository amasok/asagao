//go:build unix

package repoctx

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func repoDir(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	return repo
}

// Odd things named .asana.json are errors (never a hang, never silently skipped): a
// directory, a FIFO, a device, a dangling or looping symlink, an unreadable file.
func TestFindAttackOddFiles(t *testing.T) {
	for name, setup := range map[string]func(p string){
		"directory":        func(p string) { _ = os.Mkdir(p, 0o700) },
		"fifo":             func(p string) { _ = syscall.Mkfifo(p, 0o600) },
		"dev zero":         func(p string) { _ = os.Symlink("/dev/zero", p) },
		"dev null":         func(p string) { _ = os.Symlink("/dev/null", p) },
		"dangling symlink": func(p string) { _ = os.Symlink("/nonexistent/x", p) },
		"symlink loop":     func(p string) { _ = os.Symlink(p, p) },
		"unreadable":       func(p string) { _ = os.WriteFile(p, []byte(`{}`), 0o000) },
		"empty":            func(p string) { _ = os.WriteFile(p, nil, 0o600) },
		"bom":              func(p string) { _ = os.WriteFile(p, []byte("\xef\xbb\xbf{}"), 0o600) },
		"nul bytes":        func(p string) { _ = os.WriteFile(p, []byte("{\x00}"), 0o600) },
		"deep nesting":     func(p string) { _ = os.WriteFile(p, []byte(strings.Repeat("[", 50000)), 0o600) },
	} {
		t.Run(name, func(t *testing.T) {
			if name == "unreadable" && os.Geteuid() == 0 {
				t.Skip("root reads anything")
			}
			repo := repoDir(t)
			setup(filepath.Join(repo, FileName))
			c, err := Find(repo)
			if err == nil {
				t.Errorf("Find = %+v, nil; want an error", c)
			} else if !strings.Contains(err.Error(), FileName) {
				t.Errorf("error does not name the file: %v", err)
			}
		})
	}
}

// A symlink to a regular file is followed (it is the user's own file).
func TestFindAttackSymlinkToFile(t *testing.T) {
	repo := repoDir(t)
	real := filepath.Join(t.TempDir(), "ctx.json")
	if err := os.WriteFile(real, []byte(`{"task":"42"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(repo, FileName)); err != nil {
		t.Fatal(err)
	}
	if c, err := Find(repo); err != nil || c == nil || c.Task != "42" {
		t.Errorf("Find = %+v, %v", c, err)
	}
}

// The search stops at the nearest .git: a nested repository does not use its parent's
// file, a .git that is a file (worktree, submodule) stops it, and the file in the same
// folder as .git is used.
func TestFindAttackRepositoryBoundary(t *testing.T) {
	outer := repoDir(t)
	if err := os.WriteFile(filepath.Join(outer, FileName), []byte(`{"task":"7"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(outer, "vendor", "lib")
	if err := os.MkdirAll(filepath.Join(inner, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	deep := filepath.Join(inner, "pkg")
	if err := os.Mkdir(deep, 0o700); err != nil {
		t.Fatal(err)
	}
	if c, err := Find(deep); err != nil || c != nil {
		t.Errorf("nested repository used the outer file: %+v %v", c, err)
	}
	// submodule: .git is a file
	sub := filepath.Join(outer, "sub")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, ".git"), []byte("gitdir: ../.git/modules/sub"), 0o600); err != nil {
		t.Fatal(err)
	}
	if c, err := Find(sub); err != nil || c != nil {
		t.Errorf("submodule used the outer file: %+v %v", c, err)
	}
	// same folder as .git
	if c, err := Find(outer); err != nil || c == nil || c.Task != "7" {
		t.Errorf("file next to .git: %+v %v", c, err)
	}
	// a broken .git symlink still counts as the root (Lstat)
	odd := filepath.Join(outer, "odd")
	if err := os.MkdirAll(odd, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/nonexistent", filepath.Join(odd, ".git")); err != nil {
		t.Fatal(err)
	}
	if c, err := Find(odd); err != nil || c != nil {
		t.Errorf("dangling .git: %+v %v", c, err)
	}
}

// A relative dir and a dir that does not exist.
func TestFindAttackDirs(t *testing.T) {
	repo := repoDir(t)
	if err := os.WriteFile(filepath.Join(repo, FileName), []byte(`{"task":"1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	old, _ := os.Getwd()
	defer os.Chdir(old)
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	if c, err := Find("."); err != nil || c == nil || c.Task != "1" {
		t.Errorf("relative: %+v %v", c, err)
	}
	if c, err := Find("nope/dir"); err != nil || c == nil || c.Task != "1" {
		t.Logf("nonexistent subfolder: %+v %v", c, err) // walks up lexically; documented either way
	}
}

// Parse: values that look harmful are refused or kept as plain strings.
func TestParseAttack(t *testing.T) {
	for _, in := range []string{
		`{"project":"a\u0000b"}`, `{"project":"a\nb"}`, `{"project":"a\tb"}`, `{"task":"\u001b]0;x\u0007"}`,
		`{"task":"1\u007f"}`,
		`{"project":"p","project":"q","bogus":1}`, `[]`, `"str"`, `{"project":null,"section":"s"}`,
		`{"project":["a"]}`, `{"task":12345}`, `{"Project":"case"} x`, `{"project":"a"}` + "\x00",
	} {
		if c, err := Parse([]byte(in)); err == nil {
			t.Errorf("Parse(%q) = %+v, want an error", in, c)
		}
	}
	// accepted: null, empty, whitespace-only values; last duplicate wins
	for in, want := range map[string]Context{
		`null`:                          {},
		`{}`:                            {},
		`{"project":"   "}`:             {},
		`{"project":"a","project":"b"}`: {Project: "b"},
		`{"PROJECT":"x"}`:               {Project: "x"}, // encoding/json is case-insensitive
	} {
		c, err := Parse([]byte(in))
		if err != nil || c.Project != want.Project || c.Task != want.Task {
			t.Errorf("Parse(%q) = %+v, %v; want %+v", in, c, err, want)
		}
	}
}

// The home folder is a ceiling: a repository whose root is above it counts as none, so the
// file of a folder above $HOME is never used from inside it. The file in the folder itself,
// and a repository at $HOME (dotfiles), still work.
func TestFindAttackHomeCeiling(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home", "me")
	work := filepath.Join(home, "work", "proj")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	// the repository root is above home
	if err := os.Mkdir(filepath.Join(base, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, FileName), []byte(`{"task":"1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	if c, err := Find(work); err != nil || c != nil {
		t.Errorf("used a file above $HOME: %+v %v", c, err)
	}
	// $HOME with a trailing slash, or unset: the same, and no panic
	t.Setenv("HOME", home+"/")
	if c, err := Find(work); err != nil || c != nil {
		t.Errorf("trailing slash: %+v %v", c, err)
	}
	t.Setenv("HOME", "")
	_, _ = Find(work) // may find the file above (no home known); must not panic or hang
	t.Setenv("HOME", home)

	// the folder's own file is found even then
	if err := os.WriteFile(filepath.Join(work, FileName), []byte(`{"task":"2"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if c, err := Find(work); err != nil || c == nil || c.Task != "2" {
		t.Errorf("own file: %+v %v", c, err)
	}
	// a repository at $HOME itself: files up to $HOME are used
	if err := os.Remove(filepath.Join(work, FileName)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(home, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, FileName), []byte(`{"task":"3"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if c, err := Find(work); err != nil || c == nil || c.Task != "3" {
		t.Errorf("repository at $HOME: %+v %v", c, err)
	}
}

// Outside a repository only the folder itself is looked at (a shared parent such as /tmp
// must not choose the project).
func TestFindAttackNoRepositoryOnlyOwnFolder(t *testing.T) {
	base := t.TempDir()
	deep := filepath.Join(base, "a", "b")
	if err := os.MkdirAll(deep, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	if err := os.WriteFile(filepath.Join(base, FileName), []byte(`{"task":"1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if c, err := Find(deep); err != nil || c != nil {
		t.Errorf("parent file used outside a repository: %+v %v", c, err)
	}
	if err := os.WriteFile(filepath.Join(deep, FileName), []byte(`{"task":"2"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if c, err := Find(deep); err != nil || c == nil || c.Task != "2" {
		t.Errorf("own file: %+v %v", c, err)
	}
}

// $HOME given through a symlink while the folder is by its real path: the ceiling still
// holds (the repository above home is not used).
func TestFindAttackHomeViaSymlink(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real", "me")
	work := filepath.Join(real, "proj")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(base, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, FileName), []byte(`{"task":"1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "homelink")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", link)
	if c, err := Find(work); err != nil || c != nil {
		t.Errorf("symlinked $HOME did not stop the search: %+v %v", c, err)
	}
}

// "task" is digits only (it goes into a URL), and what is shown of a bad one is safe.
func TestParseAttackTaskDigits(t *testing.T) {
	for _, task := range []string{"../x", "1/2", "1?a=b", "1#", "1 2", "1\\u0000", "x", "-1", "1.5", "１２", "1e3", "%2e", "t1", "\\u001b[2J"} {
		_, err := Parse([]byte(`{"task":"` + task + `"}`))
		if err == nil {
			t.Errorf("task %q accepted", task)
			continue
		}
		if strings.ContainsAny(err.Error(), "\x1b\x07\u009b") {
			t.Errorf("task %q: error has control characters: %q", task, err)
		}
	}
	for _, task := range []string{"1", "0123", " 12 ", strings.Repeat("9", 40)} {
		if c, err := Parse([]byte(`{"task":"` + task + `"}`)); err != nil || c.Task != strings.TrimSpace(task) {
			t.Errorf("task %q: %+v %v", task, c, err)
		}
	}
}
