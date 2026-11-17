// Package xdg says where the tool's folders are, following the XDG base directories (also
// on macOS): $XDG_CONFIG_HOME/asagao or ~/.config/asagao, and the same for data and cache.
//
// It reads only environment variables and the home directory, and creates nothing, so any
// layer may use it (like termsafe): config, and the sync engine for where attachments are
// downloaded by default.
package xdg

import (
	"os"
	"path/filepath"
)

// AppName is the name of the tool's own folder in each base directory.
const AppName = "asagao"

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

// base is $env, or fallback in the home folder ("." when there is no home folder).
func base(env, fallback string) string {
	if v := os.Getenv(env); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, fallback)
}
