package xdg

import (
	"path/filepath"
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
