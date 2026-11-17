package cli

import (
	"runtime/debug"
	"strings"
)

// Set at link time by goreleaser (.goreleaser.yaml):
//
//	-X github.com/amasok/asagao/internal/cli.version=1.2.3
//	-X github.com/amasok/asagao/internal/cli.commit=<full hash>
//	-X github.com/amasok/asagao/internal/cli.date=<commit date, RFC 3339>
//
// Empty when built otherwise (go install, go build): currentBuild falls back to the build info.
var version, commit, date string

// buildVersion is which asagao this is.
type buildVersion struct {
	Version string // "1.2.3", or "dev" when not known
	Commit  string // "" when not known
	Date    string // the commit's date (RFC 3339), "" when not known
}

// String is what `asagao --version` prints after "asagao version":
// "1.2.3 (commit 0123456789ab, 2026-11-17T00:00:00Z)".
func (b buildVersion) String() string {
	var extra []string
	if b.Commit != "" {
		extra = append(extra, "commit "+shortCommit(b.Commit))
	}
	if b.Date != "" {
		extra = append(extra, b.Date)
	}
	if len(extra) == 0 {
		return b.Version
	}
	return b.Version + " (" + strings.Join(extra, ", ") + ")"
}

func currentBuild() buildVersion {
	info, _ := debug.ReadBuildInfo()
	return resolveBuild(version, commit, date, info)
}

// resolveBuild takes the version from what the linker embedded, then from the module version
// that `go install ...@vX` records, and is "dev" when neither is there. The commit and the
// date come from the linker, or from the VCS stamp of a build in a checkout.
func resolveBuild(ver, rev, when string, info *debug.BuildInfo) buildVersion {
	if info != nil {
		if ver == "" {
			ver = moduleVersion(info.Main.Version)
		}
		for _, s := range info.Settings {
			switch {
			case s.Key == "vcs.revision" && rev == "":
				rev = s.Value
			case s.Key == "vcs.time" && when == "":
				when = s.Value
			}
		}
	}
	if ver == "" {
		ver = "dev"
	}
	return buildVersion{Version: ver, Commit: rev, Date: when}
}

// moduleVersion is the module version without the leading "v", so that it reads like the
// version goreleaser embeds ("1.2.3"). "(devel)" (no version known) is "".
func moduleVersion(v string) string {
	if v == "" || v == "(devel)" {
		return ""
	}
	return strings.TrimPrefix(v, "v")
}

func shortCommit(rev string) string {
	if len(rev) > 12 {
		return rev[:12]
	}
	return rev
}
