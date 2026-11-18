package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testTree makes a fake ~/asagao-test with a config, a token and a branch binary.
func testTree(t *testing.T, cfg string) (home, bin string) {
	t.Helper()
	home = t.TempDir()
	root := filepath.Join(home, "asagao-test")
	for _, d := range []string{"config/asagao", "data", "cache", "bin", "realcheck-runs"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p, s string, mode os.FileMode) {
		if err := os.WriteFile(filepath.Join(root, p), []byte(s), mode); err != nil {
			t.Fatal(err)
		}
	}
	write("config/asagao/config.toml", cfg, 0o600)
	write("config/asagao/token.json", "{}", 0o600)
	write("bin/asagao", "#!/bin/sh\n", 0o755)
	write("bin/asagao-realcheck", "#!/bin/sh\n", 0o755)
	return home, filepath.Join(root, "bin", "asagao-realcheck")
}

const fileStore = "[auth]\ntoken_store = \"file\"\n"

func TestCheckEnvAcceptsTestTree(t *testing.T) {
	home, bin := testTree(t, fileStore)
	e := NewEnv(home, bin, "/usr/bin", "20260101-000000")
	if err := CheckEnv(e); err != nil {
		t.Fatal(err)
	}
	// data and cache are the run's own, not the hand-testing store in ~/asagao-test/data.
	for _, v := range e.Vars {
		if strings.HasPrefix(v, "XDG_DATA_HOME=") && strings.HasSuffix(v, "asagao-test/data") {
			t.Fatalf("shared data dir: %s", v)
		}
	}
	if err := e.MakeRunDirs(); err != nil {
		t.Fatal(err)
	}
	if err := CheckEnv(e); err != nil {
		t.Fatalf("after MakeRunDirs: %v", err)
	}
}

func TestCheckEnvRefuses(t *testing.T) {
	home, bin := testTree(t, fileStore)
	root := filepath.Join(home, "asagao-test")
	set := func(e *Env, k, v string) {
		for i, kv := range e.Vars {
			if strings.HasPrefix(kv, k+"=") {
				e.Vars[i] = k + "=" + v
			}
		}
	}
	cases := map[string]func(e *Env){
		"usual config":    func(e *Env) { set(e, "XDG_CONFIG_HOME", filepath.Join(home, ".config")) },
		"usual data":      func(e *Env) { set(e, "XDG_DATA_HOME", filepath.Join(home, ".local/share")) },
		"shared data":     func(e *Env) { set(e, "XDG_DATA_HOME", filepath.Join(root, "data")) },
		"dotdot":          func(e *Env) { set(e, "XDG_CACHE_HOME", e.Cache+"/../../../../.cache") },
		"relative":        func(e *Env) { set(e, "XDG_CONFIG_HOME", "asagao-test/config") },
		"real home":       func(e *Env) { set(e, "HOME", home) },
		"no HOME":         func(e *Env) { e.Vars = e.Vars[1:] },
		"token":           func(e *Env) { e.Vars = append(e.Vars, "ASANA_TOKEN=x") },
		"client":          func(e *Env) { e.Vars = append(e.Vars, "ASANA_CLIENT_SECRET=x") },
		"data field":      func(e *Env) { e.Data = filepath.Join(home, ".local/share") },
		"work outside":    func(e *Env) { e.Work = home },
		"run is the root": func(e *Env) { e.Run = filepath.Join(root, "realcheck-runs") },
		"run outside":     func(e *Env) { e.Run = filepath.Join(root, "data") },
		"config moved":    func(e *Env) { e.Config = filepath.Join(root, "data") },
		"usual binary":    func(e *Env) { e.Bin = filepath.Join(root, "bin/asagao") },
		"no binary":       func(e *Env) { e.Bin = filepath.Join(home, "nope") },
	}
	for name, mod := range cases {
		t.Run(name, func(t *testing.T) {
			e := NewEnv(home, bin, "/usr/bin", "20260101-000000")
			mod(&e)
			if err := CheckEnv(e); err == nil {
				t.Fatal("CheckEnv accepted it")
			}
		})
	}
}

