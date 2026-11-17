package config

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// helper process: waits for the start file, then migrates and prints the result.
func TestMigrateHelperProcess(t *testing.T) {
	if os.Getenv("ASAGAO_MIGRATE_HELPER") == "" {
		t.Skip("helper")
	}
	start := os.Getenv("ASAGAO_MIGRATE_START")
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		if _, err := os.Stat(start); err == nil {
			break
		}
	}
	if err := MigrateLegacyDirs(); err != nil {
		fmt.Println("MIGRATE-ERR:", err)
		os.Exit(3)
	}
	fmt.Println("MIGRATE-OK")
}

// Real separate processes start at once: nobody fails, nothing is lost, and the end state
// is one real folder plus a link at the old path (all three folders).
func TestAttackMigrateConcurrentProcesses(t *testing.T) {
	skipWithoutSymlinks(t)
	for run := range 6 {
		root := legacyEnv(t)
		for _, k := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME"} {
			old := filepath.Join(os.Getenv(k), LegacyAppName)
			for _, f := range []string{"a", "store.db", "store.db-wal", "store.db-shm", ".push.lock", "sub/x"} {
				writeFile(t, filepath.Join(old, f), k+f)
			}
		}
		start := filepath.Join(root, "go")
		var wg sync.WaitGroup
		outs := make([]string, 6)
		for i := range outs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				cmd := exec.Command(os.Args[0], "-test.run=^TestMigrateHelperProcess$")
				cmd.Env = append(os.Environ(), "ASAGAO_MIGRATE_HELPER=1", "ASAGAO_MIGRATE_START="+start)
				b, err := cmd.CombinedOutput()
				outs[i] = string(b)
				if err != nil {
					outs[i] += "EXIT:" + err.Error()
				}
			}()
		}
		time.Sleep(300 * time.Millisecond)
		writeFile(t, start, "go")
		wg.Wait()
		for i, o := range outs {
			if !strings.Contains(o, "MIGRATE-OK") || strings.Contains(o, "EXIT:") {
				t.Fatalf("run %d proc %d: %s", run, i, o)
			}
		}
		for _, k := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME"} {
			base := os.Getenv(k)
			nu, old := filepath.Join(base, appName), filepath.Join(base, LegacyAppName)
			if fi, err := os.Lstat(nu); err != nil || !fi.IsDir() {
				t.Fatalf("run %d %s: new is not a real folder: %v", run, k, err)
			}
			if !isSymlink(t, old) {
				t.Fatalf("run %d %s: old is not a link", run, k)
			}
			for _, f := range []string{"a", "store.db", "store.db-wal", "store.db-shm", ".push.lock", "sub/x"} {
				if got := readFile(t, filepath.Join(old, f)); got != k+f {
					t.Fatalf("run %d: %s/%s = %q", run, old, f, got)
				}
			}
		}
	}
}

