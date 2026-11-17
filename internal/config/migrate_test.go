package config

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// legacyEnv points the XDG folders at a temporary directory and returns it.
func legacyEnv(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, k := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME"} {
		dir := filepath.Join(root, k)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv(k, dir)
	}
	return root
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func isSymlink(t *testing.T, path string) bool {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode()&fs.ModeSymlink != 0
}

func skipWithoutSymlinks(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("symbolic links need extra rights on Windows")
	}
}

// Only the old folders exist: they are moved, and the old paths are links to the new
// folders, so an old binary still reads and writes the same files (one outbox, not two).
func TestMigrateLegacyDirsMovesAndLeavesLink(t *testing.T) {
	skipWithoutSymlinks(t)
	legacyEnv(t)
	oldConf := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), LegacyAppName)
	oldData := filepath.Join(os.Getenv("XDG_DATA_HOME"), LegacyAppName)
	oldCache := filepath.Join(os.Getenv("XDG_CACHE_HOME"), LegacyAppName)
	writeFile(t, filepath.Join(oldConf, "config.toml"), "[sync]\ncompleted_days = 3\n")
	writeFile(t, filepath.Join(oldData, "store.db"), "db")
	writeFile(t, filepath.Join(oldCache, "attachments", "1-a.png"), "png")

	if err := MigrateLegacyDirs(); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(DataDir(), "store.db")); got != "db" {
		t.Errorf("store.db in the new folder = %q", got)
	}
	if got := readFile(t, filepath.Join(CacheDir(), "attachments", "1-a.png")); got != "png" {
		t.Errorf("cached attachment in the new folder = %q", got)
	}
	cfg, err := Load()
	if err != nil || cfg.Sync.CompletedDays != 3 {
		t.Errorf("config.toml should be read from the new folder: %+v, %v", cfg.Sync, err)
	}
	for _, p := range []struct{ old, new string }{{oldConf, ConfigDir()}, {oldData, DataDir()}, {oldCache, CacheDir()}} {
		if fi, err := os.Lstat(p.new); err != nil || !fi.IsDir() {
			t.Errorf("%s should be a real folder: %v, %v", p.new, fi, err)
		}
		if !isSymlink(t, p.old) {
			t.Errorf("%s should be a link to the new folder", p.old)
			continue
		}
		a, _ := filepath.EvalSymlinks(p.old)
		b, _ := filepath.EvalSymlinks(p.new)
		if a != b {
			t.Errorf("%s points to %s, want %s", p.old, a, b)
		}
	}
	// What an old binary writes through the old path is in the new folder.
	writeFile(t, filepath.Join(oldData, "outbox-marker"), "x")
	if got := readFile(t, filepath.Join(DataDir(), "outbox-marker")); got != "x" {
		t.Errorf("a write through the old path should land in the new folder: %q", got)
	}
	// Running again changes nothing.
	if err := MigrateLegacyDirs(); err != nil {
		t.Fatal(err)
	}
	if !isSymlink(t, oldData) || readFile(t, filepath.Join(DataDir(), "store.db")) != "db" {
		t.Error("a second run changed the folders")
	}
}

// Both exist: the new folder is used, and the old one is left exactly as it is.
func TestMigrateLegacyDirsBothExist(t *testing.T) {
	legacyEnv(t)
	oldData := filepath.Join(os.Getenv("XDG_DATA_HOME"), LegacyAppName)
	writeFile(t, filepath.Join(oldData, "store.db"), "old")
	writeFile(t, filepath.Join(DataDir(), "store.db"), "new")
	if err := MigrateLegacyDirs(); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(oldData, "store.db")); got != "old" {
		t.Errorf("old store = %q", got)
	}
	if got := readFile(t, filepath.Join(DataDir(), "store.db")); got != "new" {
		t.Errorf("new store = %q", got)
	}
	if fi, _ := os.Lstat(oldData); !fi.IsDir() {
		t.Error("the old folder should stay a folder")
	}
}

// Neither exists (a new user): nothing is made.
func TestMigrateLegacyDirsNothingToDo(t *testing.T) {
	legacyEnv(t)
	if err := MigrateLegacyDirs(); err != nil {
		t.Fatal(err)
	}
	for _, p := range dirPairs() {
		for _, d := range []string{p.old, p.new} {
			if _, err := os.Lstat(d); err == nil {
				t.Errorf("%s was made", d)
			}
		}
	}
}

// The old path is a file: it is not touched, the error says why, and the new folder
// is not made (startup goes on with it).
func TestMigrateLegacyDirsOldIsFile(t *testing.T) {
	legacyEnv(t)
	old := filepath.Join(os.Getenv("XDG_DATA_HOME"), LegacyAppName)
	writeFile(t, old, "not a folder")
	err := MigrateLegacyDirs()
	if err == nil || !strings.Contains(err.Error(), old) || !strings.Contains(err.Error(), "フォルダではない") {
		t.Fatalf("want an error naming %s: %v", old, err)
	}
	if got := readFile(t, old); got != "not a folder" {
		t.Errorf("the file was changed: %q", got)
	}
	if _, err := os.Lstat(DataDir()); err == nil {
		t.Error("the new folder should not be made")
	}
}

