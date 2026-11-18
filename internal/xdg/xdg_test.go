package xdg

import (
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

func TestDirs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, env := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME"} {
		t.Setenv(env, "")
	}
	for _, tc := range []struct {
		name string
		got  func() string
		want string
	}{
		{"config home", ConfigHome, filepath.Join(home, ".config")},
		{"data home", DataHome, filepath.Join(home, ".local", "share")},
		{"cache home", CacheHome, filepath.Join(home, ".cache")},
		{"config", ConfigDir, filepath.Join(home, ".config", "asagao")},
		{"data", DataDir, filepath.Join(home, ".local", "share", "asagao")},
		{"cache", CacheDir, filepath.Join(home, ".cache", "asagao")},
	} {
		if got := tc.got(); got != tc.want {
			t.Errorf("%s without XDG: %q, want %q", tc.name, got, tc.want)
		}
	}
	t.Setenv("XDG_CONFIG_HOME", "/x/config")
	t.Setenv("XDG_DATA_HOME", "/x/data")
	t.Setenv("XDG_CACHE_HOME", "/x/cache")
	for _, tc := range []struct {
		name string
		got  func() string
		want string
	}{
		{"config", ConfigDir, filepath.Join("/x/config", "asagao")},
		{"data", DataDir, filepath.Join("/x/data", "asagao")},
		{"cache", CacheDir, filepath.Join("/x/cache", "asagao")},
		{"cache home", CacheHome, "/x/cache"},
	} {
		if got := tc.got(); got != tc.want {
			t.Errorf("%s with XDG: %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestConfigFileAndOwnDirs(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/c")
	t.Setenv("XDG_DATA_HOME", "/d")
	t.Setenv("XDG_CACHE_HOME", "/k")
	if got := ConfigFile(); got != filepath.Join("/c", "asagao", "config.toml") {
		t.Errorf("ConfigFile = %q", got)
	}
	want := []string{"/c/asagao", "/d/asagao", "/k/asagao", "/c/asana-tui", "/d/asana-tui", "/k/asana-tui"}
	got := OwnDirs()
	if len(got) != len(want) {
		t.Fatalf("OwnDirs = %q", got)
	}
	for i := range want {
		if got[i] != filepath.FromSlash(want[i]) {
			t.Errorf("OwnDirs[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// realHome is the home folder of the user who runs the tests, from the user database (HOME
// is a temporary directory in the tests). Only its path is used: nothing reads or writes in
// it.
func realHome(t *testing.T) string {
	t.Helper()
	u, err := user.Current()
	if err != nil || u.HomeDir == "" {
		t.Skip("no home folder in the user database:", err)
	}
	return u.HomeDir
}

// wantPanic calls f and fails unless it panics with the guard's message.
func wantPanic(t *testing.T, what string, f func() string) {
	t.Helper()
	defer func() {
		t.Helper()
		r := recover()
		if r == nil {
			return
		}
		if msg, _ := r.(string); !strings.Contains(msg, "a test reached the user's own folder") {
			panic(r)
		}
		what = ""
	}()
	got := f()
	if what != "" {
		t.Errorf("%s gave %q in a test instead of stopping (Issue #53)", what, got)
	}
}

// A test that reaches one of the user's own folders is stopped, however it gets there:
// HOME back at the real home, an XDG folder set to the real base directory, or a link to
// it. This is what would have stopped the test that wrote over the user's store.db
// (Issue #53). The functions only compute paths: nothing is read or written.
func TestGuardStopsTheRealFolders(t *testing.T) {
	home := realHome(t)
	all := map[string]func() string{
		"ConfigDir": ConfigDir, "DataDir": DataDir, "CacheDir": CacheDir, "ConfigFile": ConfigFile,
		"ConfigHome": ConfigHome, "DataHome": DataHome, "CacheHome": CacheHome,
		"LegacyDirs": func() string { return strings.Join(LegacyDirs(), ",") },
		"OwnDirs":    func() string { return strings.Join(OwnDirs(), ",") },
	}

	t.Run("real HOME", func(t *testing.T) {
		t.Setenv("HOME", home)
		for _, env := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME"} {
			t.Setenv(env, "")
		}
		for name, f := range all {
			wantPanic(t, name, f)
		}
	})

	for env, base := range map[string]string{
		"XDG_CONFIG_HOME": filepath.Join(home, ".config"),
		"XDG_DATA_HOME":   filepath.Join(home, ".local", "share"),
		"XDG_CACHE_HOME":  filepath.Join(home, ".cache"),
	} {
		for _, v := range []string{base, base + "/", base + "/./", filepath.Join(base, "..", filepath.Base(base))} {
			t.Run(env+"="+v, func(t *testing.T) {
				t.Setenv(env, v)
				wantPanic(t, "OwnDirs", func() string { return strings.Join(OwnDirs(), ",") })
			})
		}
		t.Run(env+" link", func(t *testing.T) {
			if _, err := os.Stat(base); err != nil {
				t.Skip("no", base, "on this machine")
			}
			link := filepath.Join(t.TempDir(), "base")
			if err := os.Symlink(base, link); err != nil {
				t.Skip("no symbolic links:", err)
			}
			t.Setenv(env, link)
			wantPanic(t, "OwnDirs", func() string { return strings.Join(OwnDirs(), ",") })
		})
	}
	// The tool's own folders, not their base directories: a folder inside the real
	// ~/.local/share/asagao is stopped too.
	t.Run("inside", func(t *testing.T) {
		t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share", "asagao", "x"))
		wantPanic(t, "DataDir", DataDir)
	})
}

// The guard stops only the user's folders: a temporary base directory, or a folder next to
// the real ones, is given as usual.
func TestGuardLetsOtherFoldersThrough(t *testing.T) {
	home := realHome(t)
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share-not"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache", "other"))
	if got := ConfigDir(); got != filepath.Join(tmp, AppName) {
		t.Errorf("ConfigDir = %q", got)
	}
	if got := DataDir(); got != filepath.Join(home, ".local", "share-not", AppName) {
		t.Errorf("DataDir = %q", got)
	}
	if got := CacheDir(); got != filepath.Join(home, ".cache", "other", AppName) {
		t.Errorf("CacheDir = %q", got)
	}
}

func TestWithin(t *testing.T) {
	for _, tc := range []struct {
		p, dir string
		want   bool
	}{
		{"/a/b", "/a/b", true},
		{"/a/b/c", "/a/b", true},
		{"/a/bc", "/a/b", false},
		{"/a", "/a/b", false},
		{"/x/b", "/a/b", false},
		{"/a/..b", "/a", true},
	} {
		if got := within(tc.p, tc.dir); got != tc.want {
			t.Errorf("within(%q, %q) = %v, want %v", tc.p, tc.dir, got, tc.want)
		}
	}
}
