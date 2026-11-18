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

// TestWorkspace is the only workspace this script may write to (#31, #47).
const TestWorkspace = "1220646164450270"

// Env is the environment the asagao child processes run in. It is built from scratch so
// that the usual token (ASANA_TOKEN, keychain login, ~/.config/asagao) can never leak in.
type Env struct {
	Home   string // the user's home; the test tree is Home/asagao-test
	Bin    string // the asagao built from this branch
	Vars   []string
	Config string
	Data   string
	Cache  string
}

// testRoot is where the test asagao keeps its config, data and cache.
func testRoot(home string) string { return filepath.Join(home, "asagao-test") }

// NewEnv builds the child environment for the test tree under home.
func NewEnv(home, bin, path string) Env {
	root := testRoot(home)
	e := Env{
		Home:   home,
		Bin:    bin,
		Config: filepath.Join(root, "config"),
		Data:   filepath.Join(root, "data"),
		Cache:  filepath.Join(root, "cache"),
	}
	e.Vars = []string{
		"HOME=" + home,
		"PATH=" + path,
		"LANG=C.UTF-8",
		"XDG_CONFIG_HOME=" + e.Config,
		"XDG_DATA_HOME=" + e.Data,
		"XDG_CACHE_HOME=" + e.Cache,
	}
	return e
}

var tokenStoreFile = regexp.MustCompile(`(?m)^\s*token_store\s*=\s*"file"\s*$`)

// CheckEnv refuses to run unless every path points into ~/asagao-test, the binary is not
// the user's usual one, and the test config stores the token in a file (the keychain
// entry is not split by XDG, so a keychain login would be the usual one).
func CheckEnv(e Env) error {
	root := testRoot(e.Home)
	for _, v := range e.Vars {
		k, val, _ := strings.Cut(v, "=")
		switch k {
		case "ASANA_TOKEN", "ASANA_CLIENT_ID", "ASANA_CLIENT_SECRET":
			return fmt.Errorf("%s must not be passed to the test asagao", k)
		case "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME":
			if !inside(root, val) {
				return fmt.Errorf("%s=%s is not under %s", k, val, root)
			}
		}
	}
	for _, d := range []string{e.Config, e.Data, e.Cache} {
		if !inside(root, d) {
			return fmt.Errorf("%s is not under %s", d, root)
		}
	}
	bin, err := filepath.Abs(e.Bin)
	if err != nil {
		return err
	}
	if bin == filepath.Join(root, "bin", "asagao") {
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
	if me.Data.GID == "" {
		return "", errors.New("users/me: no gid")
	}
	return me.Data.GID, nil
}
