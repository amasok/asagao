package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// The only workspace this script may write to, and the test account and team in it
// (#31, #47).
const (
	TestWorkspace = "1220646164450270"
	TestUser      = "1220646164280620"
	TestTeam      = "1220646164450280"
)

// Env is the environment the asagao child processes run in. It is built from scratch so
// that the usual token (ASANA_TOKEN, keychain login, ~/.config/asagao) can never leak in.
// The config (with the test token) is ~/asagao-test/config; data, cache, HOME and the
// working directory are new for every run (~/asagao-test/realcheck-runs/<time>), so the
// run never touches what the user queued by hand in ~/asagao-test/data, and a schema
// migration of this branch cannot reach the store the usual test binary opens.
type Env struct {
	Root   string // ~/asagao-test
	Run    string // Root/realcheck-runs/<time>
	Bin    string // the asagao built from this branch
	Vars   []string
	Config string
	Data   string
	Cache  string
	Home   string
	Work   string // the working directory of the children (no .asana.json above it)
}

// testRoot is where the test asagao keeps its config.
func testRoot(home string) string { return filepath.Join(home, "asagao-test") }

// NewEnv builds the child environment for the test tree under home and the run id.
func NewEnv(home, bin, path, run string) Env {
	root := testRoot(home)
	rd := filepath.Join(root, "realcheck-runs", run)
	e := Env{
		Root:   root,
		Run:    rd,
		Bin:    bin,
		Config: filepath.Join(root, "config"),
		Data:   filepath.Join(rd, "data"),
		Cache:  filepath.Join(rd, "cache"),
		Home:   filepath.Join(rd, "home"),
		Work:   filepath.Join(rd, "work"),
	}
	e.Vars = []string{
		"HOME=" + e.Home,
		"PATH=" + path,
		"LANG=C.UTF-8",
		"XDG_CONFIG_HOME=" + e.Config,
		"XDG_DATA_HOME=" + e.Data,
		"XDG_CACHE_HOME=" + e.Cache,
	}
	return e
}

// MakeRunDirs creates the per-run directories (after CheckEnv).
func (e Env) MakeRunDirs() error {
	for _, d := range []string{e.Data, e.Cache, e.Home, e.Work} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	return nil
}

var tokenStoreFile = regexp.MustCompile(`(?m)^\s*token_store\s*=\s*"file"\s*$`)

// CheckEnv refuses to run unless every path points into ~/asagao-test (data and cache into
// this run's own directory), the binary is not the user's usual one, the test tree is made
// of real files (no symlink could lead the token or the store elsewhere), and the test
// config stores the token in a file (the keychain entry is not split by XDG, so a keychain
// login would be the usual one).
func CheckEnv(e Env) error {
	runs := filepath.Join(e.Root, "realcheck-runs")
	if !inside(runs, e.Run) || e.Run == runs {
		return fmt.Errorf("run directory %s is not under %s", e.Run, runs)
	}
	// The config is the test one, or a copy of it in this run's directory (-syncbench
	// changes a setting there; the test config itself is never written).
	if e.Config != filepath.Join(e.Root, "config") && !inside(e.Run, e.Config) {
		return fmt.Errorf("config %s is neither %s nor in %s", e.Config, filepath.Join(e.Root, "config"), e.Run)
	}
	want := map[string]string{
		"HOME":            e.Home,
		"XDG_CONFIG_HOME": e.Config,
		"XDG_DATA_HOME":   e.Data,
		"XDG_CACHE_HOME":  e.Cache,
	}
	seen := map[string]bool{}
	for _, v := range e.Vars {
		k, val, _ := strings.Cut(v, "=")
		switch k {
		case "ASANA_TOKEN", "ASANA_CLIENT_ID", "ASANA_CLIENT_SECRET":
			return fmt.Errorf("%s must not be passed to the test asagao", k)
		}
		if w, ok := want[k]; ok {
			if val != w || !inside(e.Root, val) {
				return fmt.Errorf("%s=%s, want %s under %s", k, val, w, e.Root)
			}
			seen[k] = true
		}
	}
	for k := range want {
		if !seen[k] {
			return fmt.Errorf("%s is not set", k)
		}
	}
	for _, d := range []string{e.Data, e.Cache, e.Home, e.Work} {
		if !inside(e.Run, d) {
			return fmt.Errorf("%s is not under %s", d, e.Run)
		}
	}
	// No symlinks on the way to the token, the config or the run directory.
	for _, p := range []struct {
		path string
		dir  bool
	}{
		{e.Root, true},
		{e.Config, true},
		{filepath.Join(e.Config, "asagao"), true},
		{filepath.Join(e.Config, "asagao", "token.json"), false},
		{filepath.Join(e.Config, "asagao", "config.toml"), false},
	} {
		if err := plain(p.path, p.dir); err != nil {
			return err
		}
	}
	for _, p := range []string{runs, e.Run} {
		if fi, err := os.Lstat(p); err == nil {
			if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("%s is not a plain directory", p)
			}
		}
	}
	bin, err := filepath.Abs(e.Bin)
	if err != nil {
		return err
	}
	if bin == filepath.Join(e.Root, "bin", "asagao") {
		return errors.New("use a binary built from this branch (e.g. ~/asagao-test/bin/asagao-realcheck), not ~/asagao-test/bin/asagao")
	}
	if _, err := os.Stat(bin); err != nil {
		return fmt.Errorf("asagao binary: %w", err)
	}
	cfg, err := os.ReadFile(filepath.Join(e.Config, "asagao", "config.toml"))
	if err != nil {
		return fmt.Errorf("test config: %w", err)
	}
	if !tokenStoreFile.Match(cfg) {
		return errors.New(`test config.toml must set token_store = "file"`)
	}
	return nil
}

// plain checks that p exists and is a directory (dir) or a regular file, not a symlink.
func plain(p string, dir bool) error {
	fi, err := os.Lstat(p)
	if err != nil {
		return err
	}
	switch {
	case fi.Mode()&os.ModeSymlink != 0:
		return fmt.Errorf("%s is a symlink", p)
	case dir && !fi.IsDir():
		return fmt.Errorf("%s is not a directory", p)
	case !dir && !fi.Mode().IsRegular():
		return fmt.Errorf("%s is not a regular file", p)
	}
	return nil
}

// inside reports whether p is root or below it (after cleaning; no symlink tricks with "..").
func inside(root, p string) bool {
	if p == "" || !filepath.IsAbs(p) {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(p))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// CheckMe checks the body of GET /users/me: the account must belong to exactly the test
// workspace. sync pulls every workspace the account is in, so one more would be read and
// cached too, and a write could land in it.
func CheckMe(body []byte) (userGID string, err error) {
	var me struct {
		Data struct {
			GID        string `json:"gid"`
			Workspaces []struct {
				GID string `json:"gid"`
			} `json:"workspaces"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &me); err != nil {
		return "", fmt.Errorf("users/me: %w", err)
	}
	ws := me.Data.Workspaces
	if len(ws) != 1 || ws[0].GID != TestWorkspace {
		gids := make([]string, len(ws))
		for i, w := range ws {
			gids[i] = w.GID
		}
		return "", fmt.Errorf("the account must be in exactly workspace %s, got %v", TestWorkspace, gids)
	}
	if me.Data.GID != TestUser {
		return "", fmt.Errorf("users/me is %q, not the test account %s", me.Data.GID, TestUser)
	}
	return me.Data.GID, nil
}
