//go:build unix

package tokenstore

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
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

// A token folder that is a symbolic link is not chmod'ed: it is used only if what it points
// to is a folder of ours nobody else can use (#73).
func TestFileSaveFolderSymlink(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		mode os.FileMode
		ok   bool
	}{{0o700, true}, {0o755, false}, {0o711, false}, {0o770, false}} {
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
					t.Fatal("saved into a folder others can use")
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

// A token file that is a symbolic link is read only if it points to a regular file of ours
// that nobody else can read or write; anything else that is not a regular file is refused
// (#73).
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
	for _, mode := range []os.FileMode{0o644, 0o640, 0o604, 0o620, 0o602} {
		t.Run("link to "+mode.String(), func(t *testing.T) {
			dir := t.TempDir()
			write(t, filepath.Join(dir, "real.json"), mode)
			symlink(t, filepath.Join(dir, "real.json"), filepath.Join(dir, "token.json"))
			if got, err := (&File{Path: filepath.Join(dir, "token.json")}).Load(ctx); err == nil || errors.Is(err, ErrNotFound) {
				t.Fatalf("Load = %+v, %v; want refused", got, err)
			}
		})
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
func (f fakeInfo) Sys() any          { return &syscall.Stat_t{Uid: f.uid} }

// What a link points to must be ours (a test cannot make a file of another user).
func TestOwnedPrivateRefusesOtherOwner(t *testing.T) {
	me := uint32(os.Geteuid())
	if err := ownedPrivate("p", fakeInfo{mode: 0o600, uid: me}, "ファイル"); err != nil {
		t.Fatalf("own private file refused: %v", err)
	}
	if err := ownedPrivate("p", fakeInfo{mode: 0o600, uid: me + 1}, "ファイル"); err == nil {
		t.Fatal("a file of someone else accepted")
	}
	if err := ownedPrivate("p", fakeInfo{mode: 0o700 | os.ModeDir, uid: me + 1}, "フォルダ"); err == nil {
		t.Fatal("a folder of someone else accepted")
	}
}

// The error for a link to something others can use names what it points to and the command
// that makes it private (it is not changed for the user: it may be shared for something
// else). The paths are made safe for the terminal.
func TestFileSymlinkErrorSaysWhatToRun(t *testing.T) {
	ctx := context.Background()
	t.Run("folder", func(t *testing.T) {
		root := t.TempDir()
		real := filepath.Join(root, "dot files")
		if err := os.Mkdir(real, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(real, 0o755); err != nil {
			t.Fatal(err)
		}
		symlink(t, real, filepath.Join(root, "asana-tui"))
		err := (&File{Path: filepath.Join(root, "asana-tui", "token.json")}).Save(ctx, &Credentials{RefreshToken: "rt"})
		target, _ := filepath.EvalSymlinks(real)
		if err == nil || !strings.Contains(err.Error(), "chmod 700 '"+target+"'") {
			t.Fatalf("err = %v; want the command for %s", err, target)
		}
	})
	t.Run("file", func(t *testing.T) {
		dir := t.TempDir()
		real := filepath.Join(dir, "real.json")
		if err := os.WriteFile(real, []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(real, 0o644); err != nil {
			t.Fatal(err)
		}
		symlink(t, real, filepath.Join(dir, "token.json"))
		_, err := (&File{Path: filepath.Join(dir, "token.json")}).Load(ctx)
		target, _ := filepath.EvalSymlinks(real)
		if err == nil || !strings.Contains(err.Error(), "chmod 600 "+target) {
			t.Fatalf("err = %v; want the command for %s", err, target)
		}
	})
	t.Run("control characters", func(t *testing.T) {
		dir := t.TempDir()
		real := filepath.Join(dir, "x\x1b]0;owned\x07.json")
		if err := os.WriteFile(real, []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(real, 0o644); err != nil {
			t.Fatal(err)
		}
		symlink(t, real, filepath.Join(dir, "token.json"))
		_, err := (&File{Path: filepath.Join(dir, "token.json")}).Load(ctx)
		if err == nil || strings.ContainsAny(err.Error(), "\x1b\x07") {
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
