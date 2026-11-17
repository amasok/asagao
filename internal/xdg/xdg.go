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
	if v := os.Getenv(env); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, fallback)
}