func TestCheckEnvRefusesSymlinks(t *testing.T) {
	elsewhere := t.TempDir()
	if err := os.WriteFile(filepath.Join(elsewhere, "token.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(root string) error{
		"token": func(root string) error {
			p := filepath.Join(root, "config/asagao/token.json")
			os.Remove(p)
			return os.Symlink(filepath.Join(elsewhere, "token.json"), p)
		},
		"config dir": func(root string) error {
			p := filepath.Join(root, "config/asagao")
			if err := os.Rename(p, filepath.Join(elsewhere, "asagao")); err != nil {
				return err
			}
			return os.Symlink(filepath.Join(elsewhere, "asagao"), p)
		},
		"runs dir": func(root string) error {
			p := filepath.Join(root, "realcheck-runs")
			os.Remove(p)
			return os.Symlink(elsewhere, p)
		},
	}
	for name, mod := range cases {
		t.Run(name, func(t *testing.T) {
			home, bin := testTree(t, fileStore)
			if err := mod(filepath.Join(home, "asagao-test")); err != nil {
				t.Fatal(err)
			}
			if err := CheckEnv(NewEnv(home, bin, "/usr/bin", "20260101-000000")); err == nil {
				t.Fatal("CheckEnv accepted a symlink")
			}
		})
	}
}

func TestCheckEnvRefusesKeychainConfig(t *testing.T) {
	for _, cfg := range []string{"", "[auth]\ntoken_store = \"keychain\"\n", "# token_store = \"file\"\n"} {
		home, bin := testTree(t, cfg)
		if err := CheckEnv(NewEnv(home, bin, "/usr/bin", "20260101-000000")); err == nil {
			t.Errorf("config %q accepted", cfg)
		}
	}
}

func TestCheckMe(t *testing.T) {
	ok := `{"data":{"gid":"` + TestUser + `","workspaces":[{"gid":"` + TestWorkspace + `"}]}}`
	if gid, err := CheckMe([]byte(ok)); err != nil || gid != TestUser {
		t.Fatalf("got %q, %v", gid, err)
	}
	bad := []string{
		`{"data":{"gid":"` + TestUser + `","workspaces":[]}}`,
		`{"data":{"gid":"` + TestUser + `","workspaces":[{"gid":"1"}]}}`,
		`{"data":{"gid":"` + TestUser + `","workspaces":[{"gid":"` + TestWorkspace + `"},{"gid":"1"}]}}`,
		`{"data":{"gid":"42","workspaces":[{"gid":"` + TestWorkspace + `"}]}}`, // another account
		`{"data":{"gid":"","workspaces":[{"gid":"` + TestWorkspace + `"}]}}`,
		`{"errors":[{"message":"Not Authorized"}]}`,
		`not json`,
	}
	for _, b := range bad {
		if _, err := CheckMe([]byte(b)); err == nil {
			t.Errorf("accepted %s", b)
		}
	}
}

// -syncbench may use a copy of the test config inside the run directory, nowhere else.
func TestCheckEnvConfigCopyInRun(t *testing.T) {
	home, bin := testTree(t, fileStore)
	e := NewEnv(home, bin, "/usr/bin", "20260101-000000")
	cfg := filepath.Join(e.Run, "config")
	if err := os.MkdirAll(filepath.Join(cfg, "asagao"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"token.json", "config.toml"} {
		if err := os.WriteFile(filepath.Join(cfg, "asagao", f), []byte(fileStore), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	use := func(e *Env, dir string) {
		e.Config = dir
		for i, kv := range e.Vars {
			if strings.HasPrefix(kv, "XDG_CONFIG_HOME=") {
				e.Vars[i] = "XDG_CONFIG_HOME=" + dir
			}
		}
	}
	use(&e, cfg)
	if err := CheckEnv(e); err != nil {
		t.Fatalf("copy in the run refused: %v", err)
	}
	for _, dir := range []string{filepath.Join(home, ".config"), filepath.Join(home, "asagao-test", "realcheck-runs", "other", "config")} {
		e2 := NewEnv(home, bin, "/usr/bin", "20260101-000000")
		use(&e2, dir)
		if err := CheckEnv(e2); err == nil {
			t.Errorf("config %s accepted", dir)
		}
	}
}

func TestParseBench(t *testing.T) {
	vs := parseBench("main=/a, lazy=/b")
	if len(vs) != 3 || vs[0].Bin != "/a" || vs[0].Offline || !vs[2].Eager || vs[2].Bin != "/b" || !vs[1].Offline {
		t.Fatalf("%+v", vs)
	}
}
