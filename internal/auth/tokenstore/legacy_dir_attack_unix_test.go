//go:build unix

package tokenstore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/config"
)

func migratedEnv(t *testing.T, mode os.FileMode) (conf string) {
	t.Helper()
	conf = t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", conf)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	old := filepath.Join(conf, config.LegacyAppName)
	if err := os.MkdirAll(old, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := (&File{Path: filepath.Join(old, "token.json")}).Save(context.Background(), &Credentials{RefreshToken: "rt"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(old, mode); err != nil {
		t.Fatal(err)
	}
	if err := config.MigrateLegacyDirs(); err != nil {
		t.Fatal(err)
	}
	return conf
}

// Whatever mode the old folder had, the new binary reads and saves the token at the new
// path; saving through the old link needs a folder others cannot write to.
func TestAttackFileStoreMigratedFolderModes(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		mode    os.FileMode
		oldOK   bool
		newSave bool
	}{{0o700, true, true}, {0o755, true, true}, {0o750, true, true}, {0o775, false, true}, {0o777, false, true}} {
		t.Run(tc.mode.String(), func(t *testing.T) {
			conf := migratedEnv(t, tc.mode)
			old := &File{Path: filepath.Join(conf, config.LegacyAppName, "token.json")}
			// the token file itself is 0600 and ours: reading through the link is fine
			if _, err := old.Load(ctx); err != nil {
				t.Fatalf("old path load (mode %v): %v", tc.mode, err)
			}
			// saving through the link needs a folder nobody else can write to (#73)
			err := old.Save(ctx, &Credentials{RefreshToken: "rt"})
			if (err == nil) != tc.oldOK {
				t.Fatalf("old path save (mode %v): %v", tc.mode, err)
			}
			if err != nil && strings.TrimSpace(err.Error()) == "" {
				t.Error("empty error")
			}
			nu := &File{Path: defaultFilePath()}
			if c, err := nu.Load(ctx); err != nil || c.RefreshToken != "rt" {
				t.Fatalf("new path load: %+v %v", c, err)
			}
			if err := nu.Save(ctx, &Credentials{RefreshToken: "rt2"}); (err == nil) != tc.newSave {
				t.Fatalf("new path save: %v", err)
			}
		})
	}
}

// The token file stays 0600 through the move.
func TestAttackFileStoreTokenStays0600(t *testing.T) {
	migratedEnv(t, 0o700)
	fi, err := os.Stat(defaultFilePath())
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", fi, err)
	}
}
