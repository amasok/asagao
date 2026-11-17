package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The -X flags in .goreleaser.yaml name variables that exist: the linker ignores a -X for a
// symbol that is not there, so a renamed variable would quietly give released binaries
// "dev". Build with the flags as goreleaser would and run `asagao --version`.
func TestGoreleaserLdflagsSetTheVersion(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	cfg, err := os.ReadFile("../../.goreleaser.yaml")
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{
		".Version":    "9.8.7",
		".FullCommit": "fedcba9876543210fedcba9876543210fedcba98",
		".CommitDate": "2026-11-17T01:02:03Z",
	}
	re := regexp.MustCompile(`-X (\S+)=\{\{ *(\.\w+) *\}\}`)
	var ldflags []string
	seen := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(string(cfg), -1) {
		v, ok := values[m[2]]
		if !ok {
			t.Fatalf("-X %s uses %s: give it a value in this test", m[1], m[2])
		}
		seen[m[2]] = true
		ldflags = append(ldflags, "-X "+m[1]+"="+v)
	}
	for k := range values {
		if !seen[k] {
			t.Errorf(".goreleaser.yaml does not embed {{ %s }}", k)
		}
	}

	bin := filepath.Join(t.TempDir(), "asagao")
	build := exec.Command("go", "build", "-ldflags", strings.Join(ldflags, " "), "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	// Never the user's store.db or config.toml.
	home := t.TempDir()
	run := exec.Command(bin, "--version")
	run.Env = append(os.Environ(), "HOME="+home,
		"XDG_DATA_HOME="+home, "XDG_CONFIG_HOME="+home, "XDG_CACHE_HOME="+home, "XDG_STATE_HOME="+home)
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("asagao --version: %v\n%s", err, out)
	}
	if got, want := string(out), "asagao version 9.8.7 (commit fedcba987654, 2026-11-17T01:02:03Z)\n"; got != want {
		t.Errorf("asagao --version = %q, want %q", got, want)
	}
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		t.Errorf("--version wrote to the data folders: %v", entries)
	}
}
