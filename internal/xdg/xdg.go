// Package xdg says where the tool's folders are, following the XDG base directories (also
// on macOS): $XDG_CONFIG_HOME/asagao or ~/.config/asagao, and the same for data and cache.
//
// It reads only environment variables and the home directory, and creates nothing, so any
// layer may use it (like termsafe): config, and the sync engine for where attachments are
// downloaded by default.
//
// In a test binary it refuses to give the user's own folders (see guard): the tests run
// with HOME and the XDG folders moved to a temporary directory (internal/testguard), and a
// folder of the real home means that something bypassed that, which once cost a user their
// store.db (Issue #53).
package xdg

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

// AppName is the name of the tool's own folder in each base directory.
const AppName = "asagao"

// LegacyAppName is the tool's former name (its folders are moved to the new ones; its
// keychain entry and 1Password item are read when nothing is saved under the new name).
const LegacyAppName = "asana-tui"

// ConfigHome, DataHome and CacheHome are the base directories the tool's folders are made
// in: $XDG_CONFIG_HOME, $XDG_DATA_HOME, $XDG_CACHE_HOME, or their defaults in the home
// folder (~/.config, ~/.local/share, ~/.cache).
func ConfigHome() string { return base("XDG_CONFIG_HOME", ".config") }
func DataHome() string   { return base("XDG_DATA_HOME", filepath.Join(".local", "share")) }
func CacheHome() string  { return base("XDG_CACHE_HOME", ".cache") }

// ConfigDir is $XDG_CONFIG_HOME/asagao or ~/.config/asagao.
func ConfigDir() string { return filepath.Join(ConfigHome(), AppName) }

// DataDir is $XDG_DATA_HOME/asagao or ~/.local/share/asagao.
func DataDir() string { return filepath.Join(DataHome(), AppName) }

// CacheDir is $XDG_CACHE_HOME/asagao or ~/.cache/asagao (downloaded attachments).
func CacheDir() string { return filepath.Join(CacheHome(), AppName) }

// ConfigFile is the settings file, config.toml in ConfigDir.
func ConfigFile() string { return filepath.Join(ConfigDir(), "config.toml") }

// LegacyDirs are the config, data and cache folders under the tool's former name.
func LegacyDirs() []string {
	return []string{
		filepath.Join(ConfigHome(), LegacyAppName),
		filepath.Join(DataHome(), LegacyAppName),
		filepath.Join(CacheHome(), LegacyAppName),
	}
}

// OwnDirs is every folder that holds the tool's own files (the token, the store, downloaded
// attachments): the config, data and cache folders, under the current name and the former
// one (left in place when both exist, or a link to the new folder).
func OwnDirs() []string { return append([]string{ConfigDir(), DataDir(), CacheDir()}, LegacyDirs()...) }

// base is $env, or fallback in the home folder ("." when there is no home folder).
func base(env, fallback string) string {
	b := os.Getenv(env)
	if b == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "."
		}
		b = filepath.Join(home, fallback)
	}
	guard(b)
	return b
}

// TestguardEnv is testguard.EnvDir (xdg uses the standard library only, so it is repeated
// here; a test of testguard checks that they are the same).
const TestguardEnv = "ASAGAO_TESTGUARD_DIR"

// realDirs are, in a test binary, the tool's folders of the user who runs the tests (under
// the current and the former name), as they were before any test could move HOME or the
// XDG folders: package variables are set before TestMain runs. Outside tests it is empty.
var realDirs = userDirs()

// userDirs returns the tool's folders under the default base directories of the real home
// (from $HOME and from the user database, in case they differ) and under the XDG folders
// set when the test binary started (unless it was started by a guarded test), with
// symbolic links resolved as well. It returns nil
// outside a test binary.
func userDirs() []string {
	if !testing.Testing() {
		return nil
	}
	var bases []string
	var homes []string
	if h, err := os.UserHomeDir(); err == nil {
		homes = append(homes, h)
	}
	if u, err := user.Current(); err == nil && u.HomeDir != "" {
		homes = append(homes, u.HomeDir)
	}
	for _, h := range homes {
		bases = append(bases, filepath.Join(h, ".config"), filepath.Join(h, ".local", "share"), filepath.Join(h, ".cache"))
	}
	// A process started by a guarded test (testguard) inherits the test's XDG folders:
	// they are temporary, not the user's.
	if os.Getenv(TestguardEnv) == "" {
		for _, env := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME"} {
			if v := os.Getenv(env); v != "" {
				bases = append(bases, v)
			}
		}
	}
	var out []string
	add := func(d string) {
		d = filepath.Clean(d)
		for _, o := range out {
			if o == d {
				return
			}
		}
		out = append(out, d)
	}
	for _, b := range bases {
		for _, name := range []string{AppName, LegacyAppName} {
			d := filepath.Join(b, name)
			add(d)
			if r, err := filepath.EvalSymlinks(b); err == nil {
				add(filepath.Join(r, name))
			}
		}
	}
	return out
}

// guard panics, in a test binary, when the base directory b would put one of the tool's
// folders at (or inside) one of the user's own (realDirs): a test must never read or write
// the user's store, settings or token. The tests point HOME and the XDG folders at a
// temporary directory first (internal/testguard); a test that sets its own XDG folders
// uses t.TempDir. Symbolic links in b are resolved too, so a link to the real folder is
// caught.
func guard(b string) {
	if len(realDirs) == 0 {
		return
	}
	cands := []string{filepath.Clean(b)}
	if r, err := filepath.EvalSymlinks(b); err == nil {
		cands = append(cands, r)
	}
	for _, c := range cands {
		for _, name := range []string{AppName, LegacyAppName} {
			d := filepath.Join(c, name)
			for _, real := range realDirs {
				if within(d, real) {
					panic(fmt.Sprintf("xdg: a test reached the user's own folder %s (base directory %q): point HOME and the XDG folders at a temporary directory (internal/testguard, Issue #53)", real, b))
				}
			}
		}
	}
}

// within reports whether path p is dir or inside it.
func within(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
