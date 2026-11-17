package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad(t *testing.T) {
	for _, tc := range []struct {
		name, body, wantID, wantStore string
	}{
		{"quoted client_id", "[auth]\nclient_id = \"1220505655375676\"\n", "1220505655375676", TokenStoreKeychain},
		{"unquoted client_id", "[auth]\nclient_id = 1220505655375676\ntoken_store = \"1password\"\n", "1220505655375676", TokenStore1Password},
		{"no auth section", "[sync]\ncompleted_days = 3\n", "", TokenStoreKeychain},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", dir)
			t.Setenv("ASANA_CLIENT_ID", "")
			if err := os.MkdirAll(filepath.Join(dir, appName), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(FilePath(), []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Auth.ClientID != tc.wantID || cfg.Auth.TokenStore != tc.wantStore {
				t.Fatalf("got client_id=%q token_store=%q", cfg.Auth.ClientID, cfg.Auth.TokenStore)
			}
			// Defaults survive a partial file.
			if cfg.Auth.RedirectPort != 8976 || cfg.Auth.OnePassword.Vault != "Private" {
				t.Fatalf("defaults lost: %+v", cfg.Auth)
			}
		})
	}
}

func TestKeysAndTheme(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	_ = os.MkdirAll(filepath.Join(dir, appName), 0o700)
	write := func(body string) {
		if err := os.WriteFile(FilePath(), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("[ui]\ntheme = \"dark\"\naccent = \"#7aa2f7\"\n[keys]\ncomplete = \"d\"\ndue = [\"D\", \"ctrl+t\"]\n")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	keys, _ := cfg.KeyBindings()
	if cfg.UI.Theme != "dark" || cfg.UI.Accent != "#7aa2f7" || keys["complete"][0] != "d" || len(keys["due"]) != 2 {
		t.Fatalf("cfg = %+v keys = %v", cfg.UI, keys)
	}
	write("[ui]\ntheme = \"blue\"\n")
	if _, err := Load(); err == nil {
		t.Fatal("unknown theme should fail")
	}
	write("[keys]\ncomplete = 1\n")
	if _, err := Load(); err == nil {
		t.Fatal("non-string key should fail")
	}
}

// loadWith writes body as the config file (nil: no file) and loads it.
func loadWith(t *testing.T, body *string) (Config, error) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if body != nil {
		if err := os.MkdirAll(filepath.Join(dir, appName), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(FilePath(), []byte(*body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return Load()
}

func TestValidate(t *testing.T) {
	t.Setenv("ASANA_CLIENT_ID", "")
	for _, tc := range []struct {
		body    string
		errPart string // "" = valid
	}{
		{"[auth]\nredirect_port = 1\n", ""},
		{"[auth]\nredirect_port = 65535\n", ""},
		{"[auth]\nredirect_port = 0\n", "auth.redirect_port"},
		{"[auth]\nredirect_port = -1\n", "auth.redirect_port"},
		{"[auth]\nredirect_port = 65536\n", "auth.redirect_port"},
		{"[auth]\ntoken_store = \"keychain\"\n", ""},
		{"[auth]\ntoken_store = \"1password\"\n", ""},
		{"[auth]\ntoken_store = \"file\"\n", ""},
		{"[auth]\ntoken_store = \"vault\"\n", "auth.token_store"},
		{"[auth]\ntoken_store = \"\"\n", "auth.token_store"},
		{"[auth]\ntoken_store = \"Keychain\"\n", "auth.token_store"},
		{"[ui]\nimage_protocol = \"auto\"\n", ""},
		{"[ui]\nimage_protocol = \"iterm\"\n", ""},
		{"[ui]\nimage_protocol = \"kitty\"\n", ""},
		{"[ui]\nimage_protocol = \"open\"\n", ""},
		{"[ui]\nimage_protocol = \"sixel\"\n", "ui.image_protocol"},
		{"[ui]\nimage_protocol = \"\"\n", "ui.image_protocol"},
		{"[ui]\ninline_images = \"on\"\n", ""},
		{"[ui]\ninline_images = \"off\"\n", ""},
		{"[ui]\ninline_images = \"yes\"\n", "ui.inline_images"},
		{"[ui]\ntheme = \"\"\n", ""},
		{"[ui]\ntheme = \"auto\"\n", ""},
		{"[ui]\ntheme = \"light\"\n", ""},
		{"[ui]\ntheme = \"blue\"\n", "ui.theme"},
		{"[keys]\ndue = [\"D\", 2]\n", "keys.due"},
		{"[keys]\ndue = {a = \"b\"}\n", "keys.due"},
		{"[auth\n", "parse "},
	} {
		body := tc.body
		_, err := loadWith(t, &body)
		switch {
		case tc.errPart == "" && err != nil:
			t.Errorf("%q: unexpected error %v", tc.body, err)
		case tc.errPart != "" && (err == nil || !strings.Contains(err.Error(), tc.errPart)):
			t.Errorf("%q: err = %v, want one mentioning %q", tc.body, err, tc.errPart)
		}
	}
}

func TestLoadWithoutFile(t *testing.T) {
	t.Setenv("ASANA_CLIENT_ID", "")
	cfg, err := loadWith(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := Default()
	if cfg.Auth.RedirectPort != want.Auth.RedirectPort || cfg.Auth.TokenStore != want.Auth.TokenStore ||
		cfg.Sync.CompletedDays != 14 || cfg.UI.ImageProtocol != "auto" || cfg.UI.InlineImages != "auto" || cfg.Auth.ClientID != "" {
		t.Fatalf("cfg = %+v, want defaults", cfg)
	}
}

func TestClientIDFromEnv(t *testing.T) {
	t.Setenv("ASANA_CLIENT_ID", "999")
	body := "[auth]\nclient_id = \"111\"\n"
	if cfg, err := loadWith(t, &body); err != nil || cfg.Auth.ClientID != "999" {
		t.Fatalf("env should override the file: %q, %v", cfg.Auth.ClientID, err)
	}
	if cfg, err := loadWith(t, nil); err != nil || cfg.Auth.ClientID != "999" {
		t.Fatalf("env without a file: %q, %v", cfg.Auth.ClientID, err)
	}
	// The override does not skip validation.
	bad := "[auth]\nredirect_port = 0\n"
	if _, err := loadWith(t, &bad); err == nil {
		t.Fatal("invalid config with ASANA_CLIENT_ID should still fail")
	}
	t.Setenv("ASANA_CLIENT_ID", "")
	if cfg, err := loadWith(t, &body); err != nil || cfg.Auth.ClientID != "111" {
		t.Fatalf("empty env should keep the file value: %q, %v", cfg.Auth.ClientID, err)
	}
}

func TestDirs(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/x/conf")
	t.Setenv("XDG_DATA_HOME", "/x/data")
	t.Setenv("XDG_CACHE_HOME", "/x/cache")
	if got := ConfigDir(); got != filepath.Join("/x/conf", appName) {
		t.Errorf("ConfigDir = %q", got)
	}
	if got := DataDir(); got != filepath.Join("/x/data", appName) {
		t.Errorf("DataDir = %q", got)
	}
	if got := CacheDir(); got != filepath.Join("/x/cache", appName) {
		t.Errorf("CacheDir = %q", got)
	}
	if got := FilePath(); got != filepath.Join("/x/conf", appName, "config.toml") {
		t.Errorf("FilePath = %q", got)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	if got := ConfigDir(); got != filepath.Join(home, ".config", appName) {
		t.Errorf("ConfigDir fallback = %q", got)
	}
	if got := DataDir(); got != filepath.Join(home, ".local", "share", appName) {
		t.Errorf("DataDir fallback = %q", got)
	}
}

func TestMCPUploadDirs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := Default()
	if err := parse([]byte("[mcp]\nupload_dirs = [\"~/Documents/receipts\", \"/srv/share/\", \"~\"]\n"), &cfg); err != nil {
		t.Fatal(err)
	}
	if err := cfg.validate(); err != nil {
		t.Fatal(err)
	}
	got := cfg.MCP.ExpandedUploadDirs()
	want := []string{filepath.Join(home, "Documents", "receipts"), "/srv/share", home}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("upload_dirs = %v, want %v", got, want)
	}
	cfg.MCP.UploadDirs = []string{"Downloads"}
	if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "mcp.upload_dirs") {
		t.Fatalf("a relative folder should be refused: %v", err)
	}
}

// The default 1Password item falls back to the one of the former name; an item set in
// config.toml (whatever its name) does not.
func TestOnePasswordLegacyItem(t *testing.T) {
	for _, tc := range []struct{ name, body, wantItem, wantLegacy string }{
		{"no config", "", "asagao", LegacyAppName},
		{"other settings", "[auth.onepassword]\nvault = \"Work\"\n", "asagao", LegacyAppName},
		{"item set", "[auth.onepassword]\nitem = \"my-asana\"\n", "my-asana", ""},
		{"item set to the new name", "[auth.onepassword]\nitem = \"asagao\"\n", "asagao", ""},
		{"item set to the old name", "[auth.onepassword]\nitem = \"asana-tui\"\n", "asana-tui", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", dir)
			if tc.body != "" {
				if err := os.MkdirAll(filepath.Join(dir, appName), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(FilePath(), []byte(tc.body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cfg, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			if op := cfg.Auth.OnePassword; op.Item != tc.wantItem || op.LegacyItem != tc.wantLegacy {
				t.Errorf("item %q legacy %q, want %q %q", op.Item, op.LegacyItem, tc.wantItem, tc.wantLegacy)
			}
		})
	}
}
