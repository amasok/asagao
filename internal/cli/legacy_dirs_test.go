package cli

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/config"
)

// Every command moves the folders of the former name first: config.toml is then read from
// the new folder (an invalid value in it makes the command fail), and the old data folder
// is a link to the new one.
func TestCommandsMigrateLegacyDirs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic links need extra rights on Windows")
	}
	conf, data := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", conf)
	t.Setenv("XDG_DATA_HOME", data)
	oldConf := filepath.Join(conf, config.LegacyAppName)
	if err := os.MkdirAll(oldConf, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldConf, "config.toml"), []byte("[ui]\ntheme = \"bogus\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldData := filepath.Join(data, config.LegacyAppName)
	if err := os.MkdirAll(oldData, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldData, "marker"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := runCLI(t, "outbox", "list")
	if err == nil || !strings.Contains(err.Error(), "ui.theme") {
		t.Fatalf("config.toml of the old folder should be read from the new one: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(config.DataDir(), "marker")); err != nil || string(b) != "x" {
		t.Errorf("old data should be in %s: %q, %v", config.DataDir(), b, err)
	}
	if fi, err := os.Lstat(oldData); err != nil || fi.Mode()&fs.ModeSymlink == 0 {
		t.Errorf("%s should be a link to the new folder: %v", oldData, err)
	}
}

// A folder that cannot be moved does not stop the command: it warns and goes on.
func TestCommandsWarnWhenLegacyDirCannotMove(t *testing.T) {
	conf, data := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", conf)
	t.Setenv("XDG_DATA_HOME", data)
	old := filepath.Join(data, config.LegacyAppName)
	if err := os.WriteFile(old, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, stderr, err := runCLISplit(t, "outbox", "list")
	if err != nil {
		t.Fatalf("the command should go on: %v", err)
	}
	if !strings.Contains(stderr, "注意:") || !strings.Contains(stderr, old) {
		t.Errorf("want a warning naming %s:\n%s", old, stderr)
	}
	if b, _ := os.ReadFile(old); string(b) != "file" {
		t.Error("the file at the old path was changed")
	}
}