// Everything moves: dotfiles, WAL/shm, nested folders, inner links, modes and times.
func TestAttackMigrateKeepsEverything(t *testing.T) {
	skipWithoutSymlinks(t)
	legacyEnv(t)
	base := os.Getenv("XDG_DATA_HOME")
	old := filepath.Join(base, LegacyAppName)
	writeFile(t, filepath.Join(old, "store.db"), "db")
	writeFile(t, filepath.Join(old, "store.db-wal"), "wal")
	writeFile(t, filepath.Join(old, ".hidden", "deep", "f"), "deep")
	if err := os.Symlink("store.db", filepath.Join(old, "inner-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(old, 0o750); err != nil {
		t.Fatal(err)
	}
	mt := time.Unix(1_600_000_000, 0)
	os.Chtimes(filepath.Join(old, "store.db"), mt, mt)
	if err := MigrateLegacyDirs(); err != nil {
		t.Fatal(err)
	}
	nu := DataDir()
	for f, want := range map[string]string{"store.db": "db", "store.db-wal": "wal", ".hidden/deep/f": "deep", "inner-link": "db"} {
		if got := readFile(t, filepath.Join(nu, f)); got != want {
			t.Errorf("%s = %q", f, got)
		}
	}
	if fi, _ := os.Stat(nu); fi.Mode().Perm() != 0o750 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(filepath.Join(nu, "store.db")); !fi.ModTime().Equal(mt) {
		t.Errorf("mtime changed: %v", fi.ModTime())
	}
	// an old binary writing through the old path lands in the new folder
	writeFile(t, filepath.Join(old, "store.db-shm"), "shm")
	if got := readFile(t, filepath.Join(nu, "store.db-shm")); got != "shm" {
		t.Errorf("write through the link: %q", got)
	}
}

// An open file handle (an old process running during the move) keeps writing into the
// same file.
func TestAttackMigrateOpenHandleSurvives(t *testing.T) {
	legacyEnv(t)
	old := filepath.Join(os.Getenv("XDG_DATA_HOME"), LegacyAppName)
	writeFile(t, filepath.Join(old, "outbox.log"), "1\n")
	f, err := os.OpenFile(filepath.Join(old, "outbox.log"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := MigrateLegacyDirs(); err != nil {
		t.Fatal(err)
	}
	f.WriteString("2\n")
	if got := readFile(t, filepath.Join(DataDir(), "outbox.log")); got != "1\n2\n" {
		t.Errorf("got %q", got)
	}
}

// Running again changes nothing and reports nothing.
func TestAttackMigrateIdempotent(t *testing.T) {
	skipWithoutSymlinks(t)
	legacyEnv(t)
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), LegacyAppName, "config.toml"), "x=1")
	for i := range 3 {
		if err := MigrateLegacyDirs(); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	if got := readFile(t, filepath.Join(ConfigDir(), "config.toml")); got != "x=1" {
		t.Errorf("got %q", got)
	}
	if fi, _ := os.Lstat(ConfigDir()); !fi.IsDir() {
		t.Error("new is not a real folder")
	}
}

// Both folders hold an outbox: the new one is used, the old one is byte-for-byte untouched
// (no merge, no delete, not turned into a link).
func TestAttackMigrateBothExistLeavesOldUntouched(t *testing.T) {
	legacyEnv(t)
	base := os.Getenv("XDG_DATA_HOME")
	writeFile(t, filepath.Join(base, LegacyAppName, "store.db"), "OLD")
	writeFile(t, filepath.Join(base, LegacyAppName, "store.db-wal"), "OLDWAL")
	writeFile(t, filepath.Join(base, appName, "store.db"), "NEW")
	for range 2 {
		if err := MigrateLegacyDirs(); err != nil {
			t.Fatal(err)
		}
	}
	if readFile(t, filepath.Join(base, LegacyAppName, "store.db")) != "OLD" ||
		readFile(t, filepath.Join(base, LegacyAppName, "store.db-wal")) != "OLDWAL" ||
		readFile(t, filepath.Join(base, appName, "store.db")) != "NEW" {
		t.Error("data was changed")
	}
	if isSymlink(t, filepath.Join(base, LegacyAppName)) {
		t.Error("old folder must not be replaced by a link when both exist")
	}
}

// Config, data and cache point at the same base: the first move wins, the others see the
// new folder and do nothing; no data lost.
func TestAttackMigrateSameBaseForAll(t *testing.T) {
	skipWithoutSymlinks(t)
	base := t.TempDir()
	for _, k := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME"} {
		t.Setenv(k, base)
	}
	writeFile(t, filepath.Join(base, LegacyAppName, "config.toml"), "c")
	writeFile(t, filepath.Join(base, LegacyAppName, "store.db"), "d")
	if err := MigrateLegacyDirs(); err != nil {
		t.Fatal(err)
	}
	if readFile(t, filepath.Join(ConfigDir(), "config.toml")) != "c" || readFile(t, filepath.Join(DataDir(), "store.db")) != "d" {
		t.Error("data lost")
	}
}

