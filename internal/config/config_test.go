package config

import (
	"os"
	"path/filepath"
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
