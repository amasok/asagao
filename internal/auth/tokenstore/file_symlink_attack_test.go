//go:build unix

package tokenstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func writeMode(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func loadWithTimeout(t *testing.T, path string) (*Credentials, error) {
	t.Helper()
	type r struct {
		c   *Credentials
		err error
	}
	ch := make(chan r, 1)
	go func() { c, err := (&File{Path: path}).Load(context.Background()); ch <- r{c, err} }()
	select {
	case x := <-ch:
		return x.c, x.err
	case <-time.After(3 * time.Second):
		t.Fatal("Load hangs")
		return nil, nil
	}
}

func TestLoadSymlinkAttacks(t *testing.T) {
	const tok = `{"refresh_token":"rt"}`
	t.Run("chain of links to a private file", func(t *testing.T) {
		d := t.TempDir()
		writeMode(t, filepath.Join(d, "real"), tok, 0o600)
		symlink(t, filepath.Join(d, "real"), filepath.Join(d, "l1"))
		symlink(t, filepath.Join(d, "l1"), filepath.Join(d, "token.json"))
		if c, err := loadWithTimeout(t, filepath.Join(d, "token.json")); err != nil || c.RefreshToken != "rt" {
			t.Fatalf("%+v %v", c, err)
		}
	})
	t.Run("chain ending at a file others can write is refused", func(t *testing.T) {
		d := t.TempDir()
		writeMode(t, filepath.Join(d, "real"), tok, 0o666)
		symlink(t, filepath.Join(d, "real"), filepath.Join(d, "l1"))
		symlink(t, filepath.Join(d, "l1"), filepath.Join(d, "token.json"))
		if c, err := loadWithTimeout(t, filepath.Join(d, "token.json")); err == nil || errors.Is(err, ErrNotFound) {
			t.Fatalf("%+v %v", c, err)
		}
	})
	t.Run("broken link is not found, not an error that blocks login", func(t *testing.T) {
		d := t.TempDir()
		symlink(t, filepath.Join(d, "nowhere"), filepath.Join(d, "token.json"))
		if _, err := loadWithTimeout(t, filepath.Join(d, "token.json")); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("link loop", func(t *testing.T) {
		d := t.TempDir()
		symlink(t, filepath.Join(d, "b"), filepath.Join(d, "a"))
		symlink(t, filepath.Join(d, "a"), filepath.Join(d, "b"))
		if _, err := loadWithTimeout(t, filepath.Join(d, "a")); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("link to a FIFO does not hang", func(t *testing.T) {
		d := t.TempDir()
		if err := syscall.Mkfifo(filepath.Join(d, "fifo"), 0o600); err != nil {
			t.Fatal(err)
		}
		symlink(t, filepath.Join(d, "fifo"), filepath.Join(d, "token.json"))
		if _, err := loadWithTimeout(t, filepath.Join(d, "token.json")); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("FIFO itself does not hang", func(t *testing.T) {
		d := t.TempDir()
		if err := syscall.Mkfifo(filepath.Join(d, "token.json"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadWithTimeout(t, filepath.Join(d, "token.json")); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("link to a folder", func(t *testing.T) {
		d := t.TempDir()
		symlink(t, d, filepath.Join(d, "token.json"))
		if _, err := loadWithTimeout(t, filepath.Join(d, "token.json")); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("link to /dev/zero style device is refused", func(t *testing.T) {
		d := t.TempDir()
		symlink(t, "/dev/null", filepath.Join(d, "token.json"))
		if _, err := loadWithTimeout(t, filepath.Join(d, "token.json")); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("link to a file only readable by others is read", func(t *testing.T) {
		d := t.TempDir()
		writeMode(t, filepath.Join(d, "real"), tok, 0o644)
		symlink(t, filepath.Join(d, "real"), filepath.Join(d, "token.json"))
		if c, err := loadWithTimeout(t, filepath.Join(d, "token.json")); err != nil || c.RefreshToken != "rt" {
			t.Fatalf("%+v %v", c, err)
		}
	})
	t.Run("regular token file others can write is refused, readable is read", func(t *testing.T) {
		for mode, ok := range map[os.FileMode]bool{0o600: true, 0o644: true, 0o640: true, 0o620: false, 0o602: false, 0o666: false} {
			d := t.TempDir()
			writeMode(t, filepath.Join(d, "token.json"), tok, mode)
			_, err := loadWithTimeout(t, filepath.Join(d, "token.json"))
			if (err == nil) != ok {
				t.Fatalf("mode %v: err = %v, want ok=%v", mode, err, ok)
			}
		}
	})
	t.Run("link to a group-writable file", func(t *testing.T) {
		d := t.TempDir()
		writeMode(t, filepath.Join(d, "real"), tok, 0o660)
		symlink(t, filepath.Join(d, "real"), filepath.Join(d, "token.json"))
		if _, err := loadWithTimeout(t, filepath.Join(d, "token.json")); err == nil || errors.Is(err, ErrNotFound) {
			t.Fatalf("%v", err)
		}
	})
}

func TestSaveFolderSymlinkAttacks(t *testing.T) {
	ctx := context.Background()
	cred := &Credentials{RefreshToken: "rt"}
	t.Run("link to a readable folder is used and left unchanged", func(t *testing.T) {
		for _, mode := range []os.FileMode{0o755, 0o750, 0o701, 0o705} {
			base := t.TempDir()
			real := filepath.Join(base, "real")
			if err := os.Mkdir(real, 0o700); err != nil {
				t.Fatal(err)
			}
			os.Chmod(real, mode)
			symlink(t, real, filepath.Join(base, "cfg"))
			err := (&File{Path: filepath.Join(base, "cfg", "token.json")}).Save(ctx, cred)
			if got := modeOf(t, real); got != mode {
				t.Fatalf("mode %v changed to %v", mode, got)
			}
			os.Chmod(real, 0o700)
			if err != nil {
				t.Fatalf("mode %v: %v", mode, err)
			}
			if got := modeOf(t, filepath.Join(real, "token.json")); got != 0o600 {
				t.Fatalf("token file mode %v", got)
			}
		}
	})
	t.Run("link to a folder others can write to is refused and left unchanged", func(t *testing.T) {
		for _, mode := range []os.FileMode{0o770, 0o777, 0o775, 0o757, 0o722, 0o702} {
			base := t.TempDir()
			real := filepath.Join(base, "real")
			if err := os.Mkdir(real, 0o700); err != nil {
				t.Fatal(err)
			}
			os.Chmod(real, mode)
			symlink(t, real, filepath.Join(base, "cfg"))
			err := (&File{Path: filepath.Join(base, "cfg", "token.json")}).Save(ctx, cred)
			os.Chmod(real, 0o700)
			if err == nil {
				t.Fatalf("mode %v: saved into a folder others can write to", mode)
			}
			os.Chmod(real, mode)
			if got := modeOf(t, real); got != mode {
				t.Fatalf("mode %v changed to %v", mode, got)
			}
			os.Chmod(real, 0o700)
			if entries, _ := os.ReadDir(real); len(entries) != 0 {
				t.Fatalf("mode %v: left %d files behind", mode, len(entries))
			}
		}
	})
	t.Run("chain of folder links ending at a private folder", func(t *testing.T) {
		base := t.TempDir()
		real := filepath.Join(base, "real")
		os.Mkdir(real, 0o700)
		symlink(t, real, filepath.Join(base, "l1"))
		symlink(t, filepath.Join(base, "l1"), filepath.Join(base, "cfg"))
		f := &File{Path: filepath.Join(base, "cfg", "token.json")}
		if err := f.Save(ctx, cred); err != nil {
			t.Fatal(err)
		}
		if c, err := f.Load(ctx); err != nil || c.RefreshToken != "rt" {
			t.Fatalf("%+v %v", c, err)
		}
	})
	t.Run("broken folder link", func(t *testing.T) {
		base := t.TempDir()
		symlink(t, filepath.Join(base, "nowhere"), filepath.Join(base, "cfg"))
		if err := (&File{Path: filepath.Join(base, "cfg", "token.json")}).Save(ctx, cred); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("folder link to a regular file", func(t *testing.T) {
		base := t.TempDir()
		writeMode(t, filepath.Join(base, "f"), "x", 0o600)
		symlink(t, filepath.Join(base, "f"), filepath.Join(base, "cfg"))
		if err := (&File{Path: filepath.Join(base, "cfg", "token.json")}).Save(ctx, cred); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("a link in the middle of the path is not the token folder", func(t *testing.T) {
		base := t.TempDir()
		real := filepath.Join(base, "real")
		os.Mkdir(real, 0o755) // shared parent, reached through a link
		symlink(t, real, filepath.Join(base, "mid"))
		f := &File{Path: filepath.Join(base, "mid", "asana", "token.json")}
		if err := f.Save(ctx, cred); err != nil {
			t.Fatal(err)
		}
		if got := modeOf(t, filepath.Join(real, "asana")); got != 0o700 {
			t.Fatalf("token folder mode %v", got)
		}
		if got := modeOf(t, filepath.Join(real, "asana", "token.json")); got&0o077 != 0 {
			t.Fatalf("token file mode %v", got)
		}
	})
	t.Run("token.json as a link is replaced, the target is not written", func(t *testing.T) {
		base := t.TempDir()
		victim := filepath.Join(base, "victim")
		writeMode(t, victim, "keep", 0o644)
		symlink(t, victim, filepath.Join(base, "token.json"))
		if err := (&File{Path: filepath.Join(base, "token.json")}).Save(ctx, cred); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(victim)
		if string(b) != "keep" || modeOf(t, victim) != 0o644 {
			t.Fatalf("victim changed: %q %v", b, modeOf(t, victim))
		}
	})
}
