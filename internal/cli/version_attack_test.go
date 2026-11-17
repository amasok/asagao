package cli

import (
	"runtime/debug"
	"testing"
)

func TestResolveBuildBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name string
		mod  string
		want string
	}{
		{"empty module version", "", "dev"},
		{"only a v", "v", "dev"},
		{"double v keeps the second", "vv1.0.0", "v1.0.0"},
		{"pre-release", "v1.0.0-rc.1", "1.0.0-rc.1"},
		{"incompatible", "v2.0.0+incompatible", "2.0.0+incompatible"},
		{"devel with dirty", "(devel)+dirty", "(devel)+dirty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveBuild("", "", "", &debug.BuildInfo{Main: debug.Module{Version: tc.mod}})
			if got.Version != tc.want {
				t.Errorf("Version = %q, want %q", got.Version, tc.want)
			}
		})
	}
}

func TestResolveBuildPartialStamp(t *testing.T) {
	// A linker commit with no date takes the date from the VCS stamp, and the other way round;
	// the settings must never overwrite what the linker gave.
	info := &debug.BuildInfo{Settings: []debug.BuildSetting{
		{Key: "vcs.revision", Value: "vcsrev"}, {Key: "vcs.time", Value: "vcstime"},
	}}
	got := resolveBuild("1.0.0", "linkrev", "", info)
	if got.Commit != "linkrev" || got.Date != "vcstime" {
		t.Errorf("got %+v", got)
	}
	got = resolveBuild("1.0.0", "", "linktime", info)
	if got.Commit != "vcsrev" || got.Date != "linktime" {
		t.Errorf("got %+v", got)
	}
}

func TestShortCommitBoundaries(t *testing.T) {
	for in, want := range map[string]string{
		"": "", "abc": "abc", "123456789012": "123456789012", "1234567890123": "123456789012",
	} {
		if got := shortCommit(in); got != want {
			t.Errorf("shortCommit(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildVersionStringOmitsEmptyParts(t *testing.T) {
	if got := (buildVersion{Version: "1", Date: "d"}).String(); got != "1 (d)" {
		t.Errorf("date only: %q", got)
	}
	if got := (buildVersion{Version: "1", Commit: "abc"}).String(); got != "1 (commit abc)" {
		t.Errorf("commit only: %q", got)
	}
}
