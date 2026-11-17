// Package settings holds asagao's settings as types: what config.toml sets (internal/config
// reads it) and the defaults. It has no file or TOML code, so the entry points can take the
// settings without importing the config adapter (the shared types layer, like readmodel).
package settings

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/amasok/asagao/internal/xdg"
)

// Token store backends.
const (
	TokenStoreKeychain  = "keychain"
	TokenStore1Password = "1password"
	TokenStoreFile      = "file"
)

// Config is all the settings.
type Config struct {
	Auth AuthConfig `toml:"auth"`
	Sync SyncConfig `toml:"sync"`
	UI   UIConfig   `toml:"ui"`
	MCP  MCPConfig  `toml:"mcp"`
	// Keys rebinds TUI shortcuts: action name = "key" or ["key", ...] (see `asagao keys`).
	Keys map[string]any `toml:"keys"`
}

// KeyBindings returns [keys] as action -> keys.
func (c Config) KeyBindings() (map[string][]string, error) {
	out := map[string][]string{}
	for action, v := range c.Keys {
		switch v := v.(type) {
		case string:
			out[action] = []string{v}
		case []any:
			for _, k := range v {
				s, ok := k.(string)
				if !ok {
					return nil, fmt.Errorf("keys.%s: keys must be strings", action)
				}
				out[action] = append(out[action], s)
			}
		default:
			return nil, fmt.Errorf("keys.%s: want a string or a list of strings", action)
		}
	}
	return out, nil
}

type UIConfig struct {
	// ImageProtocol is how images are shown: "auto" (detect the terminal), "iterm"
	// (iTerm2 / WezTerm), "kitty" (kitty / Ghostty) or "open" (the OS image viewer).
	ImageProtocol string `toml:"image_protocol"`
	// InlineImages draws images inside the detail pane where they appear in the text:
	// "auto" (when the image protocol is kitty), "on" or "off".
	InlineImages string `toml:"inline_images"`
	// Editor is the command used to edit tasks and comments (default: $VISUAL, $EDITOR, nvim).
	Editor string `toml:"editor"`
	// Theme is "auto" (follow the terminal background), "dark" or "light".
	Theme string `toml:"theme"`
	// Accent is the accent color, e.g. "#7aa2f7" ("" = default).
	Accent string `toml:"accent"`
}

type MCPConfig struct {
	// UploadDirs are folders (besides the current folder, ~/Downloads and ~/Desktop) whose
	// files the MCP tool upload_attachment may send to Asana. "~/" is the home folder.
	UploadDirs []string `toml:"upload_dirs"`
}

// ExpandedUploadDirs returns [mcp] upload_dirs with "~" replaced by the home folder.
func (c MCPConfig) ExpandedUploadDirs() []string {
	home, _ := os.UserHomeDir()
	var out []string
	for _, d := range c.UploadDirs {
		if d == "~" || strings.HasPrefix(d, "~/") {
			if home == "" {
				continue
			}
			d = filepath.Join(home, strings.TrimPrefix(d, "~"))
		}
		out = append(out, filepath.Clean(d))
	}
	return out
}

type AuthConfig struct {
	// ClientID of the OAuth app registered in the Asana developer console.
	ClientID string `toml:"client_id"`
	// RedirectPort is the localhost port for the OAuth callback.
	// The redirect URL http://localhost:<port>/callback must be registered on the app.
	RedirectPort int `toml:"redirect_port"`
	// Scopes to request. Empty means the app's "Full permissions" (default scope).
	Scopes []string `toml:"scopes"`
	// TokenStore is one of "keychain", "1password", "file".
	TokenStore  string            `toml:"token_store"`
	OnePassword OnePasswordConfig `toml:"onepassword"`
}

type OnePasswordConfig struct {
	Vault string `toml:"vault"`
	Item  string `toml:"item"`
	// LegacyItem is the item read when Item has nothing yet: the default item name before
	// the rename (asana-tui). Empty when config.toml sets item (a name chosen is kept).
	LegacyItem string `toml:"-"`
	Account    string `toml:"account"` // optional, for multiple 1Password accounts
}

type SyncConfig struct {
	// CompletedDays is how many days of completed tasks to keep in the local store.
	CompletedDays int `toml:"completed_days"`
	// AllProjects syncs every project in the workspace instead of only projects you are a member of.
	AllProjects bool `toml:"all_projects"`
}

// Default is the settings when config.toml sets nothing.
func Default() Config {
	return Config{
		Auth: AuthConfig{
			RedirectPort: 8976,
			TokenStore:   TokenStoreKeychain,
			OnePassword:  OnePasswordConfig{Vault: "Private", Item: xdg.AppName, LegacyItem: xdg.LegacyAppName},
		},
		Sync: SyncConfig{CompletedDays: 14},
		UI:   UIConfig{ImageProtocol: "auto", InlineImages: "auto"},
	}
}
