//go:build unix

package tokenstore

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func modeOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

// A token folder that is a symbolic link is not chmod'ed: it is used if what it points to is
// a folder of ours nobody else can write to (#73). 0755 is fine (dotfiles): the token file
// in it is 0600, and only someone who can write to the folder could replace it.
func TestFileSaveFolderSymlink(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		mode os.FileMode
		ok   bool
	}{{0o700, true}, {0o755, true}, {0o711, true}, {0o750, true}, {0o770, false}, {0o777, false}, {0o757, false}, {0o720, false}} {
		t.Run(c.mode.String(), func(t *testing.T) {
			root := t.TempDir()
			real := filepath.Join(root, "dotfiles")
			if err := os.Mkdir(real, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(real, c.mode); err != nil {
				t.Fatal(err)
			}
			conf := filepath.Join(root, "asana-tui")
			symlink(t, real, conf)
			f := &File{Path: filepath.Join(conf, "token.json")}
			err := f.Save(ctx, &Credentials{RefreshToken: "rt"})
			if got := modeOf(t, real); got != c.mode {
				t.Fatalf("the folder the link points to was changed: %v -> %v", c.mode, got)
			}
			if !c.ok {
				if err == nil {
					t.Fatal("saved into a folder others can write to")
				}
				if _, err := os.Lstat(filepath.Join(real, "token.json")); !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("token written: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := f.Load(ctx)
			if err != nil || got.RefreshToken != "rt" {
				t.Fatalf("Load = %+v, %v", got, err)
			}
		})
	}
}

// A token file, or what it links to, is read only if it is a regular file of ours that nobody
// else can write to; anything else that is not a regular file is refused (#73).
func TestFileLoadSymlink(t *testing.T) {
	ctx := context.Background()
	const token = `{"refresh_token":"rt"}`
	write := func(t *testing.T, path string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("link to a private file", func(t *testing.T) {
		dir := t.TempDir()
		write(t, filepath.Join(dir, "real.json"), 0o600)
		symlink(t, filepath.Join(dir, "real.json"), filepath.Join(dir, "token.json"))
		got, err := (&File{Path: filepath.Join(dir, "token.json")}).Load(ctx)
		if err != nil || got.RefreshToken != "rt" {
			t.Fatalf("Load = %+v, %v", got, err)
		}
	})
	for _, c := range []struct {
		mode os.FileMode
		ok   bool
	}{{0o644, true}, {0o640, true}, {0o400, true}, {0o620, false}, {0o602, false}, {0o666, false}} {
		for _, viaLink := range []bool{false, true} {
			name := c.mode.String()
			if viaLink {
				name = "link to " + name
			}
			t.Run(name, func(t *testing.T) {
				dir := t.TempDir()
				real := filepath.Join(dir, "token.json")
				if viaLink {
					real = filepath.Join(dir, "real.json")
					symlink(t, real, filepath.Join(dir, "token.json"))
				}
				write(t, real, c.mode)
				got, err := (&File{Path: filepath.Join(dir, "token.json")}).Load(ctx)
				if c.ok {
					if err != nil || got.RefreshToken != "rt" {
						t.Fatalf("Load = %+v, %v", got, err)
					}
				} else if err == nil || errors.Is(err, ErrNotFound) {
					t.Fatalf("Load = %+v, %v; want refused", got, err)
				}
			})
		}
	}
	t.Run("regular file", func(t *testing.T) {
		dir := t.TempDir()
		write(t, filepath.Join(dir, "token.json"), 0o600)
		if got, err := (&File{Path: filepath.Join(dir, "token.json")}).Load(ctx); err != nil || got.RefreshToken != "rt" {
			t.Fatalf("Load = %+v, %v", got, err)
		}
	})
	t.Run("link to nothing", func(t *testing.T) {
		dir := t.TempDir()
		symlink(t, filepath.Join(dir, "gone.json"), filepath.Join(dir, "token.json"))
		if _, err := (&File{Path: filepath.Join(dir, "token.json")}).Load(ctx); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Load = %v, want ErrNotFound", err)
		}
	})
	t.Run("link to a folder", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, "sub"), 0o700); err != nil {
			t.Fatal(err)
		}
		symlink(t, filepath.Join(dir, "sub"), filepath.Join(dir, "token.json"))
		if _, err := (&File{Path: filepath.Join(dir, "token.json")}).Load(ctx); err == nil || errors.Is(err, ErrNotFound) {
			t.Fatalf("Load = %v; want refused", err)
		}
	})
	// a FIFO (at the path, or where a link points) is refused without waiting for a writer
	for _, viaLink := range []bool{false, true} {
		name := "FIFO"
		if viaLink {
			name = "link to a FIFO"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			fifo := filepath.Join(dir, "token.json")
			if viaLink {
				fifo = filepath.Join(dir, "fifo")
				symlink(t, fifo, filepath.Join(dir, "token.json"))
			}
			if err := syscall.Mkfifo(fifo, 0o600); err != nil {
				t.Skip("mkfifo:", err)
			}
			done := make(chan error, 1)
			go func() {
				_, err := (&File{Path: filepath.Join(dir, "token.json")}).Load(ctx)
				done <- err
			}()
			select {
			case err := <-done:
				if err == nil || errors.Is(err, ErrNotFound) {
					t.Fatalf("Load = %v; want refused", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("Load waits on a FIFO")
			}
		})
	}
}

