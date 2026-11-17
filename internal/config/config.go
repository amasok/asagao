// Package config loads asagao settings from ~/.config/asagao/config.toml.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/amasok/asagao/internal/settings"
	"github.com/amasok/asagao/internal/xdg"
)

const appName = xdg.AppName

// The settings' types are internal/settings's; config reads them from config.toml.
type (
	Config            = settings.Config
	UIConfig          = settings.UIConfig
	MCPConfig         = settings.MCPConfig
	AuthConfig        = settings.AuthConfig
	OnePasswordConfig = settings.OnePasswordConfig
	SyncConfig        = settings.SyncConfig
)

// Token store backends (settings').
const (
	TokenStoreKeychain  = settings.TokenStoreKeychain
	TokenStore1Password = settings.TokenStore1Password
	TokenStoreFile      = settings.TokenStoreFile
)

// Default is settings.Default.
func Default() Config { return settings.Default() }

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
	return cfg, validate(cfg)
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
		if op, ok := a["onepassword"].(map[string]any); ok {
			if _, ok := op["item"]; ok {
				cfg.Auth.OnePassword.LegacyItem = ""
			}
		}
	}
	normalized, err := toml.Marshal(raw)
	if err != nil {
		return err
	}
	return toml.Unmarshal(normalized, cfg)
}

func validate(c Config) error {
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
	for _, d := range c.MCP.UploadDirs {
		if d != "~" && !strings.HasPrefix(d, "~/") && !filepath.IsAbs(d) {
			return fmt.Errorf("mcp.upload_dirs: %q must be an absolute path or start with ~/", d)
		}
	}
	if c.Auth.RedirectPort <= 0 || c.Auth.RedirectPort > 65535 {
		return fmt.Errorf("auth.redirect_port: invalid port %d", c.Auth.RedirectPort)
	}
	return nil
}

// ConfigDir follows XDG: $XDG_CONFIG_HOME/asagao or ~/.config/asagao (also on macOS).
func ConfigDir() string { return xdg.ConfigDir() }

// DataDir follows XDG: $XDG_DATA_HOME/asagao or ~/.local/share/asagao.
func DataDir() string { return xdg.DataDir() }

// CacheDir follows XDG: $XDG_CACHE_HOME/asagao or ~/.cache/asagao (downloaded attachments).
func CacheDir() string { return xdg.CacheDir() }

func FilePath() string { return xdg.ConfigFile() }