// A relative XDG path: the link at the old path must resolve whatever the working folder.
func TestAttackMigrateRelativeXDG(t *testing.T) {
	skipWithoutSymlinks(t)
	wd, _ := os.Getwd()
	root := t.TempDir()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(wd) })
	t.Setenv("XDG_CONFIG_HOME", "rel/conf")
	t.Setenv("XDG_DATA_HOME", "rel/data/") // trailing slash
	t.Setenv("XDG_CACHE_HOME", "rel/cache")
	writeFile(t, filepath.Join("rel/data", LegacyAppName, "store.db"), "db")
	if err := MigrateLegacyDirs(); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(root, "rel/data", LegacyAppName, "store.db")); got != "db" {
		t.Errorf("through the old link: %q", got)
	}
	os.Chdir(t.TempDir()) // the link must not depend on the working folder
	if got := readFile(t, filepath.Join(root, "rel/data", LegacyAppName, "store.db")); got != "db" {
		t.Errorf("after chdir: %q", got)
	}
}

// Odd old paths: nothing deleted, no panic.
func TestAttackMigrateOddOldPaths(t *testing.T) {
	skipWithoutSymlinks(t)
	t.Run("link to a file", func(t *testing.T) {
		root := legacyEnv(t)
		target := filepath.Join(root, "afile")
		writeFile(t, target, "keep")
		old := filepath.Join(os.Getenv("XDG_DATA_HOME"), LegacyAppName)
		os.Symlink(target, old)
		if err := MigrateLegacyDirs(); err == nil {
			t.Error("want a warning")
		}
		if readFile(t, target) != "keep" {
			t.Error("file changed")
		}
	})
	t.Run("link loop", func(t *testing.T) {
		legacyEnv(t)
		old := filepath.Join(os.Getenv("XDG_DATA_HOME"), LegacyAppName)
		os.Symlink(LegacyAppName, old)
		_ = MigrateLegacyDirs()
	})
	t.Run("new is a dangling link", func(t *testing.T) {
		legacyEnv(t)
		base := os.Getenv("XDG_DATA_HOME")
		writeFile(t, filepath.Join(base, LegacyAppName, "store.db"), "db")
		os.Symlink(filepath.Join(base, "gone"), filepath.Join(base, appName))
		_ = MigrateLegacyDirs()
		if readFile(t, filepath.Join(base, LegacyAppName, "store.db")) != "db" {
			t.Error("old data lost")
		}
	})
	t.Run("new is a file", func(t *testing.T) {
		legacyEnv(t)
		base := os.Getenv("XDG_DATA_HOME")
		writeFile(t, filepath.Join(base, LegacyAppName, "store.db"), "db")
		writeFile(t, filepath.Join(base, appName), "file")
		_ = MigrateLegacyDirs()
		if readFile(t, filepath.Join(base, LegacyAppName, "store.db")) != "db" {
			t.Error("old data lost")
		}
	})
	t.Run("old is a link to a folder elsewhere, run twice", func(t *testing.T) {
		root := legacyEnv(t)
		dot := filepath.Join(root, "dot")
		writeFile(t, filepath.Join(dot, "store.db"), "db")
		base := os.Getenv("XDG_DATA_HOME")
		os.Symlink(dot, filepath.Join(base, LegacyAppName))
		for range 2 {
			if err := MigrateLegacyDirs(); err != nil {
				t.Fatal(err)
			}
		}
		writeFile(t, filepath.Join(DataDir(), "new"), "n")
		if readFile(t, filepath.Join(dot, "new")) != "n" || readFile(t, filepath.Join(base, LegacyAppName, "new")) != "n" {
			t.Error("old and new paths do not share one folder")
		}
	})
}

// One folder failing must not stop the others.
func TestAttackMigrateOneFailureDoesNotStopOthers(t *testing.T) {
	skipWithoutSymlinks(t)
	legacyEnv(t)
	writeFile(t, filepath.Join(os.Getenv("XDG_DATA_HOME"), LegacyAppName), "a file")
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), LegacyAppName, "config.toml"), "c")
	if err := MigrateLegacyDirs(); err == nil {
		t.Error("want a warning")
	}
	if readFile(t, filepath.Join(ConfigDir(), "config.toml")) != "c" {
		t.Error("config not migrated")
	}
}
