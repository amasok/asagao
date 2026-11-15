package tokenstore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// #37 (tester): two processes refreshing the token at once (TUI and MCP server) both save.
// Every save is complete: the file is always one whole token, 0600, and no temporary file
// is left behind.
func TestFileConcurrentSaves(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "conf")
	f := &File{Path: filepath.Join(dir, "token.json")}
	const n = 30
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			g := &File{Path: f.Path}
			errs <- g.Save(ctx, &Credentials{RefreshToken: fmt.Sprintf("rt-%02d-%s", i, strings.Repeat("x", 4096))})
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	c, err := f.Load(ctx)
	if err != nil || !strings.HasPrefix(c.RefreshToken, "rt-") || len(c.RefreshToken) != 6+4096 {
		t.Fatalf("token after concurrent saves: %v %v", c, err)
	}
	if fi, err := os.Stat(f.Path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode: %v %v", fi, err)
	}
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("folder mode: %v %v", fi, err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("files left: %v", entries)
	}
}

// A token folder whose mode has extra bits (setgid, group write) is made exactly 0700.
func TestFileSaveFolderOddModes(t *testing.T) {
	for _, mode := range []os.FileMode{0o777, 0o750, 0o711, 0o500} {
		t.Run(mode.String(), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "conf")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(dir, mode); err != nil {
				t.Fatal(err)
			}
			f := &File{Path: filepath.Join(dir, "token.json")}
			if err := f.Save(context.Background(), &Credentials{RefreshToken: "rt"}); err != nil {
				t.Fatalf("mode %v: %v", mode, err)
			}
			if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
				t.Fatalf("folder mode %v -> %v %v", mode, fi.Mode(), err)
			}
		})
	}
}
