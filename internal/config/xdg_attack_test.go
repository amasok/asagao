package config

import (
	"os"
	"path/filepath"
	"testing"
)

// The pre-xdg implementation, to compare against.
func oldXDGDir(env, fallback string) string {
	base := os.Getenv(env)
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "."
		}
		base = filepath.Join(home, fallback)
	}
	return filepath.Join(base, "asagao")
}

func TestDirsSameAsBeforeForOddEnvironments(t *testing.T) {
	for _, tc := range []struct{ xdg, home string }{
		{"", "/home/u"}, {"/x/y", "/home/u"}, {"rel/dir", "/home/u"}, {"/x/y/", "/home/u"}, {"/x//y/../z", "/home/u"},
		{" ", "/home/u"}, {"~/c", "/home/u"}, {"", ""}, {"/x", ""}, {"", "/home/u/"}, {"", "relative-home"},
	} {
		t.Run(tc.xdg+"|"+tc.home, func(t *testing.T) {
			for _, k := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME"} {
				t.Setenv(k, tc.xdg)
			}
			t.Setenv("HOME", tc.home)
			if got, want := ConfigDir(), oldXDGDir("XDG_CONFIG_HOME", ".config"); got != want {
				t.Errorf("ConfigDir = %q, want %q", got, want)
			}
			if got, want := DataDir(), oldXDGDir("XDG_DATA_HOME", filepath.Join(".local", "share")); got != want {
				t.Errorf("DataDir = %q, want %q", got, want)
			}
			if got, want := CacheDir(), oldXDGDir("XDG_CACHE_HOME", ".cache"); got != want {
				t.Errorf("CacheDir = %q, want %q", got, want)
			}
		})
	}
}
