//go:build unix

package tokenstore

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/amasok/asagao/internal/config"
)

// After the config folder of the former name is moved, the file token store accepts both
// paths (#73: the old path is a link to a folder of ours that nobody else can write to):
// the new binary reads the token at the new path, and an old binary still registered
// somewhere reads and saves it through the old one, in the same file.
func TestFileStoreAfterLegacyDirMigration(t *testing.T) {
	ctx := context.Background()
	conf := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", conf)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	oldPath := filepath.Join(conf, config.LegacyAppName, "token.json")
	if err := (&File{Path: oldPath}).Save(ctx, &Credentials{RefreshToken: "rt-old"}); err != nil {
		t.Fatal(err)
	}
	if err := config.MigrateLegacyDirs(); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(filepath.Dir(oldPath)); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the old folder should be a link now: %v", err)
	}
	newStore := &File{Path: defaultFilePath()}
	if c, err := newStore.Load(ctx); err != nil || c.RefreshToken != "rt-old" {
		t.Fatalf("new path: %+v, %v", c, err)
	}
	old := &File{Path: oldPath}
	if c, err := old.Load(ctx); err != nil || c.RefreshToken != "rt-old" {
		t.Fatalf("old path through the link: %+v, %v", c, err)
	}
	if err := old.Save(ctx, &Credentials{RefreshToken: "rt-2"}); err != nil {
		t.Fatalf("saving through the old path: %v", err)
	}
	if c, err := newStore.Load(ctx); err != nil || c.RefreshToken != "rt-2" {
		t.Fatalf("a save through the old path should be seen at the new one: %+v, %v", c, err)
	}
}
