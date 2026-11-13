// Package config loads asana-tui settings from ~/.config/asana-tui/config.toml.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"

	"github.com/pelletier/go-toml/v2"
)

const appName = "asana-tui"

// Token store backends.
const (
	TokenStoreKeychain  = "keychain"
	TokenStore1Password = "1password"
	TokenStoreFile      = "file"
)

type Config struct {
	Auth AuthConfig `toml:"auth"`
	Sync SyncConfig `toml:"sync"`
	UI   UIConfig   `toml:"ui"`
	// Keys rebinds TUI shortcuts: action name = "key" or ["key", ...] (see `asana-tui keys`).
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
	Vault   string `toml:"vault"`
	Item    string `toml:"item"`
	Account string `toml:"account"` // optional, for multiple 1Password accounts
}

type SyncConfig struct {
	// CompletedDays is how many days of completed tasks to keep in the local store.
	CompletedDays int `toml:"completed_days"`
	// AllProjects syncs every project in the workspace instead of only projects you are a member of.
	AllProjects bool `toml:"all_projects"`
}

func Default() Config {
	return Config{
		Auth: AuthConfig{
			RedirectPort: 8976,
			TokenStore:   TokenStoreKeychain,
			OnePassword:  OnePasswordConfig{Vault: "Private", Item: appName},
		},
		Sync: SyncConfig{CompletedDays: 14},
		UI:   UIConfig{ImageProtocol: "auto", InlineImages: "auto"},
	}
}

// Load reads the config file, falling back to defaults when it does not exist.
// ASANA_CLIENT_ID overrides auth.client_id.
func Load() (Config, error) {
	cfg := Default()
	path := FilePath()
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return cfg, err
	default:
		if err := parse(b, &cfg); err != nil {
			return cfg, fmt.Errorf("parse %s: %w", path, err)
		}
	}
	if v := os.Getenv("ASANA_CLIENT_ID"); v != "" {
		cfg.Auth.ClientID = v
	}
	return cfg, cfg.validate()
}

// parse decodes TOML into cfg. Asana client IDs are all digits, so an unquoted
// `client_id = 123` (a TOML integer) is accepted as well as the string form.
func parse(b []byte, cfg *Config) error {
	var raw map[string]any
	if err := toml.Unmarshal(b, &raw); err != nil {
		return err
	}
	if a, ok := raw["auth"].(map[string]any); ok {
		if n, ok := a["client_id"].(int64); ok {
			a["client_id"] = strconv.FormatInt(n, 10)
		}
	}
	normalized, err := toml.Marshal(raw)
	if err != nil {
		return err
	}
	return toml.Unmarshal(normalized, cfg)
}

func (c Config) validate() error {
	switch c.Auth.TokenStore {
	case TokenStoreKeychain, TokenStore1Password, TokenStoreFile:
	default:
		return fmt.Errorf("auth.token_store: unknown value %q (want keychain, 1password or file)", c.Auth.TokenStore)
	}
	switch c.UI.ImageProtocol {
	case "auto", "iterm", "kitty", "open":
	default:
		return fmt.Errorf("ui.image_protocol: unknown value %q (want auto, iterm, kitty or open)", c.UI.ImageProtocol)
	}
	switch c.UI.Theme {
	case "", "auto", "dark", "light":
	default:
		return fmt.Errorf("ui.theme: unknown value %q (want auto, dark or light)", c.UI.Theme)
	}
	if _, err := c.KeyBindings(); err != nil {
		return err
	}
	switch c.UI.InlineImages {
	case "auto", "on", "off":
	default:
		return fmt.Errorf("ui.inline_images: unknown value %q (want auto, on or off)", c.UI.InlineImages)
	}
	if c.Auth.RedirectPort <= 0 || c.Auth.RedirectPort > 65535 {
		return fmt.Errorf("auth.redirect_port: invalid port %d", c.Auth.RedirectPort)
	}
	return nil
}

// ConfigDir follows XDG: $XDG_CONFIG_HOME/asana-tui or ~/.config/asana-tui (also on macOS).
func ConfigDir() string { return xdgDir("XDG_CONFIG_HOME", ".config") }

// DataDir follows XDG: $XDG_DATA_HOME/asana-tui or ~/.local/share/asana-tui.
func DataDir() string { return xdgDir("XDG_DATA_HOME", filepath.Join(".local", "share")) }

// CacheDir follows XDG: $XDG_CACHE_HOME/asana-tui or ~/.cache/asana-tui (downloaded attachments).
func CacheDir() string { return xdgDir("XDG_CACHE_HOME", ".cache") }

func FilePath() string { return filepath.Join(ConfigDir(), "config.toml") }

func xdgDir(env, fallback string) string {
	if v := os.Getenv(env); v != "" {
		return filepath.Join(v, appName)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, fallback, appName)
}