// fakeInfo is a FileInfo of a file owned by someone else.
type fakeInfo struct {
	fs.FileInfo
	mode os.FileMode
	uid  uint32
}

func (f fakeInfo) Mode() os.FileMode { return f.mode }
func (f fakeInfo) IsDir() bool       { return f.mode.IsDir() }
func (f fakeInfo) Sys() any          { return &syscall.Stat_t{Uid: f.uid} }

// What is read or saved into must be ours, a link or not (a test cannot make a file of
// another user): a regular token.json someone else put in a folder others can write to is
// refused (#73).
func TestOwnedUnwritableRefusesOtherOwner(t *testing.T) {
	me := uint32(os.Geteuid())
	for _, link := range []bool{false, true} {
		if err := ownedUnwritable("p", fakeInfo{mode: 0o600, uid: me}, link); err != nil {
			t.Fatalf("link %v: own private file refused: %v", link, err)
		}
		if err := ownedUnwritable("p", fakeInfo{mode: 0o600, uid: me + 1}, link); err == nil {
			t.Fatalf("link %v: a file of someone else accepted", link)
		}
		if err := ownedUnwritable("p", fakeInfo{mode: 0o700 | os.ModeDir, uid: me + 1}, link); err == nil {
			t.Fatalf("link %v: a folder of someone else accepted", link)
		}
	}
}

// The error for a link to something others can write to names what it points to and the
// command that fixes it (it is not changed for the user: it may be shared for something
// else). The paths are made safe for the terminal.
func TestFileSymlinkErrorSaysWhatToRun(t *testing.T) {
	ctx := context.Background()
	t.Run("folder", func(t *testing.T) {
		root := t.TempDir()
		real := filepath.Join(root, "dot files")
		if err := os.Mkdir(real, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(real, 0o775); err != nil {
			t.Fatal(err)
		}
		symlink(t, real, filepath.Join(root, "asana-tui"))
		err := (&File{Path: filepath.Join(root, "asana-tui", "token.json")}).Save(ctx, &Credentials{RefreshToken: "rt"})
		target, _ := filepath.EvalSymlinks(real)
		if err == nil || !strings.Contains(err.Error(), "chmod go-w '"+target+"'") {
			t.Fatalf("err = %v; want the command for %s", err, target)
		}
	})
	t.Run("file", func(t *testing.T) {
		dir := t.TempDir()
		real := filepath.Join(dir, "real.json")
		if err := os.WriteFile(real, []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(real, 0o646); err != nil {
			t.Fatal(err)
		}
		symlink(t, real, filepath.Join(dir, "token.json"))
		_, err := (&File{Path: filepath.Join(dir, "token.json")}).Load(ctx)
		target, _ := filepath.EvalSymlinks(real)
		if err == nil || !strings.Contains(err.Error(), "chmod go-w "+target) {
			t.Fatalf("err = %v; want the command for %s", err, target)
		}
	})
	t.Run("control characters", func(t *testing.T) {
		dir := t.TempDir()
		real := filepath.Join(dir, "x\nchmod 777 ~\x1b]0;owned\x07.json")
		if err := os.WriteFile(real, []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(real, 0o666); err != nil {
			t.Fatal(err)
		}
		symlink(t, real, filepath.Join(dir, "token.json"))
		_, err := (&File{Path: filepath.Join(dir, "token.json")}).Load(ctx)
		target, _ := filepath.EvalSymlinks(real)
		// no line made up, nothing for the terminal; the path is shown quoted, and no command
		// is given with a path that is not the real one
		if err == nil || strings.ContainsAny(err.Error(), "\n\r\x1b\x07") ||
			!strings.Contains(err.Error(), strconv.Quote(target)) || strings.Contains(err.Error(), "chmod go-w /") || strings.Contains(err.Error(), "chmod go-w '") {
			t.Fatalf("err = %q", err)
		}
	})
}

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{
		"/home/me/.config/asana-tui": "/home/me/.config/asana-tui",
		"/a b":                       "'/a b'",
		"/it's":                      `'/it'\''s'`,
		"/$(rm)":                     "'/$(rm)'",
	} {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestShowPath(t *testing.T) {
	for in, want := range map[string]string{
		"/home/me/.config/asana-tui": "/home/me/.config/asana-tui",
		"/a b/日本語":                   "/a b/日本語",
		"/x\ny":                      `"/x\ny"`,
		"/x\ty":                      `"/x\ty"`,
		"/x\x1b[2Jy":                 `"/x\x1b[2Jy"`,
		"/x\u202ey":                  `"/x\u202ey"`,
	} {
		if got := showPath(in); got != want {
			t.Errorf("showPath(%q) = %q, want %q", in, got, want)
		}
	}
}
