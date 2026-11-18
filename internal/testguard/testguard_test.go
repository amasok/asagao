package testguard

import (
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/xdg"
)

func TestMain(m *testing.M) { Main(m) }

func inside(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Before any test, HOME and the XDG folders are the private directory, and the tool's
// folders are in it.
func TestSetupMovesHomeAndXDG(t *testing.T) {
	d := Dir()
	if d == "" || !owned {
		t.Fatalf("Setup did not make a private directory: %q, owned %v", d, owned)
	}
	if !strings.HasPrefix(filepath.Base(d), dirPrefix) || filepath.Dir(d) != filepath.Clean(os.TempDir()) {
		t.Errorf("private directory %s is not %s* in %s", d, dirPrefix, os.TempDir())
	}
	for _, k := range Vars {
		if v := os.Getenv(k); !inside(v, d) {
			t.Errorf("%s = %q, not in %s", k, v, d)
		}
	}
	if v := os.Getenv(EnvDir); v != d {
		t.Errorf("%s = %q, want %q", EnvDir, v, d)
	}
	for _, p := range xdg.OwnDirs() {
		if !inside(p, d) {
			t.Errorf("xdg gives %s, not in %s", p, d)
		}
	}
	if err := Setup(); err != nil || Dir() != d {
		t.Errorf("a second Setup changed things: %v, %q", err, Dir())
	}
}

// The go command's folders stay where they were (not in the private directory), so tests
// that run `go` still find the caches and settings.
func TestGoDirsAreKept(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go command")
	}
	for _, k := range []string{"GOPATH", "GOMODCACHE", "GOCACHE", "GOENV"} {
		v := os.Getenv(k)
		if v == "" {
			t.Errorf("%s is not set", k)
			continue
		}
		if k != "GOENV" && inside(v, Dir()) {
			t.Errorf("%s = %q is in the private directory", k, v)
		}
		out, err := exec.Command("go", "env", k).Output()
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(string(out)); got != v {
			t.Errorf("go env %s = %q, want %q", k, got, v)
		}
	}
}

// xdg repeats the name of EnvDir: they must be the same, or a helper process would stop.
func TestEnvDirIsWhatXDGReads(t *testing.T) {
	if EnvDir != xdg.TestguardEnv {
		t.Errorf("testguard.EnvDir %q, xdg %q", EnvDir, xdg.TestguardEnv)
	}
}

// A process started by a guarded test keeps its environment only when it is the one
// testguard set up: anything else gets a private directory of its own.
func TestInherited(t *testing.T) {
	tmp := filepath.Clean(os.TempDir())
	d, err := os.MkdirTemp(tmp, dirPrefix)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	other, err := os.MkdirTemp(tmp, "other-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(other) })
	file := filepath.Join(tmp, dirPrefix+"file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(file) })
	home := "/nonexistent-home"
	if u, err := user.Current(); err == nil && u.HomeDir != "" {
		home = u.HomeDir
	}
	for _, tc := range []struct {
		name, dir, home string
		ok              bool
	}{
		{"home in it", d, filepath.Join(d, "HOME"), true},
		{"home in a t.TempDir", d, filepath.Join(tmp, "TestX123", "001"), true},
		{"unset", "", filepath.Join(d, "HOME"), false},
		{"relative", "rel/" + dirPrefix + "x", filepath.Join(d, "HOME"), false},
		{"not ours", other, filepath.Join(other, "HOME"), false},
		{"missing", filepath.Join(tmp, dirPrefix+"missing"), filepath.Join(tmp, "x"), false},
		{"a file", file, filepath.Join(tmp, "x"), false},
		{"deeper", filepath.Join(d, dirPrefix+"x"), filepath.Join(d, "HOME"), false},
		{"real home", d, home, false},
		{"no home", d, "", false},
		{"relative home", d, "HOME", false},
		{"home next to TempDir", d, tmp + "-x", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(EnvDir, tc.dir)
			t.Setenv("HOME", tc.home)
			if _, ok := inherited(); ok != tc.ok {
				t.Errorf("inherited = %v, want %v", ok, tc.ok)
			}
		})
	}
}

// The helper of the tests below: it prints where the tool's data folder is, as the tests
// of another package would see it. It only computes the path.
func TestHelperPrintDataDir(t *testing.T) {
	if os.Getenv("ASAGAO_TESTGUARD_HELPER") != "1" {
		t.Skip("helper process")
	}
	os.Stdout.WriteString("DATADIR=" + xdg.DataDir() + "\n")
}

func helper(t *testing.T, env ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperPrintDataDir$", "-test.count=1")
	cmd.Env = append(os.Environ(), append([]string{"ASAGAO_TESTGUARD_HELPER=1"}, env...)...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// A test binary started with the user's real HOME and XDG folders (as `go test` starts
// every one) does not see the user's folders: testguard moves them first. Here the real
// ones are put back on purpose, as a broken test would, and the tool's data folder still
// comes out in a private directory (Issue #53).
func TestRealFoldersAreMovedAway(t *testing.T) {
	u, err := user.Current()
	if err != nil || u.HomeDir == "" {
		t.Skip("no home folder in the user database")
	}
	real := filepath.Join(u.HomeDir, ".local", "share")
	out, err := helper(t, "HOME="+u.HomeDir, "XDG_DATA_HOME="+real, EnvDir+"=")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	_, after, ok := strings.Cut(out, "DATADIR=")
	got, _, _ := strings.Cut(after, "\n")
	if !ok || inside(got, u.HomeDir) || !strings.Contains(got, string(filepath.Separator)+dirPrefix) {
		t.Errorf("the helper's data folder is %q, not a private directory:\n%s", got, out)
	}
}

// A helper process that inherits a guarded environment, but whose test pointed an XDG
// folder at the user's real one, is stopped by xdg before it reaches it (Issue #53).
func TestRealFolderStopsAHelper(t *testing.T) {
	u, err := user.Current()
	if err != nil || u.HomeDir == "" {
		t.Skip("no home folder in the user database")
	}
	out, err := helper(t, "XDG_DATA_HOME="+filepath.Join(u.HomeDir, ".local", "share"))
	if err == nil || strings.Contains(out, "DATADIR=") || !strings.Contains(out, "a test reached the user's own folder") {
		t.Errorf("the helper was not stopped: %v\n%s", err, out)
	}
}
