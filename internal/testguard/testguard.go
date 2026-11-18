// Package testguard keeps the tests away from the user's own files. Every package with
// tests runs them through it (archtest checks that each one has a TestMain calling Main or
// Run):
//
//	func TestMain(m *testing.M) { testguard.Main(m) }
//
// Before any test, HOME and the XDG base directories (config, data, cache, state) are a
// private temporary directory, so a test that does not set its own never reaches the
// user's ~/.local/share/asagao/store.db, ~/.config/asagao or ~/.cache/asagao, even when the
// code that picks those folders is broken (Issue #53: a test wrote "new" over the user's
// store.db). The go command's own folders (its build and module caches, its settings) are
// kept where they were, so tests that run `go` still work offline.
//
// The guard of internal/xdg stops a test that still gets a folder of the real home, and
// internal/auth/tokenstore uses go-keyring's in-memory mock in a test binary, so the
// keychain is never read or written either.
//
// Only tests import it (archtest checks this too). It uses the standard library only.
package testguard

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Vars are the environment variables pointed at the private directory: HOME, and the XDG
// base directories that the tool, and the libraries it uses, read.
var Vars = []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"}

// EnvDir names the private directory in the environment, for the processes a test starts
// from its own binary (helper processes, `os.Args[0] -test.run=...`): they inherit HOME and
// the XDG folders the test chose, which Setup keeps (see inherited).
const EnvDir = "ASAGAO_TESTGUARD_DIR"

// dirPrefix starts the name of every private directory, in os.TempDir.
const dirPrefix = "asagao-test-"

// dir is the private directory of this test binary, set by Setup. owned is whether this
// process made it (and removes it), rather than inheriting it from the test that started it.
var (
	dir   string
	owned bool
)

// Dir is the private directory that HOME and the XDG folders are under ("" before Setup).
func Dir() string { return dir }

// Main runs the tests of the package with Setup done, removes the private directory, and
// exits with their result.
func Main(m *testing.M) { os.Exit(Run(m)) }

// Run is Main for a TestMain that has more to do before or after the tests: it does Setup,
// runs m, removes the private directory, and returns the exit code (it does not exit).
func Run(m *testing.M) int {
	if err := Setup(); err != nil {
		fmt.Fprintln(os.Stderr, "testguard:", err)
		return 1
	}
	code := m.Run()
	if owned {
		os.RemoveAll(dir)
	}
	return code
}

// Setup makes the private directory and points HOME and the XDG folders at it. The go
// command's folders are first fixed to where they are now (unless already set), since they
// are found from HOME too. It is done once; later calls do nothing.
//
// A process started by a guarded test from the same binary keeps the environment it was
// given (the test may have pointed the XDG folders at its own t.TempDir), as long as HOME
// is in the private directory named by EnvDir (see inherited).
func Setup() error {
	if dir != "" {
		return nil
	}
	if d, ok := inherited(); ok {
		dir = d
		return nil
	}
	if err := keepGoDirs(); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", dirPrefix)
	if err != nil {
		return err
	}
	for _, k := range Vars {
		d := filepath.Join(tmp, k)
		if err := os.MkdirAll(d, 0o700); err != nil {
			os.RemoveAll(tmp)
			return err
		}
		if err := os.Setenv(k, d); err != nil {
			os.RemoveAll(tmp)
			return err
		}
	}
	if err := os.Setenv(EnvDir, tmp); err != nil {
		os.RemoveAll(tmp)
		return err
	}
	dir, owned = tmp, true
	return nil
}

// inherited returns the private directory of the guarded test that started this process,
// when EnvDir names one: an existing folder named like ours, directly in os.TempDir, and
// HOME in os.TempDir too (in that folder, or a test's t.TempDir). Anything else (unset, set
// by hand in a shell, or HOME elsewhere) is not trusted, and Setup makes a new private
// directory.
func inherited() (string, bool) {
	d := os.Getenv(EnvDir)
	if d == "" || !filepath.IsAbs(d) {
		return "", false
	}
	d = filepath.Clean(d)
	if filepath.Dir(d) != filepath.Clean(os.TempDir()) || !strings.HasPrefix(filepath.Base(d), dirPrefix) {
		return "", false
	}
	if fi, err := os.Lstat(d); err != nil || !fi.IsDir() {
		return "", false
	}
	home := os.Getenv("HOME")
	if home == "" || !filepath.IsAbs(home) {
		return "", false
	}
	rel, err := filepath.Rel(filepath.Dir(d), filepath.Clean(home))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return d, true
}

// keepGoDirs sets GOPATH, GOMODCACHE, GOCACHE and GOENV to the folders the go command uses
// now (as `go env` prints them), so that `go build` or `go list` run by a test (archtest,
// cmd/asagao) finds its caches and settings after HOME has moved. These are the go
// command's folders, not the tool's: the tests only use them through `go`. Without a go
// command nothing is set (no test can run one then).
func keepGoDirs() error {
	gocmd, err := exec.LookPath("go")
	if err != nil {
		return nil
	}
	out, err := exec.Command(gocmd, "env", "-json", "GOPATH", "GOMODCACHE", "GOCACHE", "GOENV").Output()
	if err != nil {
		return fmt.Errorf("go env: %w", err)
	}
	var env map[string]string
	if err := json.Unmarshal(out, &env); err != nil {
		return fmt.Errorf("go env: %w", err)
	}
	for k, v := range env {
		if v == "" || os.Getenv(k) != "" {
			continue
		}
		if err := os.Setenv(k, v); err != nil {
			return err
		}
	}
	return nil
}
