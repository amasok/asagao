package cli

import (
	"bytes"
	"runtime/debug"
	"testing"
)

func TestResolveBuild(t *testing.T) {
	stamped := []debug.BuildSetting{
		{Key: "vcs", Value: "git"},
		{Key: "vcs.revision", Value: "0123456789abcdef0123456789abcdef01234567"},
		{Key: "vcs.time", Value: "2026-11-01T00:00:00Z"},
	}
	for _, tc := range []struct {
		name            string
		ver, rev, built string
		info            *debug.BuildInfo
		want            buildVersion
		wantString      string
	}{
		{
			name: "goreleaser: what the linker embedded wins over the build info",
			ver:  "1.2.3", rev: "fedcba9876543210fedcba9876543210fedcba98", built: "2026-11-17T01:02:03Z",
			info:       &debug.BuildInfo{Main: debug.Module{Version: "v0.9.0"}, Settings: stamped},
			want:       buildVersion{"1.2.3", "fedcba9876543210fedcba9876543210fedcba98", "2026-11-17T01:02:03Z"},
			wantString: "1.2.3 (commit fedcba987654, 2026-11-17T01:02:03Z)",
		},
		{
			name:       "go install ...@v1.4.0: the module version, without the leading v",
			info:       &debug.BuildInfo{Main: debug.Module{Version: "v1.4.0"}},
			want:       buildVersion{Version: "1.4.0"},
			wantString: "1.4.0",
		},
		{
			name:       "go build in a checkout: the stamped version, commit and time",
			info:       &debug.BuildInfo{Main: debug.Module{Version: "v0.0.0-20261101000000-0123456789ab+dirty"}, Settings: stamped},
			want:       buildVersion{"0.0.0-20261101000000-0123456789ab+dirty", "0123456789abcdef0123456789abcdef01234567", "2026-11-01T00:00:00Z"},
			wantString: "0.0.0-20261101000000-0123456789ab+dirty (commit 0123456789ab, 2026-11-01T00:00:00Z)",
		},
		{
			name:       "(devel) is no version",
			info:       &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}},
			want:       buildVersion{Version: "dev"},
			wantString: "dev",
		},
		{
			name:       "no build info at all",
			want:       buildVersion{Version: "dev"},
			wantString: "dev",
		},
		{
			name:       "only the version embedded: commit and date from the VCS stamp",
			ver:        "2.0.0",
			info:       &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}, Settings: stamped},
			want:       buildVersion{"2.0.0", "0123456789abcdef0123456789abcdef01234567", "2026-11-01T00:00:00Z"},
			wantString: "2.0.0 (commit 0123456789ab, 2026-11-01T00:00:00Z)",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveBuild(tc.ver, tc.rev, tc.built, tc.info)
			if got != tc.want {
				t.Errorf("resolveBuild = %+v, want %+v", got, tc.want)
			}
			if s := got.String(); s != tc.wantString {
				t.Errorf("String() = %q, want %q", s, tc.wantString)
			}
		})
	}
}

// `asagao --version` prints the embedded version, and does nothing else: the folders are
// not moved and config.toml is not read (cobra answers --version before the hooks).
func TestVersionFlag(t *testing.T) {
	defer func(v, c, d string) { version, commit, date = v, c, d }(version, commit, date)
	version, commit, date = "1.2.3", "fedcba9876543210fedcba9876543210fedcba98", "2026-11-17T01:02:03Z"

	called := false
	w := testWiring()
	w.MigrateLegacyDirs = func() error { called = true; return nil }
	w.LoadSettings = nil // would panic if --version read config.toml

	root := NewRoot(w)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"--version"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "asagao version 1.2.3 (commit fedcba987654, 2026-11-17T01:02:03Z)\n"; got != want {
		t.Errorf("--version printed %q, want %q", got, want)
	}
	if called {
		t.Error("--version moved the legacy folders")
	}
}
