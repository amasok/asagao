package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A value of the wrong type is reported with the struct fields named config.X, as before
// the types moved to internal/settings.
func TestLoadWrongTypeNamesConfigFields(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{"[sync]\ncompleted_days = \"x\"\n", "config.SyncConfig.CompletedDays"},
		{"[ui]\ntheme = 1\n", "config.UIConfig.Theme"},
		{"[mcp]\nupload_dirs = [\"/tmp\", 3]\n", "config.MCPConfig.UploadDirs"},
	} {
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", dir)
		t.Setenv("ASANA_CLIENT_ID", "")
		if err := os.MkdirAll(filepath.Join(dir, appName), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(FilePath(), []byte(tc.body), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := Load()
		if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "settings.") {
			t.Errorf("%q: err = %v, want it to name %s", tc.body, err, tc.want)
		}
	}
}