// The folders cannot be moved (no write permission on their parent): the old folder
// stays with everything in it, and the error says so.
func TestMigrateLegacyDirsPermissionDenied(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs Unix permissions and a non-root user")
	}
	legacyEnv(t)
	base := os.Getenv("XDG_DATA_HOME")
	old := filepath.Join(base, LegacyAppName)
	writeFile(t, filepath.Join(old, "store.db"), "db")
	if err := os.Chmod(base, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(base, 0o700) })
	err := MigrateLegacyDirs()
	if err == nil || !strings.Contains(err.Error(), "移せませんでした") || !strings.Contains(err.Error(), "そのまま残しています") {
		t.Fatalf("want a clear error: %v", err)
	}
	if got := readFile(t, filepath.Join(old, "store.db")); got != "db" {
		t.Errorf("old store = %q", got)
	}
}

// The old path is a link to a folder kept elsewhere (dotfiles): the new path becomes a
// link to the same folder, and the old link is left as it is.
func TestMigrateLegacyDirsOldIsLink(t *testing.T) {
	skipWithoutSymlinks(t)
	root := legacyEnv(t)
	dotfiles := filepath.Join(root, "dotfiles", "asana-tui")
	writeFile(t, filepath.Join(dotfiles, "config.toml"), "[sync]\ncompleted_days = 5\n")
	old := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), LegacyAppName)
	if err := os.Symlink(dotfiles, old); err != nil {
		t.Fatal(err)
	}
	if err := MigrateLegacyDirs(); err != nil {
		t.Fatal(err)
	}
	if !isSymlink(t, ConfigDir()) || !isSymlink(t, old) {
		t.Fatal("both paths should be links")
	}
	want, _ := filepath.EvalSymlinks(dotfiles)
	if got, _ := filepath.EvalSymlinks(ConfigDir()); got != want {
		t.Errorf("new config folder points to %s, want %s", got, want)
	}
	if cfg, err := Load(); err != nil || cfg.Sync.CompletedDays != 5 {
		t.Errorf("config.toml should be read through the new link: %+v, %v", cfg.Sync, err)
	}
}

// The old path is a link to nothing (it pointed to the new folder, which the user
// removed since): nothing is made, no error.
func TestMigrateLegacyDirsDanglingLink(t *testing.T) {
	skipWithoutSymlinks(t)
	legacyEnv(t)
	old := filepath.Join(os.Getenv("XDG_DATA_HOME"), LegacyAppName)
	if err := os.Symlink(appName, old); err != nil {
		t.Fatal(err)
	}
	if err := MigrateLegacyDirs(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(DataDir()); err == nil {
		t.Error("the new folder should not be made")
	}
}

// Several processes start at once (the TUI and MCP servers): one moves the folder, the
// others just use it. Nobody fails, nothing is lost, and the old path is a link.
func TestMigrateLegacyDirsConcurrent(t *testing.T) {
	skipWithoutSymlinks(t)
	for i := range 20 {
		legacyEnv(t)
		old := filepath.Join(os.Getenv("XDG_DATA_HOME"), LegacyAppName)
		writeFile(t, filepath.Join(old, "store.db"), "db")
		var wg sync.WaitGroup
		errs := make(chan error, 8)
		for range 8 {
			wg.Go(func() { errs <- migrateDir(old, DataDir()) })
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("run %d: %v", i, err)
			}
		}
		if got := readFile(t, filepath.Join(DataDir(), "store.db")); got != "db" {
			t.Fatalf("run %d: store = %q", i, got)
		}
		if fi, _ := os.Lstat(DataDir()); !fi.IsDir() {
			t.Fatalf("run %d: new path is not a folder", i)
		}
		if !isSymlink(t, old) {
			t.Fatalf("run %d: old path is not a link", i)
		}
	}
}

// OwnDirs lists the folders under both names, so the MCP server refuses uploads from the
// old ones too (left in place when both exist).
func TestOwnDirsIncludesLegacy(t *testing.T) {
	legacyEnv(t)
	got := strings.Join(OwnDirs(), "\n")
	for _, d := range []string{ConfigDir(), DataDir(), CacheDir()} {
		if !strings.Contains(got, d) {
			t.Errorf("missing %s", d)
		}
	}
	for _, p := range dirPairs() {
		if !strings.Contains(got, p.old) || filepath.Base(p.old) != LegacyAppName {
			t.Errorf("missing %s", p.old)
		}
	}
}
